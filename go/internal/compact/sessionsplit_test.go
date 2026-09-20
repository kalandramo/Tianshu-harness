package compact

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/session"
)

// splitOracle 是 TS 侧真实 trySessionSplit 判定层的产出。
// 生成命令：node_modules/.bin/tsx go/testdata/sessionsplit/gen-oracle.ts
type splitOracle struct {
	Threshold struct {
		MinWindow int     `json:"minWindow"`
		MinRatio  float64 `json:"minRatio"`
	} `json:"threshold"`
	Split map[string]struct {
		DidSplit bool `json:"didSplit"`
		// EstimatedTokensBefore 是**判定前**的估算值（nil = 判定未走到 ratio 计算，
		// 即窗口门槛提前返回）。
		//
		// **为何用 before**：`trySessionSplit` 成功后会把历史替换成 handoff，
		// 之后再读 session 得到的是压缩后的状态（首版 oracle 记到 3098 而非
		// 判定时的 499000）。
		EstimatedTokensBefore *int     `json:"estimatedTokensBefore"`
		RatioBefore           *float64 `json:"ratioBefore"`
	} `json:"split"`
}

func loadSplitOracle(t *testing.T) splitOracle {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "sessionsplit", "oracle.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败（%s）：%v\n生成命令：node_modules/.bin/tsx go/testdata/sessionsplit/gen-oracle.ts",
			path, err)
	}
	var o splitOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return o
}

// splitCaseInput 返回该用例的 (messages, contextWindow)。
//
// 构造与 gen-oracle.ts 的 `sessionWithTokens` 一致：n 条等长消息，
// 每条 perMsg 字符，使总数约为 sessionTokens（4 字符 ≈ 1 token）。
func splitCaseInput(name string, sessionTokens int) ([]session.OaiMessage, int) {
	n := sessionTokens / 1000
	if n < 1 {
		n = 1
	}
	perMsg := (sessionTokens * 4) / n
	s := strings.Repeat("x", perMsg)
	msgs := make([]session.OaiMessage, n)
	for i := range msgs {
		c := s
		msgs[i] = session.OaiMessage{Role: "user", Content: &c}
	}
	return msgs, 0
}

func splitCaseWindow(name string) (int, bool) {
	switch name {
	case "small_window_never_splits":
		return 128_000, true
	case "boundary_window_499999":
		return 499_999, true
	case "boundary_window_500000":
		return 500_000, true
	case "window_1m_under_threshold":
		return 1_000_000, true
	case "window_1m_mid_ratio_0_70", "window_1m_mid_ratio_0_85", "window_1m_over_threshold":
		return 1_000_000, true
	case "large_window_small_session":
		return 2_000_000, true
	}
	return 0, false
}

func splitCaseSessionTokens(name string) int {
	switch name {
	case "small_window_never_splits":
		return 200_000
	case "boundary_window_499999":
		return 499_000
	case "boundary_window_500000":
		return 499_000
	case "window_1m_under_threshold":
		return 500_000
	case "window_1m_mid_ratio_0_70":
		return 700_000
	case "window_1m_mid_ratio_0_85":
		return 850_000
	case "window_1m_over_threshold":
		return 950_000
	case "large_window_small_session":
		return 100_000
	}
	return 0
}

// TestSessionSplitThresholdsParity —— 阈值常量与 TS 一致。
func TestSessionSplitThresholdsParity(t *testing.T) {
	o := loadSplitOracle(t)
	if SessionSplitMinWindow != o.Threshold.MinWindow {
		t.Errorf("minWindow: Go=%d TS=%d", SessionSplitMinWindow, o.Threshold.MinWindow)
	}
	if SessionSplitMinRatio != o.Threshold.MinRatio {
		t.Errorf("minRatio: Go=%v TS=%v", SessionSplitMinRatio, o.Threshold.MinRatio)
	}
}

// TestShouldSessionSplitParity —— split 判定与 TS 逐用例等价。
//
// 覆盖三条分支：窗口太小 / 比例不足 / 触发。
// **关键边界**：`small_window_never_splits` 的 ratio 高达 1.562 但窗口 128K
// ——证明窗口门槛**优先于**比例（不是「比例够就 split」）。
func TestShouldSessionSplitParity(t *testing.T) {
	o := loadSplitOracle(t)
	if len(o.Split) == 0 {
		t.Fatal("oracle 无 split 用例")
	}
	for name, want := range o.Split {
		window, ok := splitCaseWindow(name)
		if !ok {
			t.Errorf("oracle 有用例 %q 而 Go 侧无对应窗口", name)
			continue
		}
		tokens := splitCaseSessionTokens(name)
		msgs, _ := splitCaseInput(name, tokens)

		t.Run(name, func(t *testing.T) {
			got := ShouldSessionSplit(msgs, window)
			if got.ShouldSplit != want.DidSplit {
				t.Errorf("shouldSplit: Go=%v TS=%v (reason=%q ratio=%.3f)",
					got.ShouldSplit, want.DidSplit, got.Reason, got.Ratio)
			}
			// token 估算：**仅在 TS 判定走到 ratio 计算时才比对**——
			// 窗口门槛提前返回时 TS 根本没调 getEstimatedTokens（nil）。
			if want.EstimatedTokensBefore != nil {
				if got.EstimatedTokens != *want.EstimatedTokensBefore {
					t.Errorf("estimatedTokens: Go=%d TS=%d", got.EstimatedTokens, *want.EstimatedTokensBefore)
				}
			} else if got.EstimatedTokens != 0 {
				t.Errorf("窗口门槛提前返回时不应算 token，实得 %d", got.EstimatedTokens)
			}
		})
	}
}

// TestSessionSplitReasonBranches —— 三条分支的 reason 正确。
func TestSessionSplitReasonBranches(t *testing.T) {
	big := strings.Repeat("x", 4_000_000) // 约 1M token
	msgs := []session.OaiMessage{{Role: "user", Content: &big}}

	// 窗口太小（即使 ratio 远超阈值）。
	if d := ShouldSessionSplit(msgs, 128_000); d.Reason != SplitWindowTooSmall {
		t.Errorf("小窗口 reason = %q, want %q", d.Reason, SplitWindowTooSmall)
	}
	// 窗口够大但比例不足。
	small := strings.Repeat("x", 400_000) // 约 100K token
	smallMsgs := []session.OaiMessage{{Role: "user", Content: &small}}
	if d := ShouldSessionSplit(smallMsgs, 1_000_000); d.Reason != SplitBelowRatio {
		t.Errorf("比例不足 reason = %q, want %q", d.Reason, SplitBelowRatio)
	}
	// 两门槛都满足。
	if d := ShouldSessionSplit(msgs, 1_000_000); d.Reason != SplitTriggered {
		t.Errorf("触发 reason = %q, want %q", d.Reason, SplitTriggered)
	}
}

// TestSessionSplitWindowBoundary —— 500K 是硬边界（499_999 不触发）。
func TestSessionSplitWindowBoundary(t *testing.T) {
	big := strings.Repeat("x", 4_000_000)
	msgs := []session.OaiMessage{{Role: "user", Content: &big}}

	if d := ShouldSessionSplit(msgs, 499_999); d.ShouldSplit {
		t.Errorf("窗口 499_999 不应 split（reason=%q）", d.Reason)
	}
	if d := ShouldSessionSplit(msgs, 500_000); !d.ShouldSplit {
		t.Errorf("窗口 500_000 应 split（reason=%q ratio=%.3f）", d.Reason, d.Ratio)
	}
}

// TestSessionSplitRatioBoundary —— 0.86 是比例边界。
func TestSessionSplitRatioBoundary(t *testing.T) {
	window := 1_000_000
	// 构造恰好 86% 与略低于 86% 的会话。
	// 每 token 4 字符 → 860_000 token = 3_440_000 字符。
	at := strings.Repeat("x", 3_440_000)
	below := strings.Repeat("x", 3_400_000) // 约 85%

	atMsgs := []session.OaiMessage{{Role: "user", Content: &at}}
	belowMsgs := []session.OaiMessage{{Role: "user", Content: &below}}

	if d := ShouldSessionSplit(belowMsgs, window); d.ShouldSplit {
		t.Errorf("低于 86%% 不应 split（ratio=%.4f）", d.Ratio)
	}
	if d := ShouldSessionSplit(atMsgs, window); !d.ShouldSplit {
		t.Errorf("达到 86%% 应 split（ratio=%.4f）", d.Ratio)
	}
}

// TestBuildSessionHandoffBasic —— 最小 handoff 的形态。
//
// **这是降级实现**（缺 task-state/trajectory 章节），故只断言本实现
// 承诺的形态，不假装与 TS 完整 handoff 等价。
func TestBuildSessionHandoffBasic(t *testing.T) {
	a1 := "分析问题"
	a2 := "得出结论"
	tool := "读 /src/foo.ts 与 /src/bar.go"
	msgs := []session.OaiMessage{
		{Role: "user", Content: strPtrLocal("问题")},
		{Role: "assistant", Content: &a1},
		{Role: "tool", Content: &tool},
		{Role: "assistant", Content: &a2},
	}

	got := BuildSessionHandoff(msgs, 0.87)

	if !strings.HasPrefix(got, "<session-handoff>") || !strings.HasSuffix(got, "</session-handoff>") {
		t.Errorf("应被 <session-handoff> 包裹，实得 %q", got)
	}
	if !strings.Contains(got, "Session split at 87% context") {
		t.Errorf("应含 split 比例（87%%），实得 %q", got)
	}
	if !strings.Contains(got, "分析问题") || !strings.Contains(got, "得出结论") {
		t.Errorf("应含近期推理，实得 %q", got)
	}
	if !strings.Contains(got, "/src/foo.ts") || !strings.Contains(got, "/src/bar.go") {
		t.Errorf("应含文件清单，实得 %q", got)
	}
	// 推理顺序：正序（首条在前）。
	if strings.Index(got, "分析问题") > strings.Index(got, "得出结论") {
		t.Error("近期推理应保持时间正序")
	}
}

// TestBuildSessionHandoffEmpty —— 空历史的兜底文案。
func TestBuildSessionHandoffEmpty(t *testing.T) {
	got := BuildSessionHandoff(nil, 0.9)
	if !strings.Contains(got, "（无记录）") {
		t.Errorf("空历史应含兜底文案，实得 %q", got)
	}
}

// TestFormatPercentRounding —— 百分比四舍五入（对账 toFixed(0)）。
func TestFormatPercentRounding(t *testing.T) {
	cases := []struct {
		ratio float64
		want  string
	}{
		{0.86, "86%"},
		{0.864, "86%"},
		{0.865, "87%"},
		{0.999, "100%"},
		{0, "0%"},
	}
	for _, c := range cases {
		if got := formatPercent(c.ratio); got != c.want {
			t.Errorf("formatPercent(%v) = %q, want %q", c.ratio, got, c.want)
		}
	}
}

// TestExtractFilePathsDedup —— 路径提取去重保序。
func TestExtractFilePathsDedup(t *testing.T) {
	t1 := "读 /src/a.ts"
	t2 := "又读 /src/a.ts 和 /src/b.go"
	msgs := []session.OaiMessage{
		{Role: "tool", Content: &t1},
		{Role: "tool", Content: &t2},
	}
	got := extractFilePaths(msgs)
	if len(got) != 2 {
		t.Fatalf("应提取 2 个去重路径，实得 %v", got)
	}
	if got[0] != "/src/a.ts" || got[1] != "/src/b.go" {
		t.Errorf("应保插入序，实得 %v", got)
	}
}

func strPtrLocal(s string) *string { return &s }
