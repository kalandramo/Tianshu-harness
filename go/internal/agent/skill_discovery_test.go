package agent

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/skills"
	"github.com/kalandramo/tianshu/go/internal/tools"
)

// skillDiscoveryRegistry 建一个含夹具的 skill 注册表。
//
// 带 trigger 的那个走 `ParseSkillMarkdown` 真实构造（避免测试绕过
// trigger 编译路径）。
func skillDiscoveryRegistry(t *testing.T) *skills.Registry {
	t.Helper()
	r := skills.NewRegistry()
	r.Register(skills.Definition{
		Name: "brainstorming", Description: "Explore intent before implementing",
	})
	def, err := skills.ParseSkillMarkdown(
		"---\nname: design-it\ndescription: Turn a brief into an HTML design doc\ntriggers: [\"design\", \"原型\"]\n---\nBody here.\n",
		"design-it.md")
	if err != nil {
		t.Fatalf("构造夹具失败：%v", err)
	}
	r.Register(def)
	return r
}

// TestSkillDiscoveryInjectedIntoRequest —— **本刀的核心断言**。
//
// 发现层必须真正进入请求体——否则 `skill` 工具对模型**不可达**（模型不知道
// 任何 skill 名，调用只会撞「未找到」）。
//
// 这是上一刀（第三十九刀）明确留下的遗留项：`RenderDiscoveryBlock` 当时是
// 纯函数、无生产消费方。
func TestSkillDiscoveryInjectedIntoRequest(t *testing.T) {
	sc := &scriptedServer{responses: []string{textTurn("你好")}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{
		Model: "m", MaxTokens: 100,
		SkillRegistry: skillDiscoveryRegistry(t),
	})

	if err := l.Run(t.Context(), "帮我设计一个原型"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	if len(sc.bodies) == 0 {
		t.Fatal("未捕获请求体")
	}
	body := sc.bodies[0]

	// 块头必须在
	if !strings.Contains(body, "<available-skills") {
		t.Errorf("**发现层未进入请求体**——skill 工具对模型不可达。\n请求体片段：%s", truncateForTest(body, 600))
	}
	// 每个 skill 的 name + description 都应在
	for _, want := range []string{"brainstorming", "Explore intent before implementing", "design-it"} {
		if !strings.Contains(body, want) {
			t.Errorf("发现层应含 %q：%s", want, truncateForTest(body, 600))
		}
	}
	// 正文**绝不**进发现层（只有 name + description）。
	//
	// **注意**：发现层自身用 `<skill name="..." >desc</skill>` 标签——那是
	// 发现层的正常形态，不是正文。正文的形态是
	// `<skill name="X">\n<body>\n</skill>`（正文独立成行）。
	// 故断言 fixture 正文文本不出现。
	if strings.Contains(body, "Body here.") {
		t.Errorf("发现层不应含 skill 正文（Body here.）：%s", truncateForTest(body, 600))
	}
}

// trigger 匹配的用户输入 → 相关 skill 标 relevant 并排前。
//
// 对账 TS 的 `renderDiscoveryBlock(userInput, ...)`。
func TestSkillDiscoveryUsesUserInputAsHint(t *testing.T) {
	sc := &scriptedServer{responses: []string{textTurn("ok")}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{
		Model: "m", MaxTokens: 100,
		SkillRegistry: skillDiscoveryRegistry(t),
	})
	// 用户输入含 "设计" → design-it 的 trigger 命中
	if err := l.Run(t.Context(), "帮我设计一个原型"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	body := sc.bodies[0]
	// **注意**：sc.bodies 是 JSON 序列化的请求体，引号被转义为 \"。
	// 故断言必须匹配转义形式。
	if !strings.Contains(body, `name=\"design-it\" relevant=\"true\"`) {
		t.Errorf("命中 trigger 的 skill 应标 relevant：%s", truncateForTest(body, 600))
	}
	// 未命中的不应标 relevant
	if strings.Contains(body, `name=\"brainstorming\" relevant=\"true\"`) {
		t.Errorf("未命中 trigger 的 skill 不应标 relevant")
	}
}

// **反证**：无 SkillRegistry 时不注入发现层（且不影响其他注入）。
func TestSkillDiscoveryAbsentWithoutRegistry(t *testing.T) {
	sc := &scriptedServer{responses: []string{textTurn("hi")}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100})
	if err := l.Run(t.Context(), "hi"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	if strings.Contains(sc.bodies[0], "<available-skills") {
		t.Error("未装注册表时不应有发现层")
	}
}

// **反证**：空注册表不注入空块（避免噪音）。
func TestSkillDiscoveryEmptyRegistryNoBlock(t *testing.T) {
	sc := &scriptedServer{responses: []string{textTurn("hi")}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{
		Model: "m", MaxTokens: 100,
		SkillRegistry: skills.NewRegistry(),
	})
	if err := l.Run(t.Context(), "hi"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	if strings.Contains(sc.bodies[0], "<available-skills") {
		t.Error("空注册表不应注入发现层")
	}
}

// **反证（本刀修掉的既有缺陷）**：`Advisories == nil` 时**其他注入源照常工作**。
//
// 原实现 `buildRequestMessages` 在 `l.Advisories == nil` 时直接
// `return l.messages`——那是**早退吞注入**：新增第二个注入源后，未装
// advisory bus 的会话就永远拿不到它。
//
// 本测试锁定「advisory 缺席 ≠ 发现层缺席」。
func TestSkillDiscoveryWorksWithoutAdvisoryBus(t *testing.T) {
	sc := &scriptedServer{responses: []string{textTurn("hi")}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{
		Model: "m", MaxTokens: 100,
		SkillRegistry: skillDiscoveryRegistry(t),
	})
	// l.Advisories 显式为 nil
	if l.Advisories != nil {
		t.Fatal("前置条件：Advisories 应为 nil")
	}
	if err := l.Run(t.Context(), "hi"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	if !strings.Contains(sc.bodies[0], "<available-skills") {
		t.Error("**早退吞注入**——无 advisory bus 时发现层被吞掉了")
	}
}

// **反证**：发现层不写回历史（缓存安全的前提）。
//
// **两层断言**（缺一不可）：
//  1. 内容层：历史里无 available-skills 痕迹
//  2. **条数层**：注入不改变历史长度
//
// 第 2 层是本质不变量——只查内容会漏掉「写回但内容不同」的实现
// （M154 变异实测：写回一条 "leak" 消息时内容断言 0 红）。
func TestSkillDiscoveryNotPersistedToHistory(t *testing.T) {
	sc := &scriptedServer{responses: []string{
		toolTurnArgs("c1", "read_file", map[string]any{"file_path": "nope.txt"}),
		textTurn("完成"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{
		Model: "m", MaxTokens: 100,
		SkillRegistry: skillDiscoveryRegistry(t),
	})
	if err := l.Run(t.Context(), "读文件"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	// 1) 内容层：持久化历史里不应有任何 available-skills 痕迹
	for i, m := range l.Messages() {
		if c, ok := m.Get("content"); ok {
			if s, ok := c.(string); ok && strings.Contains(s, "<available-skills") {
				t.Errorf("历史消息[%d] 含发现层——不应写回", i)
			}
		}
	}

	// 2) 条数层：user(1) + assistant(tool_calls) + tool + assistant(text) = 4
	//
	// 注入**不得**改变这个数——写回会让它增长（每轮 +1）。
	msgs := l.Messages()
	if len(msgs) != 4 {
		roles := make([]string, 0, len(msgs))
		for _, m := range msgs {
			r, _ := m.Get("role")
			rs, _ := r.(string)
			roles = append(roles, rs)
		}
		t.Errorf("历史长度 = %d, want 4（注入写回了历史？roles=%v）", len(msgs), roles)
	}
	// 且不应出现 role=user 的连续两条（注入若写回会与真实 user 消息并列）。
	for i := 1; i < len(msgs); i++ {
		prev, _ := msgs[i-1].Get("role")
		cur, _ := msgs[i].Get("role")
		ps, _ := prev.(string)
		cs, _ := cur.(string)
		if ps == "user" && cs == "user" {
			t.Errorf("历史[%d,%d] 连续两条 user——注入被写回了", i-1, i)
		}
	}
}

// **端到端可达性**：发现层告知模型有某 skill → 模型调用 → 工具成功加载。
//
// 这是本刀存在的**最终判据**：上一刀交付的 skill 工具「当前不可达」，
// 本刀要让这条链真正闭合。
func TestSkillEndToEndReachable(t *testing.T) {
	sc := &scriptedServer{responses: []string{
		// 模型看到发现层后调用 skill 工具
		toolTurnArgs("c1", "skill", map[string]any{"name": "brainstorming"}),
		textTurn("已按 skill 指令执行"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	reg := tools.NewDefaultRegistry(tools.Options{Cwd: t.TempDir()})
	l := newTestLoopWithRegistry(t, srv, Config{
		Model: "m", MaxTokens: 100,
		SkillRegistry: skillDiscoveryRegistry(t),
	}, reg)

	var toolResultText string
	l.Emit = func(e Event) {
		if e.Kind == "tool_result" {
			toolResultText = e.Text
		}
	}

	if err := l.Run(t.Context(), "用 brainstorming"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	// 第一步：发现层确实告知了模型有哪些 skill
	if !strings.Contains(sc.bodies[0], "<available-skills") {
		t.Error("发现层缺失——链路第一环断了")
	}
	// 第二步：工具调用成功（不是「未找到」）
	if strings.Contains(toolResultText, "未找到 skill") {
		t.Errorf("skill 工具未找到——注册表未透传到工具。实际：%q", toolResultText)
	}
	if !strings.Contains(toolResultText, `<skill name="brainstorming">`) {
		t.Errorf("应加载 skill 正文，实际：%q", toolResultText)
	}
}

// 截断辅助（仅测试用，避免错误信息淹没输出）。
func truncateForTest(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
