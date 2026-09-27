package agent

import (
	"testing"

	"github.com/kalandramo/tianshu/go/internal/tools"
)

// sessionmodfiles_assembly_test.go —— 装配层可达性（W2b）。
//
// # 为什么单独一条
//
// W1 的接线测试用手搭的 `&Loop{State: session.New(...)}`——那证明「给定了
// State，构造点会填字段」，**不证明生产的 `New(...)` 会给到 State**。
// HANDOFF 第 49 条坑：装配层是「实现已有但零消费」的最后一道缺口——
// 子系统全实现、单测全绿，但 `main.go` 没注入，工具永不出现。
//
// # 生产路径（已核实）
//
//	cmd/tianshu/main.go:227  if app.Agent.SessionID == "" { = session.NewID() }
//	                         （**无条件补全**——CLI 路径上 SessionID 恒非空）
//	cmd/tianshu/main.go:229  loop := agent.New(app.Agent, cl, reg)
//	internal/agent/loop.go:396  if cfg.SessionID != "" { l.State = session.New(...) }
//
// 故 `New(...)` 是这条链的收口点——本文件断言它。
//
// 对账 TS：`loop-factory.ts` 的会话状态创建与被 `tool-pipeline.ts:838` 读取，
// 同一条装配链。

// TestAssemblyNewCreatesStateSoSessionModifiedFilesHasSource —— 生产构造器建 State。
//
// 这条走 `New(...)`（CLI 用的就是它），不是手搭 Loop。若将来有人把 State 的
// 创建改成惰性/条件化（如只在对齐某配置时才建），这里会红——那正是本刀
// 依赖的上游。
func TestAssemblyNewCreatesStateSoSessionModifiedFilesHasSource(t *testing.T) {
	l := New(Config{Cwd: t.TempDir(), SessionID: "assembly-modfiles"}, nil, tools.NewRegistry())
	if l.State == nil {
		t.Fatal("生产构造器 New() 应创建 State（否则 sessionModifiedFiles() 恒无源，" +
			"git commit 归属回退再次退化为报错）")
	}

	// 端到端：经生产构造器建的 Loop，写工具录入后构造点应带出字段。
	l.observeToolResult("write_file",
		map[string]any{"file_path": "zz_assembly.txt"},
		contractResultOK("ok"))

	p := l.buildToolCallParams(toolCall{id: "t1", name: "write_file"})
	if !listHas(p.SessionModifiedFiles, "zz_assembly.txt") {
		t.Errorf("生产构造器路径下 SessionModifiedFiles 应含写过的文件，实得 %v",
			p.SessionModifiedFiles)
	}
}

// TestAssemblyNewWithoutSessionIDKeepsStateNil —— 无 SessionID → State 为 nil。
//
// 这是**反向对照**（对账 `loop.go:396` 的 `if cfg.SessionID != ""`）：
// 证明上一条不是恒真——若把上一条的判据写成「永远为真」，这条会红。
// 同时它守住 Go 的「最小可跑路径」（headless 无会话时不该崩）。
func TestAssemblyNewWithoutSessionIDKeepsStateNil(t *testing.T) {
	l := New(Config{Cwd: t.TempDir()}, nil, tools.NewRegistry())
	if l.State != nil {
		t.Error("无 SessionID 时 State 应为 nil（对账 loop.go 的条件创建）")
	}
	// 且构造点不 panic（nil 安全）。
	p := l.buildToolCallParams(toolCall{id: "t1", name: "write_file"})
	if len(p.SessionModifiedFiles) != 0 {
		t.Errorf("无 State 时字段应为空，实得 %v", p.SessionModifiedFiles)
	}
}
