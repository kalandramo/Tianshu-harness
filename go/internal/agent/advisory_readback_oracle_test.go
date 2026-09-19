package agent

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// readbackOracleEntry 是 oracle 里一项。
type readbackOracleEntry struct {
	Stats         map[string]AdvisoryKeyStats `json:"stats"`
	Outcomes      []readbackOutcome           `json:"outcomes"`
	AdoptedRate   map[string]*float64         `json:"adoptedRate"`
	Lift          map[string]*float64         `json:"lift"`
	Decided       map[string]int              `json:"decided"`
	IgnoredStreak map[string]int              `json:"ignoredStreak"`
}

type readbackOutcome struct {
	Key           string `json:"key"`
	Outcome       string `json:"outcome"`
	ExpectKind    string `json:"expectKind"`
	DeliveredTurn int    `json:"deliveredTurn"`
	EvaluatedTurn int    `json:"evaluatedTurn"`
	Shadow        bool   `json:"shadow"`
}

// readbackOp 是 cases 里一个操作。
type readbackOp struct {
	Op      string         `json:"op"`
	Key     string         `json:"key"`
	Expect  map[string]any `json:"expect"`
	Shadow  bool           `json:"shadow"`
	Turn    int            `json:"turn"`
	Name    string         `json:"name"`
	Target  string         `json:"target"`
	IsError bool           `json:"isError"`
}

func loadReadbackOracle(t *testing.T) map[string]readbackOracleEntry {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "readback", "oracle.json"))
	if err != nil {
		t.Fatalf("读 oracle 失败（先跑 node_modules/.bin/tsx go/testdata/readback/gen-oracle.ts）：%v", err)
	}
	var out map[string]readbackOracleEntry
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	if len(out) == 0 {
		t.Fatal("oracle 为空")
	}
	return out
}

func loadReadbackCases(t *testing.T) map[string][]readbackOp {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "readback", "cases.json"))
	if err != nil {
		t.Fatalf("读 cases 失败：%v", err)
	}
	var out map[string][]readbackOp
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析 cases 失败：%v", err)
	}
	return out
}

// toExpectation 把 JSON 形态转成 AdvisoryExpectation。
func toExpectation(m map[string]any) *AdvisoryExpectation {
	if m == nil {
		return nil
	}
	e := &AdvisoryExpectation{}
	if v, ok := m["kind"].(string); ok {
		e.Kind = ExpectKind(v)
	}
	if v, ok := m["targetIncludes"].(string); ok {
		e.TargetIncludes = v
	}
	if v, ok := m["path"].(string); ok {
		e.Path = v
	}
	if vs, ok := m["tools"].([]any); ok {
		for _, x := range vs {
			if s, ok := x.(string); ok {
				e.Tools = append(e.Tools, s)
			}
		}
	}
	if vs, ok := m["paths"].([]any); ok {
		for _, x := range vs {
			if s, ok := x.(string); ok {
				e.Paths = append(e.Paths, s)
			}
		}
	}
	if vs, ok := m["needles"].([]any); ok {
		for _, x := range vs {
			if s, ok := x.(string); ok {
				e.Needles = append(e.Needles, s)
			}
		}
	}
	if v, ok := m["withinTurns"].(float64); ok {
		e.WithinTurns = int(v)
	}
	return e
}

// runReadbackOps 执行操作脚本。
func runReadbackOps(ops []readbackOp) *AdvisoryReadback {
	r := NewAdvisoryReadback()
	for _, op := range ops {
		switch op.Op {
		case "track":
			r.Track([]DeliveredAdvisory{{
				Key:    op.Key,
				Expect: toExpectation(op.Expect),
				Shadow: op.Shadow,
			}}, op.Turn)
		case "tool":
			r.ObserveTool(ObservedToolEvent{
				Turn: op.Turn, Name: op.Name, Target: op.Target, IsError: op.IsError,
			})
		case "evaluate":
			r.Evaluate(op.Turn)
		}
	}
	return r
}

// TestAdvisoryReadbackOracleParity —— **readback 核心对账**（20 用例）。
func TestAdvisoryReadbackOracleParity(t *testing.T) {
	oracle := loadReadbackOracle(t)
	cases := loadReadbackCases(t)

	if len(cases) != len(oracle) {
		t.Fatalf("用例数不匹配：cases=%d oracle=%d", len(cases), len(oracle))
	}

	for name, entry := range oracle {
		t.Run(name, func(t *testing.T) {
			ops, ok := cases[name]
			if !ok {
				t.Fatalf("oracle 有 %q 但 cases 没有", name)
			}
			r := runReadbackOps(ops)

			// ── stats 对账 ──
			gotStats := r.Stats()
			if len(gotStats) != len(entry.Stats) {
				t.Errorf("stats key 数：Go=%d TS=%d", len(gotStats), len(entry.Stats))
			}
			for k, want := range entry.Stats {
				got, ok := gotStats[k]
				if !ok {
					t.Errorf("缺 stats[%q]", k)
					continue
				}
				if got.Delivered != want.Delivered {
					t.Errorf("[%s] delivered：Go=%d TS=%d", k, got.Delivered, want.Delivered)
				}
				if got.Adopted != want.Adopted {
					t.Errorf("[%s] adopted：Go=%d TS=%d", k, got.Adopted, want.Adopted)
				}
				if got.Ignored != want.Ignored {
					t.Errorf("[%s] ignored：Go=%d TS=%d", k, got.Ignored, want.Ignored)
				}
				if got.IgnoredStreak != want.IgnoredStreak {
					t.Errorf("[%s] ignoredStreak：Go=%d TS=%d", k, got.IgnoredStreak, want.IgnoredStreak)
				}
				if got.ShadowHeld != want.ShadowHeld {
					t.Errorf("[%s] shadowHeld：Go=%d TS=%d", k, got.ShadowHeld, want.ShadowHeld)
				}
				if got.ShadowSatisfied != want.ShadowSatisfied {
					t.Errorf("[%s] shadowSatisfied：Go=%d TS=%d", k, got.ShadowSatisfied, want.ShadowSatisfied)
				}
			}

			// ── outcomes 对账 ──
			gotOut := r.DrainOutcomes()
			if len(gotOut) != len(entry.Outcomes) {
				t.Fatalf("outcomes 数：Go=%d TS=%d\nGo: %+v", len(gotOut), len(entry.Outcomes), gotOut)
			}
			for i, want := range entry.Outcomes {
				g := gotOut[i]
				if g.Key != want.Key {
					t.Errorf("[%d] key：Go=%q TS=%q", i, g.Key, want.Key)
				}
				if string(g.Outcome) != want.Outcome {
					t.Errorf("[%d] outcome：Go=%q TS=%q", i, g.Outcome, want.Outcome)
				}
				if string(g.ExpectKind) != want.ExpectKind {
					t.Errorf("[%d] expectKind：Go=%q TS=%q", i, g.ExpectKind, want.ExpectKind)
				}
				if g.DeliveredTurn != want.DeliveredTurn {
					t.Errorf("[%d] deliveredTurn：Go=%d TS=%d", i, g.DeliveredTurn, want.DeliveredTurn)
				}
				if g.EvaluatedTurn != want.EvaluatedTurn {
					t.Errorf("[%d] evaluatedTurn：Go=%d TS=%d", i, g.EvaluatedTurn, want.EvaluatedTurn)
				}
				if g.Shadow != want.Shadow {
					t.Errorf("[%d] shadow：Go=%v TS=%v", i, g.Shadow, want.Shadow)
				}
			}

			// ── 查询方法对账 ──
			for k, want := range entry.AdoptedRate {
				got := r.GetAdoptionRate(k)
				if !floatPtrEqual(got, want) {
					t.Errorf("adoptionRate[%s]：Go=%v TS=%v", k, floatPtrStr(got), floatPtrStr(want))
				}
			}
			for k, want := range entry.Lift {
				got := r.GetLift(k)
				if !floatPtrEqual(got, want) {
					t.Errorf("lift[%s]：Go=%v TS=%v", k, floatPtrStr(got), floatPtrStr(want))
				}
			}
			for k, want := range entry.Decided {
				if got := r.GetDecidedCount(k); got != want {
					t.Errorf("decided[%s]：Go=%d TS=%d", k, got, want)
				}
			}
			for k, want := range entry.IgnoredStreak {
				if got := r.GetIgnoredStreak(k); got != want {
					t.Errorf("ignoredStreak[%s]：Go=%d TS=%d", k, got, want)
				}
			}
		})
	}
}

// floatPtrEqual 比较两个 *float64（含 nil 语义与浮点容差）。
func floatPtrEqual(a, b *float64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return math.Abs(*a-*b) < 1e-9
}

func floatPtrStr(p *float64) string {
	if p == nil {
		return "nil"
	}
	return strconv.FormatFloat(*p, 'f', -1, 64)
}

// TestAdvisoryReadbackFlushAtSessionEnd —— **会话结束不把未到期判 ignored**。
//
// 对账 flushAtSessionEnd 的语义：未到期的作为 unresolved 报出，**不记账**。
// TS 注释：worker 中位只跑 2 轮，几乎所有 pending 在会话结束时都未到期；
// 判 ignored 会把「没机会响应」记成「听了不做」。
func TestAdvisoryReadbackFlushAtSessionEnd(t *testing.T) {
	r := NewAdvisoryReadback()
	// verify_attempted 窗口 2 轮——turn 0 送达，turn 0 就 flush（远未到期）
	r.Track([]DeliveredAdvisory{{
		Key: "k1", Expect: &AdvisoryExpectation{Kind: ExpectVerifyAttempted},
	}}, 0)

	decided, unresolved := r.FlushAtSessionEnd(0)

	if decided != 0 {
		t.Errorf("未到期不该判定，decided=%d", decided)
	}
	if len(unresolved) != 1 {
		t.Fatalf("应报出 1 条 unresolved，得到 %d", len(unresolved))
	}
	if unresolved[0].Key != "k1" {
		t.Errorf("unresolved key：%q", unresolved[0].Key)
	}
	// **关键**：不记账
	s := r.Stats()["k1"]
	if s.Ignored != 0 {
		t.Errorf("**未到期不该计 ignored**，实际 %d（假 ignored 会压低效力评分）", s.Ignored)
	}
	if s.IgnoredStreak != 0 {
		t.Errorf("未到期不该推进 ignoredStreak，实际 %d", s.IgnoredStreak)
	}
}

// TestAdvisoryReadbackPatternAbsentOnlyAtDeadline —— **负向谓词只在到期时判**。
//
// 对账 evaluate 的 pattern_absent 分支：过早读文件会把「还没来得及清」
// 误判为忽略。
func TestAdvisoryReadbackPatternAbsentOnlyAtDeadline(t *testing.T) {
	r := NewAdvisoryReadback()
	// pattern_absent 窗口 4 轮
	r.Track([]DeliveredAdvisory{{
		Key: "probe",
		Expect: &AdvisoryExpectation{
			Kind: ExpectPatternAbsent, Path: "/nonexistent.ts", Needles: []string{"console.log"},
		},
	}}, 0)

	// turn 0/1/2 都不判（未到期）
	for turn := 0; turn <= 2; turn++ {
		if n := r.Evaluate(turn); n != 0 {
			t.Errorf("turn %d 未到期，不该判定，得到 %d", turn, n)
		}
	}
	// turn 3 = deadline（0 + 4 - 1）→ 判
	if n := r.Evaluate(3); n != 1 {
		t.Errorf("turn 3 应判定 1 条，得到 %d", n)
	}
}

// TestAdvisoryReadbackShadowIsolation —— **shadow 不污染主账本**。
func TestAdvisoryReadbackShadowIsolation(t *testing.T) {
	r := NewAdvisoryReadback()
	// shadow 送达 + 满足
	r.Track([]DeliveredAdvisory{{
		Key: "k1", Expect: &AdvisoryExpectation{Kind: ExpectVerifyAttempted}, Shadow: true,
	}}, 0)
	r.ObserveTool(ObservedToolEvent{Turn: 0, Name: "run_tests"})
	r.Evaluate(0)

	s := r.Stats()["k1"]
	if s.Delivered != 0 {
		t.Errorf("shadow 不该计 delivered，实际 %d", s.Delivered)
	}
	if s.ShadowHeld != 1 {
		t.Errorf("shadowHeld 应为 1，实际 %d", s.ShadowHeld)
	}
	if s.ShadowSatisfied != 1 {
		t.Errorf("shadowSatisfied 应为 1，实际 %d", s.ShadowSatisfied)
	}
	if s.Adopted != 0 {
		t.Errorf("shadow 不该计 adopted，实际 %d", s.Adopted)
	}
	if s.IgnoredStreak != 0 {
		t.Errorf("shadow 不该推进 streak，实际 %d", s.IgnoredStreak)
	}
}

// TestAdvisoryReadbackEventTrimByTurn —— **事件按轮修剪**（不按条数）。
func TestAdvisoryReadbackEventTrimByTurn(t *testing.T) {
	r := NewAdvisoryReadback()
	// turn 0 的事件
	r.ObserveTool(ObservedToolEvent{Turn: 0, Name: "read_file", Target: "a.ts"})
	// turn 10 触发修剪（cutoff = 10 - 8 = 2，turn 0 被裁掉）
	r.ObserveTool(ObservedToolEvent{Turn: 10, Name: "read_file", Target: "b.ts"})

	// 送达 file_touched(a.ts)，窗口 1 轮——a.ts 的事件已被裁掉 → 不满足
	r.Track([]DeliveredAdvisory{{
		Key: "k1", Expect: &AdvisoryExpectation{Kind: ExpectFileTouched, Paths: []string{"a.ts"}},
	}}, 10)
	r.Evaluate(10)

	s := r.Stats()["k1"]
	if s.Adopted != 0 {
		t.Errorf("turn 0 的事件应已被裁掉，a.ts 不该满足，实际 adopted=%d", s.Adopted)
	}
	if s.Ignored != 1 {
		t.Errorf("应判 ignored，实际 %d", s.Ignored)
	}
}

// TestAdvisoryReadbackLiftFormula —— lift 公式与 nil 条件。
func TestAdvisoryReadbackLiftFormula(t *testing.T) {
	r := NewAdvisoryReadback()

	// 无 stats → nil
	if r.GetLift("unknown") != nil {
		t.Error("无 stats 应返回 nil")
	}

	// 有 delivered 但无 shadowHeld → nil
	r.Track([]DeliveredAdvisory{{Key: "k1", Expect: &AdvisoryExpectation{Kind: ExpectVerifyAttempted}}}, 0)
	r.ObserveTool(ObservedToolEvent{Turn: 0, Name: "run_tests"})
	r.Evaluate(0)
	if r.GetLift("k1") != nil {
		t.Error("shadowHeld=0 应返回 nil")
	}

	// 加 shadow 组：adopted=1/decided=1 - shadowSatisfied=0/shadowHeld=1 = 1.0
	r.Track([]DeliveredAdvisory{{
		Key: "k1", Expect: &AdvisoryExpectation{Kind: ExpectVerifyAttempted}, Shadow: true,
	}}, 1)
	r.Evaluate(1) // shadow 未满足 → 不记 shadowSatisfied
	got := r.GetLift("k1")
	if got == nil {
		t.Fatal("应有 lift 值")
	}
	if math.Abs(*got-1.0) > 1e-9 {
		t.Errorf("lift 应为 1.0（1/1 - 0/1），得到 %v", *got)
	}
}
