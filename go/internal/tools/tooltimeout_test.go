package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kalandramo/tianshu/go/internal/contract"
)

// tooltimeout_test.go —— 工具级超时（第九十九刀）。
//
// # 缺口
//
// `Tool.Timeout(p)` 被 **35 个工具**声明（`registry.go:40`），但执行链上
// **零消费**——`loop.go` 全文无 `context.WithTimeout`，`registry.Execute`
// 直接 `tool.Execute(ctx, p)`（`registry.go:341`）。
//
// 对账 TS `tool-pipeline.ts:1501`：
//
//	const toolTimeout = toolDef?.timeoutMs?.(params) ?? DEFAULT_TOOL_TIMEOUT_MS
//	… withToolTimeout(execution, tu.name, toolTimeout, …)
//
// TS 注释（`:725-728`）写明它存在的理由：「unknown future hang becomes a
// visible timeout instead of a **wedged turn**」——2026-09-08 的写后挂起事故。
//
// # 语义（关键分叉）
//
// TS 侧**只有部分工具**声明 `timeoutMs`（web-crawl / council / browser /
// delegate / starflow / plan-task）；`web_fetch` / `web_map` **没声明**
// → 走 `?? DEFAULT_TOOL_TIMEOUT_MS`。
//
// 故 Go 侧 `Timeout() → 0` 的正确解释是 **「未声明」= 用默认 120s**，
// **不是**「无超时」。本文件把该语义钉住。

// slowTool 是可控耗时的测试工具。
type slowTool struct {
	delay time.Duration
	// timeout 是它声明的超时（0 = 未声明 → 用默认）。
	timeout time.Duration
	ran     bool
}

func (s *slowTool) Definition() contract.Definition {
	return contract.Definition{Name: "slow_tool"}
}

func (s *slowTool) Execute(ctx context.Context, _ *CallParams) (contract.Result, error) {
	s.ran = true
	select {
	case <-time.After(s.delay):
		return contract.Result{Content: "done"}, nil
	case <-ctx.Done():
		return contract.Result{}, ctx.Err()
	}
}

func (s *slowTool) RequiresApproval(*CallParams) bool { return false }
func (s *slowTool) ConcurrencySafe() bool             { return true }
func (s *slowTool) Enabled() bool                     { return true }
func (s *slowTool) Timeout(*CallParams) time.Duration { return s.timeout }

// ── 常量 ────────────────────────────────────────────────────────────────

// TestDefaultToolTimeoutMatchesTS —— 默认超时对账 TS `DEFAULT_TOOL_TIMEOUT_MS`。
func TestDefaultToolTimeoutMatchesTS(t *testing.T) {
	if DefaultToolTimeout != 120*time.Second {
		t.Errorf("默认工具超时应为 120s（对账 TS `DEFAULT_TOOL_TIMEOUT_MS = 120_000`），实得 %v", DefaultToolTimeout)
	}
}

// ── 核心：超时生效 ──────────────────────────────────────────────────────

// TestToolTimeoutKillsHangingTool —— ★ **核心验收**：挂起的工具被超时中止。
//
// 这是「wedged turn」的防线——没有它，一个挂死的工具会冻住整个回合。
func TestToolTimeoutKillsHangingTool(t *testing.T) {
	reg := NewRegistry()
	st := &slowTool{delay: 10 * time.Second, timeout: 50 * time.Millisecond}
	reg.Register(st)

	start := time.Now()
	_, err := reg.Execute(context.Background(), "slow_tool", &CallParams{})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("超时应报错（对账 TS `withToolTimeout` 的 reject）")
	}
	if elapsed > 2*time.Second {
		t.Errorf("应在 ~50ms 被中止，实际耗时 %v", elapsed)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("错误应含 timed out，实得 %q", err.Error())
	}
	if !strings.Contains(err.Error(), "slow_tool") {
		t.Errorf("错误应含工具名，实得 %q", err.Error())
	}
}

// TestToolTimeoutMessageParity —— 超时文案对账 TS。
//
// TS：`Tool ${toolName} timed out after ${timeoutMs / 1000}s ${TOOL_TIMEOUT_RECOVERY_HINT}`
func TestToolTimeoutMessageParity(t *testing.T) {
	reg := NewRegistry()
	st := &slowTool{delay: 10 * time.Second, timeout: 50 * time.Millisecond}
	reg.Register(st)

	_, err := reg.Execute(context.Background(), "slow_tool", &CallParams{})
	if err == nil {
		t.Fatal("应超时")
	}
	// 秒数格式化（50ms → 0.05s）——TS 是 `${timeoutMs / 1000}s`
	if !strings.Contains(err.Error(), "Tool slow_tool timed out after 0.05s") {
		t.Errorf("文案应逐字对账 TS，实得 %q", err.Error())
	}
}

// TestToolTimeoutZeroMeansDefault —— ★ **`0` 是「未声明」，不是「无超时」**。
//
// 对账 TS：`toolDef?.timeoutMs?.(params) ?? DEFAULT_TOOL_TIMEOUT_MS`
// —— 没声明 `timeoutMs` 的工具走默认值。
//
// TS 侧 `web_fetch` / `web_map` 正属此类（grep 确认未声明）。
func TestToolTimeoutZeroMeansDefault(t *testing.T) {
	reg := NewRegistry()
	// 声明 0（= 未声明）但实际挂起
	st := &slowTool{delay: 10 * time.Second, timeout: 0}
	reg.Register(st)

	// 用极短的默认值注入，避免测试真等 120s
	reg.SetDefaultToolTimeout(50 * time.Millisecond)

	start := time.Now()
	_, err := reg.Execute(context.Background(), "slow_tool", &CallParams{})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("声明 0 的工具也应有默认超时（否则挂死会冻住回合）")
	}
	if elapsed > 2*time.Second {
		t.Errorf("应在默认值处被中止，实际耗时 %v", elapsed)
	}
}

// TestToolTimeoutExplicitWins —— 显式声明优先于默认。
func TestToolTimeoutExplicitWins(t *testing.T) {
	reg := NewRegistry()
	reg.SetDefaultToolTimeout(10 * time.Second) // 默认很长
	st := &slowTool{delay: 10 * time.Second, timeout: 50 * time.Millisecond}
	reg.Register(st)

	start := time.Now()
	_, err := reg.Execute(context.Background(), "slow_tool", &CallParams{})
	if err == nil {
		t.Fatal("应超时")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("应取显式声明的 50ms，实得耗时 %v", elapsed)
	}
}

// TestToolFastPathUnaffected —— 正常完成的工具**不受影响**。
//
// 回归钉子：接线不能把快工具的返回值/错误语义改掉。
func TestToolFastPathUnaffected(t *testing.T) {
	reg := NewRegistry()
	st := &slowTool{delay: time.Millisecond, timeout: 30 * time.Second}
	reg.Register(st)

	r, err := reg.Execute(context.Background(), "slow_tool", &CallParams{})
	if err != nil {
		t.Fatalf("快工具不该出错：%v", err)
	}
	if r.Content != "done" {
		t.Errorf("实得 %q", r.Content)
	}
	if r.IsError {
		t.Error("不该是错误结果")
	}
}

// TestToolTimeoutDoesNotLeakOnParentCancel —— **父 ctx 取消 ≠ 工具超时**。
//
// # 为什么要区分（变异反证 M4' 的教训）
//
// 首版只断言「err != nil」+ 耗时——**无区分力**：把「父取消」误报成
// 「工具超时」的错误类型，测试照样绿（M4' 的红来自别的用例，不是这条）。
//
// 两者语义不同：
//   - 父取消 → `context.Canceled`（上层主动中止，不是工具的错）
//   - 工具超时 → `*ToolTimeoutError`（该工具挂死了）
//
// 混淆的后果：上层会把「用户按了 Ctrl-C」当成「工具超时」去重试/降级。
func TestToolTimeoutDoesNotLeakOnParentCancel(t *testing.T) {
	reg := NewRegistry()
	st := &slowTool{delay: 10 * time.Second, timeout: 30 * time.Second}
	reg.Register(st)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := reg.Execute(ctx, "slow_tool", &CallParams{})
	if err == nil {
		t.Fatal("父 ctx 取消应中止")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("应被父 ctx 及时中止，实得 %v", elapsed)
	}
	// ★ 关键断言：必须是父取消的错误，**不能**是工具超时错误
	var timeoutErr *ToolTimeoutError
	if errors.As(err, &timeoutErr) {
		t.Errorf("父 ctx 取消不该报成工具超时（应回传 context.Canceled），实得 %T: %v", err, err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("父 ctx 取消应回传 context.Canceled，实得 %T: %v", err, err)
	}
}

// TestToolTimeoutErrorIsDistinguishable —— 工具超时的错误**类型**可判别。
//
// 上层要据此决定「重试/降级」（超时）还是「中止」（父取消）。
func TestToolTimeoutErrorIsDistinguishable(t *testing.T) {
	reg := NewRegistry()
	st := &slowTool{delay: 10 * time.Second, timeout: 50 * time.Millisecond}
	reg.Register(st)

	_, err := reg.Execute(context.Background(), "slow_tool", &CallParams{})
	if err == nil {
		t.Fatal("应超时")
	}
	var timeoutErr *ToolTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("应是 *ToolTimeoutError，实得 %T: %v", err, err)
	}
	if timeoutErr.Tool != "slow_tool" {
		t.Errorf("应记录工具名，实得 %q", timeoutErr.Tool)
	}
	if timeoutErr.Timeout != 50*time.Millisecond {
		t.Errorf("应记录超时值，实得 %v", timeoutErr.Timeout)
	}
}

// TestToolUnknownStillErrors —— 未知工具仍走原错误路径（不被超时包装影响）。
func TestToolUnknownStillErrors(t *testing.T) {
	reg := NewRegistry()
	_, err := reg.Execute(context.Background(), "nope", &CallParams{})
	if err == nil {
		t.Fatal("未知工具应报错")
	}
	if strings.Contains(err.Error(), "timed out") {
		t.Errorf("未知工具不该报超时，实得 %q", err.Error())
	}
}

// TestToolTimeoutNilContextTolerated —— **nil ctx 不得 panic**。
//
// # 为什么补这条（接线引入的回归）
//
// `executeWithToolTimeout` 首版直接把传入的 ctx 交给 `context.WithTimeout`
// ——**对 nil parent 会 panic**。而 `Registry.Execute` 的既有调用方有传 nil 的
// （`acceptance_exportfile_test.go:31` 的测试捷径），且各工具自己在 `Execute`
// 里都做了 `if ctx == nil { ctx = context.Background() }`——**nil 在此层是
// 被接受的输入**。
//
// 后果实测：接线后全量测试从 0 FAIL 变成 2 FAIL（panic）。
// 这是「加一层包装却收窄了上层契约」的具体形态。
func TestToolTimeoutNilContextTolerated(t *testing.T) {
	reg := NewRegistry()
	st := &slowTool{delay: time.Millisecond, timeout: 5 * time.Second}
	reg.Register(st)

	r, err := reg.Execute(nil, "slow_tool", &CallParams{})
	if err != nil {
		t.Fatalf("nil ctx 不该出错：%v", err)
	}
	if r.Content != "done" {
		t.Errorf("实得 %q", r.Content)
	}
}

// TestToolTimeoutNilContextStillTimesOut —— nil ctx 下超时**仍然生效**。
//
// 上一条只证明「不崩」；这条证明「兜底默认值仍在工作」——
// 否则 nil ctx 会变成「绕过超时的后门」。
func TestToolTimeoutNilContextStillTimesOut(t *testing.T) {
	reg := NewRegistry()
	st := &slowTool{delay: 10 * time.Second, timeout: 50 * time.Millisecond}
	reg.Register(st)

	start := time.Now()
	_, err := reg.Execute(nil, "slow_tool", &CallParams{})
	if err == nil {
		t.Fatal("nil ctx 也应有超时")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("应被超时中止，实得 %v", elapsed)
	}
	var timeoutErr *ToolTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Errorf("应是 *ToolTimeoutError，实得 %T", err)
	}
}
