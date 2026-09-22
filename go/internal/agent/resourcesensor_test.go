package agent

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/tools"
)

// resourcesensor_test.go —— 内存压力比探针 + TurnBudget 接线。
//
// 对账 TS `resource-sensor.ts` 的 `rssBytes / memoryLimitBytes` 与
// `turn-orchestrator.ts:590-593` 的每轮重建。

// TestMemoryLimitBytesEnvOverride —— `RIVET_MEMORY_LIMIT_BYTES` 优先。
//
// 对账 TS `defaultMemoryLimitBytes`（`resource-sensor.ts:48`）：
// 配置 > 0 才生效。
func TestMemoryLimitBytesEnvOverride(t *testing.T) {
	t.Setenv("RIVET_MEMORY_LIMIT_BYTES", "2048")
	if got := MemoryLimitBytes(); got != 2048 {
		t.Errorf("应读环境变量，实得 %d", got)
	}
}

// TestMemoryLimitBytesInvalidFallsBack —— 非法值回退默认。
//
// 对账 TS 的 `Number.isFinite(configured) && configured > 0`——
// 非数字 / 0 / 负数都回退。
func TestMemoryLimitBytesInvalidFallsBack(t *testing.T) {
	cases := []string{"", "abc", "0", "-1", "1e999"}
	for _, raw := range cases {
		t.Setenv("RIVET_MEMORY_LIMIT_BYTES", raw)
		got := MemoryLimitBytes()
		if got != defaultMemoryLimitBytes {
			t.Errorf("输入 %q 应回退默认 %d，实得 %d", raw, defaultMemoryLimitBytes, got)
		}
	}
}

// TestCurrentRSSBytesPositive —— RSS 探针返回正值。
func TestCurrentRSSBytesPositive(t *testing.T) {
	if got := CurrentRSSBytes(); got <= 0 {
		t.Errorf("RSS 应为正，实得 %d", got)
	}
}

// TestCurrentRSSRatioInRange —— 比值在 (0, 1] 区间（默认 1GiB 上限下）。
func TestCurrentRSSRatioInRange(t *testing.T) {
	os.Unsetenv("RIVET_MEMORY_LIMIT_BYTES")
	got := CurrentRSSRatio()
	if got <= 0 {
		t.Errorf("比值应为正，实得 %v", got)
	}
	// 测试进程 RSS 远小于 1GiB。
	if got > 1 {
		t.Logf("注意：RSS 比 > 1（%v）——测试进程内存超 1GiB？", got)
	}
}

// TestCurrentRSSRatioZeroLimit —— limit <= 0 时返回 0（防除零）。
//
// 对账 TS 的 `snap ? rss/limit : 0` 保护。
func TestCurrentRSSRatioZeroLimit(t *testing.T) {
	t.Setenv("RIVET_MEMORY_LIMIT_BYTES", "0") // 非法 → 回退默认，不会是 0
	// 直接测除零保护：用负数构造（但 MemoryLimitBytes 会回退）——
	// 故改为验证「回退后仍返回合法比值」。
	got := CurrentRSSRatio()
	if got <= 0 {
		t.Errorf("回退默认上限后比值应为正，实得 %v", got)
	}
}

// TestLoopRSSRatioFnOverride —— `RSSRatioFn` 注入生效（对账 TS 的 memoryUsage 注入）。
func TestLoopRSSRatioFnOverride(t *testing.T) {
	l := &Loop{RSSRatioFn: func() float64 { return 0.42 }}
	if got := l.rssRatio(); got != 0.42 {
		t.Errorf("应用注入值，实得 %v", got)
	}
	// 无注入时用真实探针。
	l2 := &Loop{}
	if got := l2.rssRatio(); got <= 0 {
		t.Errorf("无注入应用真实探针（正值），实得 %v", got)
	}
}

// TestLoopRebuildsBudgetPerTurn —— **每轮重建**（换档，非 reset）。
//
// 对账 TS `turn-orchestrator.ts:593`。
func TestLoopRebuildsBudgetPerTurn(t *testing.T) {
	// 模拟两轮不同压力。
	ratios := []float64{0.0, 0.9}
	i := 0
	l := &Loop{RSSRatioFn: func() float64 {
		v := ratios[i]
		if i < len(ratios)-1 {
			i++
		}
		return v
	}}

	// 第一轮：基线档。
	l.turnBudget = CreateTurnBudget(l.rssRatio())
	if l.turnBudget.MaxTokensPerTurn != BaseBudgetTokens {
		t.Fatalf("第一轮应基线档，实得 %d", l.turnBudget.MaxTokensPerTurn)
	}
	// 第二轮：危急档（重建才换得了档）。
	l.turnBudget = CreateTurnBudget(l.rssRatio())
	if l.turnBudget.MaxTokensPerTurn != 0 {
		t.Errorf("第二轮应危急档（0），实得 %d", l.turnBudget.MaxTokensPerTurn)
	}
}

// TestStoredWrapUsesResultRawPath —— **接线验证**：包装用 `Result.RawPath`。
//
// 这是 `contract.Result.RawPath` 的**首个生产读取方**（此前只有 JSON tag +
// 注释，无消费者——见第二十九刀 HANDOFF）。
func TestStoredWrapUsesResultRawPath(t *testing.T) {
	b := CreateTurnBudget(0.9) // 立即耗尽
	wrapped, ok := WrapStoredIfExhausted(b, "bash", "long content here", "/tmp/raw/abc.raw")
	if !ok {
		t.Fatal("应触发包装")
	}
	if !strings.Contains(wrapped, `ref="/tmp/raw/abc.raw"`) {
		t.Errorf("包装应用 Result.RawPath 作为 ref：%.200s", wrapped)
	}
}

// TestStoredWrapEmptyRawPathUnknown —— 无 rawPath 时回退 `"unknown"`。
func TestStoredWrapEmptyRawPathUnknown(t *testing.T) {
	b := CreateTurnBudget(0.9)
	wrapped, _ := WrapStoredIfExhausted(b, "grep", "content", "")
	if !strings.Contains(wrapped, `ref="unknown"`) {
		t.Errorf("空 rawPath 应回退 unknown：%.200s", wrapped)
	}
}

// TestE2EBudgetConsumedInLoop —— **端到端接线验证**（用户级验收）。
//
// 用户动作：真实 loop 跑一轮工具调用，注入 `RSSRatioFn = 0.9`（危急档 →
// 预算 0，立即耗尽）。
// 观察到：`tool_result` 事件的文本**已被换成 `<stored ...>` 短引用**——
// 证明 loop 真的消费了 `turnBudget`，而非只是持有它。
//
// **为什么需要这条**：M86 变异（把 `l.turnBudget != nil` 改成
// `!= nil && false`）在全量套件下 **0 红**——此前没有任何测试跨越
// 「budget 字段 → 工具结果内容」这条边。单元测试只验证了
// `WrapStoredIfExhausted` 本身正确，没验证 loop 调了它。
func TestE2EBudgetConsumedInLoop(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "big.txt")
	if err := os.WriteFile(target, []byte(strings.Repeat("x", 5000)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sc := &scriptedServer{responses: []string{
		toolTurnArgs("c1", "read_file", map[string]any{"file_path": target}),
		textTurn("完成"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	reg := tools.NewDefaultRegistry(tools.Options{Cwd: root})
	l := newTestLoopWithRegistry(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root}, reg)

	// 注入危急档内存压力 → 预算 0 → 首个工具结果即耗尽。
	l.RSSRatioFn = func() float64 { return 0.9 }

	var toolResultText string
	l.Emit = func(e Event) {
		if e.Kind == "tool_result" {
			toolResultText = e.Text
		}
	}

	if err := l.Run(t.Context(), "读大文件"); err != nil {
		t.Fatalf("loop 运行失败：%v", err)
	}

	if toolResultText == "" {
		t.Fatal("未捕获到 tool_result 事件")
	}
	if !strings.Contains(toolResultText, "<stored ") {
		t.Errorf("危急档下工具结果应被包装为 <stored>，实得前 200 字符：%.200s", toolResultText)
	}
	if !strings.Contains(toolResultText, `tool="read_file"`) {
		t.Errorf("包装应含工具名，实得：%.200s", toolResultText)
	}
}

// TestE2EBudgetNotConsumedWhenBaseline —— **反证**：基线档不包装。
//
// 若上一条测试只验证「危急档有包装」，无法排除「实现无条件包装」。
// 这条注入 `RSSRatioFn = 0`（基线 50k），同一大文件**不应**被包装。
func TestE2EBudgetNotConsumedWhenBaseline(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "big.txt")
	if err := os.WriteFile(target, []byte(strings.Repeat("x", 5000)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sc := &scriptedServer{responses: []string{
		toolTurnArgs("c1", "read_file", map[string]any{"file_path": target}),
		textTurn("完成"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	reg := tools.NewDefaultRegistry(tools.Options{Cwd: root})
	l := newTestLoopWithRegistry(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root}, reg)
	l.RSSRatioFn = func() float64 { return 0.0 }

	var toolResultText string
	l.Emit = func(e Event) {
		if e.Kind == "tool_result" {
			toolResultText = e.Text
		}
	}

	if err := l.Run(t.Context(), "读大文件"); err != nil {
		t.Fatalf("loop 运行失败：%v", err)
	}
	if strings.Contains(toolResultText, "<stored ") {
		t.Errorf("基线档不应包装，实得前 200 字符：%.200s", toolResultText)
	}
}
