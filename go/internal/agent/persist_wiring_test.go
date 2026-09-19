package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/api/wire"
	"github.com/kalandramo/tianshu/go/internal/client"
	"github.com/kalandramo/tianshu/go/internal/contract"
	"github.com/kalandramo/tianshu/go/internal/session"
	"github.com/kalandramo/tianshu/go/internal/tools"
)

// ── 测试辅助 ──

func orderedUserMessage(text string) *wire.OrderedMap {
	return wire.NewOrderedMap().Set("role", "user").Set("content", text)
}

func orderedAssistantMessage(text string) *wire.OrderedMap {
	return wire.NewOrderedMap().Set("role", "assistant").Set("content", text)
}

func usageOf(in, out, cacheRead, cacheCreate int, reasoning *int) contract.Usage {
	return contract.Usage{
		InputTokens:              in,
		OutputTokens:             out,
		CacheReadInputTokens:     cacheRead,
		CacheCreationInputTokens: cacheCreate,
		ReasoningTokens:          reasoning,
	}
}

// sessionIDForTest 暴露 main 包的 ID 生成逻辑做格式验证。
//
// 注意：main 包的 newSessionID 不可导出，这里复刻同样格式做断言
// （格式契约：无路径分隔符、长度足够、两次不同）。
func sessionIDForTest() string {
	return session.NewID()
}

// TestLoopSessionPersistWiring —— 端到端验证会话落盘接线。
//
// 不碰网络：直接构造 Loop（带 SessionID）+ 手动 appendAndPersist，
// 验证消息真落到 `<cwd>/.rivet/sessions/<id>.jsonl`。
//
// 这补上了单元测试抓不到的**接线缺口**：Loop 的 New 是否真建了 Persist、
// appendAndPersist 是否真调了 Listener、FlushSession 是否真排空。
func TestLoopSessionPersistWiring(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewDefaultRegistry(tools.Options{Cwd: dir})

	loop := New(Config{
		SessionID: "e2e-test",
		Cwd:       dir,
		Model:     "test-model",
	}, client.New(client.Config{}), reg)

	if loop.Persist == nil {
		t.Fatal("Loop.Persist 未接线（New 未构造 Persist）")
	}
	if loop.Listener == nil {
		t.Fatal("Loop.Listener 未接线")
	}
	if loop.State == nil {
		t.Fatal("Loop.State 未接线")
	}

	// 手动追加（绕过网络）
	loop.appendAndPersist(orderedUserMessage("hello world"))
	loop.appendAndPersist(orderedAssistantMessage("hi there"))

	// 收尾
	loop.FlushSession()

	// 验证文件真存在且可读回
	path := filepath.Join(dir, ".rivet", "sessions", "e2e-test.jsonl")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatalf("会话文件未创建：%s", path)
	}

	got := loop.Persist.LoadOai()
	if len(got) != 2 {
		t.Fatalf("应读回 2 条消息，得到 %d", len(got))
	}
	if got[0].Role != "user" || got[0].Content == nil || *got[0].Content != "hello world" {
		t.Errorf("第一条不符：%+v", got[0])
	}
	if got[1].Role != "assistant" || got[1].Content == nil || *got[1].Content != "hi there" {
		t.Errorf("第二条不符：%+v", got[1])
	}
}

// TestLoopSessionPersistDisabledWithoutSessionID —— 无 SessionID 时不落盘。
func TestLoopSessionPersistDisabledWithoutSessionID(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewDefaultRegistry(tools.Options{Cwd: dir})

	loop := New(Config{Cwd: dir, Model: "test-model"}, client.New(client.Config{}), reg)
	if loop.Persist != nil {
		t.Error("无 SessionID 时不应构造 Persist")
	}
	if loop.Listener != nil {
		t.Error("无 SessionID 时不应构造 Listener")
	}
	// appendAndPersist 应安全降级
	loop.appendAndPersist(orderedUserMessage("no persist"))
	if len(loop.Messages()) != 1 {
		t.Error("无持久化时消息仍应进内存历史")
	}
	// FlushSession 应安全（不 panic）
	loop.FlushSession()
}

// TestLoopRecordUsageAccumulates —— usage 累加接线。
func TestLoopRecordUsageAccumulates(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewDefaultRegistry(tools.Options{Cwd: dir})
	loop := New(Config{SessionID: "usage-test", Cwd: dir, Model: "m"}, client.New(client.Config{}), reg)

	rt := 40
	loop.recordUsage(usageOf(1000, 200, 900, 50, &rt))
	loop.recordUsage(usageOf(2000, 300, 1800, 0, nil))

	u := loop.State.TotalUsage()
	if u.InputTokens != 3000 {
		t.Errorf("InputTokens 应累计 3000，得到 %d", u.InputTokens)
	}
	if u.OutputTokens != 500 {
		t.Errorf("OutputTokens 应累计 500，得到 %d", u.OutputTokens)
	}
	if u.CacheReadInputTokens != 2700 {
		t.Errorf("CacheRead 应累计 2700，得到 %d", u.CacheReadInputTokens)
	}
	if u.ReasoningTokens != 40 {
		t.Errorf("Reasoning 应累计 40，得到 %d", u.ReasoningTokens)
	}
}

// TestLoopPersistMetadataAfterAppend —— 落盘后元数据被更新。
func TestLoopPersistMetadataAfterAppend(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewDefaultRegistry(tools.Options{Cwd: dir})
	loop := New(Config{SessionID: "meta-test", Cwd: dir, Model: "m"}, client.New(client.Config{}), reg)

	loop.appendAndPersist(orderedUserMessage("my first question"))
	loop.FlushSession()

	meta := loop.Persist.Metadata().Load()
	if meta == nil {
		t.Fatal("元数据未写入")
	}
	if meta.TurnCount != 1 {
		t.Errorf("turnCount 应为 1，得到 %d", meta.TurnCount)
	}
	if meta.Title != "my first question" {
		t.Errorf("title 应为问题原文，得到 %q", meta.Title)
	}
	// 元数据文件应存在
	metaPath := filepath.Join(dir, ".rivet", "sessions", "meta-test.meta.json")
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("元数据文件未创建：%v", err)
	}
	if !strings.Contains(string(raw), "my first question") {
		t.Error("元数据文件应含 title")
	}
}

// TestLoopSessionDirUnderCwd —— 会话目录落在 config.Cwd 下。
//
// **不**断言「进程 cwd 下没有文件」——那依赖测试运行目录，脆弱且会被
// 其他测试/变异残留污染。正向断言足以锁定行为：文件出现在 config.Cwd 下
// 即证明用的是注入的 Cwd 而非进程 cwd（两者在测试里必然不同）。
func TestLoopSessionDirUnderCwd(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewDefaultRegistry(tools.Options{Cwd: dir})
	loop := New(Config{SessionID: "dir-test", Cwd: dir, Model: "m"}, client.New(client.Config{}), reg)

	loop.appendAndPersist(orderedUserMessage("x"))
	loop.FlushSession()

	want := filepath.Join(dir, ".rivet", "sessions", "dir-test.jsonl")
	if _, err := os.Stat(want); os.IsNotExist(err) {
		t.Errorf("会话文件应建在 config.Cwd 下：%s", want)
	}
}

// TestNewSessionIDFormat —— 会话 ID 格式（文件系统安全）。
func TestNewSessionIDFormat(t *testing.T) {
	id := sessionIDForTest()
	if id == "" {
		t.Fatal("会话 ID 不应为空")
	}
	if strings.ContainsAny(id, "/\\:*?\"<>|") {
		t.Errorf("会话 ID 含文件系统不安全字符：%q", id)
	}
	if len(id) < 10 {
		t.Errorf("会话 ID 过短：%q", id)
	}
	// 两次生成应不同
	if sessionIDForTest() == id {
		t.Error("两次生成的会话 ID 应不同")
	}
}

// TestSessionPersistAcrossLoopInstances —— 跨 Loop 实例读回（会话恢复）。
func TestSessionPersistAcrossLoopInstances(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewDefaultRegistry(tools.Options{Cwd: dir})

	l1 := New(Config{SessionID: "resume-test", Cwd: dir, Model: "m"}, client.New(client.Config{}), reg)
	l1.appendAndPersist(orderedUserMessage("turn one"))
	l1.FlushSession()
	l1.Persist.Close()

	// 新实例读同一会话
	l2 := New(Config{SessionID: "resume-test", Cwd: dir, Model: "m"}, client.New(client.Config{}), reg)
	defer l2.Persist.Close()
	got := l2.Persist.LoadOai()
	if len(got) != 1 {
		t.Fatalf("跨实例应读回 1 条，得到 %d", len(got))
	}
	if got[0].Content == nil || *got[0].Content != "turn one" {
		t.Errorf("内容不符：%+v", got[0])
	}
	_ = session.SessionDir(dir) // 引用保证 session 包被使用
}
