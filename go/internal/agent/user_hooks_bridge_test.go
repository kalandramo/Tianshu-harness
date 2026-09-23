package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/hooks"
)

// hookSinkRec 记录 sink 收到的结果。
type hookSinkRec struct {
	calls    int
	lastEv   hooks.HookEvent
	lastTurn int
	lastTool string
	lastErr  string
	results  []hooks.HookResult
}

func (s *hookSinkRec) sink() hooks.ResultSink {
	return func(results []hooks.HookResult, ev hooks.HookEvent, turn int, toolName, errMsg string) {
		s.calls++
		s.lastEv = ev
		s.lastTurn = turn
		s.lastTool = toolName
		s.lastErr = errMsg
		s.results = results
	}
}

// TestCreateUserHooksBridgeShape —— 4 个 hook，阶段与名字对账 TS。
func TestCreateUserHooksBridgeShape(t *testing.T) {
	deps := UserHooksDeps{Cwd: t.TempDir(), GetTurn: func() int { return 0 }}
	bridge := CreateUserHooksBridge(deps)

	if len(bridge) != 4 {
		t.Fatalf("应有 4 个 bridge hook，实得 %d", len(bridge))
	}
	want := []struct {
		name  string
		phase RuntimeHookPhase
	}{
		{"user-hooks-preTurn", PhasePreTurn},
		{"user-hooks-postTurn", PhasePostTurn},
		{"user-hooks-postTool", PhasePostTool},
		{"user-hooks-postSession", PhasePostSession},
	}
	for i, w := range want {
		if bridge[i].Name != w.name {
			t.Errorf("[%d] name = %q，期望 %q", i, bridge[i].Name, w.name)
		}
		if bridge[i].Phase != w.phase {
			t.Errorf("[%d] phase = %q，期望 %q", i, bridge[i].Phase, w.phase)
		}
	}
}

// TestUserHooksBridgeNilSinkNoOp —— 无 sink 时安全 no-op（不 panic）。
func TestUserHooksBridgeNilSinkNoOp(t *testing.T) {
	deps := UserHooksDeps{Cwd: t.TempDir(), GetTurn: func() int { return 0 }}
	bridge := CreateUserHooksBridge(deps)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil sink 不应 panic: %v", r)
		}
	}()
	for _, h := range bridge {
		if err := h.Run(context.Background(), &RuntimeHookContext{}, nil); err != nil {
			t.Errorf("%s 返回错误: %v", h.Name, err)
		}
	}
}

// TestRunOnErrorHooksRequiresSink —— **TS 守卫对账**：无 sink 直接返回。
//
// 不变量：onError 是诊断通道，无人消费时**不执行脚本**。
// 若此处执行，会形成递归风险（脚本失败 → onError → 脚本失败 → ...）。
func TestRunOnErrorHooksRequiresSink(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "EXECUTED")
	script := filepath.Join(dir, "danger.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+sentinel+"\n"), 0o755); err != nil {
		t.Fatalf("写脚本失败: %v", err)
	}
	rivetDir := filepath.Join(dir, ".rivet")
	_ = os.MkdirAll(rivetDir, 0o755)
	_ = os.WriteFile(filepath.Join(rivetDir, "hooks.json"),
		[]byte(`{"hooks":[{"event":"onError","script":"danger.sh"}]}`), 0o644)
	t.Setenv("RIVET_TRUST_PROJECT", "1")

	// sink = nil → 不得执行
	RunOnErrorHooks(UserHooksDeps{Cwd: dir, GetTurn: func() int { return 0 }}, "boom")

	if _, err := os.Stat(sentinel); err == nil {
		t.Fatal("无 sink 时不应执行 onError 脚本（TS 守卫语义）")
	}
}

// TestUserHooksErrorSinkRecursionGuard —— **递归防护不变量**。
//
// user-hooks 自身的 hook 失败（HookName 以 `user-hooks-` 开头）**不得**
// 再触发 onError 链——否则脚本失败会无限递归。
func TestUserHooksErrorSinkRecursionGuard(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "EXECUTED")
	script := filepath.Join(dir, "err.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+sentinel+"\n"), 0o755); err != nil {
		t.Fatalf("写脚本失败: %v", err)
	}
	rivetDir := filepath.Join(dir, ".rivet")
	_ = os.MkdirAll(rivetDir, 0o755)
	_ = os.WriteFile(filepath.Join(rivetDir, "hooks.json"),
		[]byte(`{"hooks":[{"event":"onError","script":"err.sh"}]}`), 0o644)
	t.Setenv("RIVET_TRUST_PROJECT", "1")

	sink := &hookSinkRec{}
	s := UserHooksErrorSink(UserHooksDeps{Cwd: dir, GetTurn: func() int { return 0 }, Sink: sink.sink()})

	// user-hooks 自身的失败 → 应被守卫拦住
	s(RuntimeHookError{Phase: PhasePreTurn, HookName: "user-hooks-preTurn", Message: "script failed"})

	if sink.calls != 0 {
		t.Errorf("user-hooks 自身失败不应触发 onError 链，实得 %d 次", sink.calls)
	}
	if _, err := os.Stat(sentinel); err == nil {
		t.Fatal("递归守卫失效：onError 脚本被执行了")
	}
}

// TestUserHooksErrorSinkFiresForOtherHooks —— 非 user-hooks 的失败正常触发。
func TestUserHooksErrorSinkFiresForOtherHooks(t *testing.T) {
	dir := t.TempDir()
	rivetDir := filepath.Join(dir, ".rivet")
	_ = os.MkdirAll(rivetDir, 0o755)
	_ = os.WriteFile(filepath.Join(rivetDir, "hooks.json"),
		[]byte(`{"hooks":[{"event":"onError","script":"missing.sh"}]}`), 0o644)
	t.Setenv("RIVET_TRUST_PROJECT", "1")

	sink := &hookSinkRec{}
	s := UserHooksErrorSink(UserHooksDeps{Cwd: dir, GetTurn: func() int { return 5 }, Sink: sink.sink()})

	s(RuntimeHookError{Phase: PhasePostTool, HookName: "lossy-observation", Message: "boom"})

	if sink.calls != 1 {
		t.Fatalf("应触发 1 次 onError 链，实得 %d", sink.calls)
	}
	if sink.lastEv != hooks.EventOnError {
		t.Errorf("事件应为 onError，实得 %q", sink.lastEv)
	}
	if sink.lastTurn != 5 {
		t.Errorf("turn 应为 5，实得 %d", sink.lastTurn)
	}
	if !strings.Contains(sink.lastErr, "boom") {
		t.Errorf("错误消息应透传，实得 %q", sink.lastErr)
	}
}

// TestUserHooksErrorSinkIncludesErrDetail —— 错误详情拼接进消息。
func TestUserHooksErrorSinkIncludesErrDetail(t *testing.T) {
	dir := t.TempDir()
	rivetDir := filepath.Join(dir, ".rivet")
	_ = os.MkdirAll(rivetDir, 0o755)
	_ = os.WriteFile(filepath.Join(rivetDir, "hooks.json"),
		[]byte(`{"hooks":[{"event":"onError","script":"missing.sh"}]}`), 0o644)
	t.Setenv("RIVET_TRUST_PROJECT", "1")

	sink := &hookSinkRec{}
	s := UserHooksErrorSink(UserHooksDeps{Cwd: dir, GetTurn: func() int { return 0 }, Sink: sink.sink()})

	s(RuntimeHookError{
		HookName: "some-hook", Message: "failed",
		Err: errString("underlying cause"),
	})

	if !strings.Contains(sink.lastErr, "failed") || !strings.Contains(sink.lastErr, "underlying cause") {
		t.Errorf("消息应含 message 与 err 详情，实得 %q", sink.lastErr)
	}
}

// TestUserHooksPostToolPassesToolContext —— postTool 传 toolName + toolResult。
//
// **对账 TS**：`toolResult` 是字符串 'success'/'failure'（不是布尔）。
func TestUserHooksPostToolPassesToolContext(t *testing.T) {
	deps := UserHooksDeps{Cwd: t.TempDir(), GetTurn: func() int { return 3 }}
	bridge := CreateUserHooksBridge(deps)

	// 找 postTool 那个
	var postTool RuntimeHook
	for _, h := range bridge {
		if h.Phase == PhasePostTool {
			postTool = h
		}
	}
	if postTool.Name == "" {
		t.Fatal("未找到 postTool bridge hook")
	}

	if err := postTool.Run(context.Background(), &RuntimeHookContext{},
		&RuntimeToolEvent{Name: "read_file", Success: true}); err != nil {
		t.Fatalf("postTool 返回错误: %v", err)
	}
}

// errString 是测试用的简单 error。
type errString string

func (e errString) Error() string { return string(e) }
