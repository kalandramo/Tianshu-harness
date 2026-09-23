package agent

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestLiftMuteInRealRequests —— **lift 消费的端到端验收**。
//
// 用户动作：模型连续多轮收到一条**成熟 lift <= 0** 的提醒（"没提醒也会做"= 纯噪音）。
// 观察到：该 advisory 在真实请求体里**连续消失 10 轮**，第 11 轮回归（probation）。
//
// **接线依赖**：`bus.SetLiftProvider(readback.GetMatureLift)`。若未接，
// lift 恒不生效，提醒会一直渲染。
//
// **成熟度门**：GetMatureLift 需 decided>=5 且 shadowHeld>=3——用 SeedPriors 播种
// 成熟样本（模拟跨会话积累），否则返回 nil（中性、不静音）。
func TestLiftMuteInRealRequests(t *testing.T) {
	root := t.TempDir()
	// 12 轮，每轮读**不同**的（不存在的）文件。
	//
	// **为什么必须不同**：同一个文件反复失败会构成「同批次反复全错」——
	// wedge-loop guard（第四十二刀）会据此终止 run，本测试就跑不满 12 轮。
	// 用不同路径避开守卫，同时保持「每轮都有工具调用且都失败」的语义
	// （lift 静音不依赖具体行为，只依赖轮数）。
	responses := make([]string, 0, 24)
	for i := 0; i < 12; i++ {
		responses = append(responses, toolTurn("c"+string(rune('a'+i)), "read_file",
			`{"file_path":"missing-`+string(rune('a'+i))+`.ts"}`))
	}
	responses = append(responses, textTurn("done"))
	sc := &scriptedServer{responses: responses}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root})
	bus := NewAdvisoryBus()
	rb := NewAdvisoryReadback()
	p := NewPipeline(PipelineOptions{})

	// 播种成熟先验：lift = 0 - 1.0 = -1.0（明显负值，纯噪音）
	rb.SeedPriors(map[string]EfficacyPriorCounts{
		"noisy": {Delivered: 10, Adopted: 0, Ignored: 6, ShadowHeld: 4, ShadowSatisfied: 4},
	})

	// 每轮投递同一 key
	p.Register(RuntimeHook{
		Name: "nudge", Phase: PhasePostTurn,
		Run: func(ctx context.Context, h *RuntimeHookContext, tool *RuntimeToolEvent) error {
			bus.Submit(AdvisoryEntry{
				Key: "noisy", Priority: 0.6, Category: CategoryDiscipline, Content: "重复提醒",
			})
			return nil
		},
	})
	l.Hooks = p
	l.Advisories = bus
	l.Readback = rb
	bus.SetLiftProvider(rb.GetMatureLift) // ← 本刀的接线点

	for i := 0; i < 12; i++ {
		if err := l.Run(t.Context(), "go"); err != nil {
			t.Fatalf("第 %d 轮失败：%v", i+1, err)
		}
	}

	// ── 观察：请求体里该 advisory 的出现模式 ──
	var present []bool
	for _, b := range sc.handlerBodies() {
		present = append(present, strings.Contains(b, `key=\"noisy\"`))
	}
	t.Logf("present=%v", present)

	// 前置断言：成熟 lift 确实是负值（否则测试构造有误）
	lift := rb.GetMatureLift("noisy")
	if lift == nil {
		t.Fatal("GetMatureLift 返回 nil——先验未生效或成熟度门未过，测试构造有误")
	}
	if *lift > 0 {
		t.Fatalf("测试构造有误：lift 应为负值，得到 %v", *lift)
	}
	t.Logf("成熟 lift=%v", *lift)

	// ① 静音发生过（连续消失 >= 5 个请求）
	maxGap, gap := 0, 0
	for _, p := range present {
		if p {
			gap = 0
		} else {
			gap++
			if gap > maxGap {
				maxGap = gap
			}
		}
	}
	if maxGap < 5 {
		t.Errorf("**lift 静音未生效**——最长连续缺席 %d 个请求（应 >= 5）\n"+
			"present=%v（若全 true 说明 SetLiftProvider 未接或静音逻辑失效）", maxGap, present)
	}

	// ② probation 放行过（静音期后有回归）
	if maxGap >= len(present) {
		t.Errorf("**无 probation 放行**——静音后从未回归（永久静音）\npresent=%v", present)
	}
}

// TestMatureLiftGate —— **成熟度门：样本不足必须中性**。
//
// 这是 lift 静音的**安全阀**：会话内 holdout 积累极慢（单会话通常 0-2 个 shadow
// 样本），若不过成熟度门就下静音结论，冷启动阶段会误杀有效提醒。
//
// 对账 TS getMatureLift（advisory-readback.ts:341）：
//
//	decided < MATURE_LIFT_MIN_DECIDED(5) || shadowHeld < MATURE_LIFT_MIN_SHADOW(3) → null
//
// 用户动作：一条提醒的样本量逐步增长。
// 观察到：样本不足时返回 nil（中性），达标后才给出 lift 值。
func TestMatureLiftGate(t *testing.T) {
	cases := []struct {
		name    string
		priors  EfficacyPriorCounts
		wantNil bool
		want    float64
	}{
		{"无样本", EfficacyPriorCounts{}, true, 0},
		{"decided 不足（4 < 5）", EfficacyPriorCounts{Adopted: 2, Ignored: 2, ShadowHeld: 3, ShadowSatisfied: 0}, true, 0},
		{"shadow 不足（2 < 3）", EfficacyPriorCounts{Adopted: 3, Ignored: 3, ShadowHeld: 2, ShadowSatisfied: 0}, true, 0},
		{"刚好达标且正 lift", EfficacyPriorCounts{Adopted: 5, Ignored: 1, ShadowHeld: 3, ShadowSatisfied: 0}, false, 5.0 / 6.0},
		{"刚好达标且负 lift", EfficacyPriorCounts{Adopted: 0, Ignored: 6, ShadowHeld: 3, ShadowSatisfied: 3}, false, -1.0},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rb := NewAdvisoryReadback()
			rb.SeedPriors(map[string]EfficacyPriorCounts{"k": c.priors})
			got := rb.GetMatureLift("k")
			if c.wantNil {
				if got != nil {
					t.Errorf("样本不足应返回 nil（中性），得到 %v\n"+
						"（不设门会让冷启动阶段误杀有效提醒）", *got)
				}
				return
			}
			if got == nil {
				t.Fatalf("样本达标应返回值，得到 nil")
			}
			if diff := *got - c.want; diff > 1e-9 || diff < -1e-9 {
				t.Errorf("lift 应为 %v，得到 %v", c.want, *got)
			}
		})
	}
}

// TestLiftExemptNotMuted —— **三类豁免不参与 lift 静音**。
//
// 对账 TS 的豁免集（advisory-bus.ts:958）：constitutional / immediate / star_domain。
// 这些条目要么是宪法级约束，要么是即时守护，要么是星域情境提醒——
// **不该被统计意义上的「无效」判定静音掉**。
func TestLiftExemptNotMuted(t *testing.T) {
	cases := []struct {
		name string
		e    AdvisoryEntry
		want bool
	}{
		{"constitutional 豁免", AdvisoryEntry{Tier: TierConstitutional}, true},
		{"immediate 豁免", AdvisoryEntry{Immediate: true}, true},
		{"star_domain 豁免", AdvisoryEntry{Category: CategoryStarDomain}, true},
		{"普通 discipline 不豁免", AdvisoryEntry{Category: CategoryDiscipline}, false},
		{"普通 repair 不豁免", AdvisoryEntry{Category: CategoryRepair}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isLiftExempt(c.e); got != c.want {
				t.Errorf("isLiftExempt = %v，期望 %v", got, c.want)
			}
		})
	}
}
