package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// lossyOracle 是 TS 侧 lossy-markers 的语义快照。
// 生成命令：npx tsx go/testdata/lossymarkers/gen-oracle.ts
type lossyOracle struct {
	TSMarkers []string `json:"tsMarkers"`
	Positive  []struct {
		Label   string `json:"label"`
		Content string `json:"content"`
		IsLossy bool   `json:"isLossy"`
	} `json:"positive"`
	Negative []struct {
		Label   string `json:"label"`
		Content string `json:"content"`
		IsLossy bool   `json:"isLossy"`
	} `json:"negative"`
}

func loadLossyOracle(t *testing.T) lossyOracle {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "lossymarkers", "oracle.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败（%s）：%v\n生成：npx tsx go/testdata/lossymarkers/gen-oracle.ts", path, err)
	}
	var o lossyOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return o
}

// goProducedMarkers 是 **Go 侧真实产生**的标记所对应的 oracle 正例标签。
//
// **为什么需要这张表**：TS 的 15 条标记里有 8 条对应的子系统在 Go 侧未移植
// （storm 折叠 / 分层摘要 / per-message-budget / 过期轮次压缩）——Go **不产生**
// 那些标记。移植它们会让 hook 永不触发（死模式）。
//
// 本测试只断言 Go 侧**确实产生**的标记被判为 lossy。将来移植相应子系统时，
// 把标签加进本表（并同步 lossy_markers.go 的模式）即可。
var goProducedMarkers = map[string]bool{
	"collapsed-header":        true,
	"collapsed-readfile":      true,
	"output-truncated-footer": true,
	"stdout-truncated":        true,
	"stderr-truncated":        true,
	"partial-view":            true,
	"microcompacted":          true,
}

// TestLossyMarkersParity —— Go 侧**真实产生**的标记必须全部被识别。
func TestLossyMarkersParity(t *testing.T) {
	o := loadLossyOracle(t)
	checked := 0
	for _, p := range o.Positive {
		if !goProducedMarkers[p.Label] {
			continue // Go 侧不产生该标记（子系统未移植）
		}
		checked++
		t.Run(p.Label, func(t *testing.T) {
			if !p.IsLossy {
				t.Fatalf("前置：TS 应判 lossy（oracle 不一致）")
			}
			if !IsLossyObservation(p.Content) {
				t.Errorf("Go 未识别该标记：%q", p.Content)
			}
		})
	}
	if checked != len(goProducedMarkers) {
		t.Errorf("对账了 %d 条，但 goProducedMarkers 声明 %d 条——表与 oracle 漂移",
			checked, len(goProducedMarkers))
	}
}

// TestLossyMarkersNegativeParity —— **反假阳性**：负例不得被识别。
//
// 这是 TS 文件头强调的规则：
//
//	Anti-false-positive rule: only structural markers match — natural-language
//	words like "truncated" in ordinary command output must NOT trigger.
func TestLossyMarkersNegativeParity(t *testing.T) {
	o := loadLossyOracle(t)
	for _, n := range o.Negative {
		t.Run(n.Label, func(t *testing.T) {
			if n.IsLossy {
				t.Fatalf("前置：TS 不应判 lossy（oracle 不一致）")
			}
			if IsLossyObservation(n.Content) {
				t.Errorf("**假阳性**：Go 误判为 lossy：%q", n.Content)
			}
		})
	}
}

// Go 侧的模式必须是 TS 模式的**子集语义**——不得出现 TS 没有的宽模式。
//
// **这是「臆造标记」的防线**：首版我曾加了一条 `lines omitted \(`，而
// TS 的三条对应模式都要求具体前缀（turn read budget 等）——裸格式在 TS 判
// false。本测试用负例 `lines-omitted-bare` 锁定该防线。
func TestLossyNoFabricatedMarkers(t *testing.T) {
	// 裸「lines omitted (...)」不带预算前缀——TS 与 Go 都不认。
	bare := "... (1234 lines omitted) ..."
	if IsLossyObservation(bare) {
		t.Error("裸 lines omitted 不应被识别（TS 侧也判 false）")
	}
}

// Go 标记表不应为空（防「全部删掉」的假绿）。
func TestLossyMarkersNonEmpty(t *testing.T) {
	if len(lossyContentMarkers) == 0 {
		t.Fatal("标记表为空——hook 永不触发")
	}
	t.Logf("Go 侧标记 %d 条（TS 侧 %d 条）", len(lossyContentMarkers), len(loadLossyOracle(t).TSMarkers))
}

// ── hook 行为 ──

// 有损输出 → 提交 advisory。
func TestLossyHookFiresOnLossyContent(t *testing.T) {
	sink := &spySink{}
	h := NewLossyObservationHook(sink)

	h.Run(context.Background(), &RuntimeHookContext{
		Snapshot: &RuntimeHookSnapshot{Turn: 1},
	}, &RuntimeToolEvent{
		Name:          "grep",
		ResultContent: "[collapsed grep: 14 matches in src/]",
	})

	if len(sink.entries) != 1 {
		t.Fatalf("应提交 1 条，得到 %d", len(sink.entries))
	}
	e := sink.entries[0]
	if e.Key != "lossy-observation" {
		t.Errorf("key = %q", e.Key)
	}
	if e.Priority != 0.48 {
		t.Errorf("priority = %v, want 0.48", e.Priority)
	}
	if e.TTL != 1 {
		t.Errorf("ttl = %d, want 1", e.TTL)
	}
	if e.Category != CategoryDiscipline {
		t.Errorf("category = %q", e.Category)
	}
	if e.Expect != nil {
		t.Error("expect 应刻意留空（W3-C2 过宽伪 expect）")
	}
}

// **反证**：正常输出不触发。
func TestLossyHookSilentOnNormalContent(t *testing.T) {
	sink := &spySink{}
	h := NewLossyObservationHook(sink)
	h.Run(context.Background(), &RuntimeHookContext{
		Snapshot: &RuntimeHookSnapshot{Turn: 1},
	}, &RuntimeToolEvent{Name: "grep", ResultContent: "src/a.ts:12: export const x = 1"})
	if len(sink.entries) != 0 {
		t.Errorf("正常输出不应触发，得到 %d 条", len(sink.entries))
	}
}

// **反证**：每轮至多 1 条（同轮多次 lossy 只投一次）。
func TestLossyHookAtMostOncePerTurn(t *testing.T) {
	sink := &spySink{}
	h := NewLossyObservationHook(sink)
	for i := 0; i < 3; i++ {
		h.Run(context.Background(), &RuntimeHookContext{
			Snapshot: &RuntimeHookSnapshot{Turn: 5},
		}, &RuntimeToolEvent{Name: "grep", ResultContent: "[collapsed grep: x]"})
	}
	if len(sink.entries) != 1 {
		t.Errorf("同轮应至多 1 条，得到 %d", len(sink.entries))
	}
}

// 不同轮可各触发一次。
func TestLossyHookFiresPerTurn(t *testing.T) {
	sink := &spySink{}
	h := NewLossyObservationHook(sink)
	for turn := 1; turn <= 3; turn++ {
		h.Run(context.Background(), &RuntimeHookContext{
			Snapshot: &RuntimeHookSnapshot{Turn: turn},
		}, &RuntimeToolEvent{Name: "grep", ResultContent: "[collapsed grep: x]"})
	}
	if len(sink.entries) != 3 {
		t.Errorf("三轮应各触发一次，得到 %d", len(sink.entries))
	}
}

// **反证**：空 ResultContent 不触发。
//
// **注意**：这条断言在「去掉空检查」的变异下**仍成立**（空串不匹配任何标记
// 模式 → `IsLossyObservation("")` 恒 false）——那是**等价变异**。
// 真正的不变量由下一条测试锁定。
func TestLossyHookSilentOnEmptyContent(t *testing.T) {
	sink := &spySink{}
	h := NewLossyObservationHook(sink)
	h.Run(context.Background(), &RuntimeHookContext{
		Snapshot: &RuntimeHookSnapshot{Turn: 1},
	}, &RuntimeToolEvent{Name: "grep"})
	if len(sink.entries) != 0 {
		t.Errorf("空内容不应触发，得到 %d 条", len(sink.entries))
	}
}

// **不变量**：不触发的调用**不得推进冷却**。
//
// **为什么这条重要**：若实现在内容检查**之前**就写 `lastFiredTurn`，则
// 「同轮先来个非 lossy 输出、再来个 lossy 输出」会漏掉后者——用户看不到
// 本该出现的提醒。这是顺序缺陷，测试必须锁定检查与赋值的先后。
func TestLossyHookNonLossyDoesNotAdvanceCooldown(t *testing.T) {
	sink := &spySink{}
	h := NewLossyObservationHook(sink)

	// 同轮：先正常输出（不触发），再 lossy 输出（应触发）
	h.Run(context.Background(), &RuntimeHookContext{
		Snapshot: &RuntimeHookSnapshot{Turn: 7},
	}, &RuntimeToolEvent{Name: "grep", ResultContent: "src/a.ts:12: ok"})

	if len(sink.entries) != 0 {
		t.Fatalf("正常输出不应触发")
	}
	// 同轮再来 lossy —— 必须能触发（冷却未被误推进）
	h.Run(context.Background(), &RuntimeHookContext{
		Snapshot: &RuntimeHookSnapshot{Turn: 7},
	}, &RuntimeToolEvent{Name: "grep", ResultContent: "[collapsed grep: x]"})

	if len(sink.entries) != 1 {
		t.Errorf("**冷却被误推进**——非 lossy 调用不应占用本轮配额，得到 %d 条", len(sink.entries))
	}
}

// 同轮：空内容在前、lossy 在后——同样不得被误推进。
func TestLossyHookEmptyDoesNotAdvanceCooldown(t *testing.T) {
	sink := &spySink{}
	h := NewLossyObservationHook(sink)
	h.Run(context.Background(), &RuntimeHookContext{
		Snapshot: &RuntimeHookSnapshot{Turn: 3},
	}, &RuntimeToolEvent{Name: "grep"}) // 空内容
	h.Run(context.Background(), &RuntimeHookContext{
		Snapshot: &RuntimeHookSnapshot{Turn: 3},
	}, &RuntimeToolEvent{Name: "grep", ResultContent: "[output truncated: x]"})
	if len(sink.entries) != 1 {
		t.Errorf("空内容不应占用本轮配额，得到 %d 条", len(sink.entries))
	}
}

// **反证**：nil 输入不 panic。
func TestLossyHookNilSafe(t *testing.T) {
	h := NewLossyObservationHook(&spySink{})
	if err := h.Run(context.Background(), nil, nil); err != nil {
		t.Errorf("nil 输入不应报错：%v", err)
	}
	h2 := NewLossyObservationHook(nil)
	if err := h2.Run(context.Background(), &RuntimeHookContext{
		Snapshot: &RuntimeHookSnapshot{Turn: 1},
	}, &RuntimeToolEvent{ResultContent: "[collapsed x]"}); err != nil {
		t.Errorf("nil bus 不应报错：%v", err)
	}
}

// 元数据对账。
func TestLossyHookMetadata(t *testing.T) {
	h := NewLossyObservationHook(&spySink{})
	if h.Name != "lossy-observation" {
		t.Errorf("name = %q", h.Name)
	}
	if h.Phase != PhasePostTool {
		t.Errorf("phase = %q, want postTool", h.Phase)
	}
}
