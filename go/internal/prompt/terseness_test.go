package prompt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// oracle 结构 —— 对账 go/testdata/terseness/oracle.json（真实 TS 实现生成）。
type tersenessOracle struct {
	Meta struct {
		Source      string `json:"source"`
		GeneratedBy string `json:"generatedBy"`
	} `json:"meta"`
	Resolve []struct {
		Name     string  `json:"name"`
		EnvName  string  `json:"envName"`
		EnvValue *string `json:"envValue"`
		CtxName  string  `json:"ctxName"`
		Enabled  *bool   `json:"enabled"`
		Escalate *bool   `json:"escalate"`
		Result   struct {
			Enabled  bool `json:"enabled"`
			Escalate bool `json:"escalate"`
		} `json:"result"`
	} `json:"resolve"`
	Nudge []struct {
		Name     string `json:"name"`
		Escalate *bool  `json:"escalate"`
		Output   string `json:"output"`
	} `json:"nudge"`
}

func loadTersenessOracle(t *testing.T) *tersenessOracle {
	t.Helper()
	p := filepath.Join("..", "..", "testdata", "terseness", "oracle.json")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读 oracle 失败（%s）：%v\n先跑：npx tsx go/testdata/terseness/gen-oracle.ts > go/testdata/terseness/oracle.json", p, err)
	}
	var o tersenessOracle
	if err := json.Unmarshal(data, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	if o.Meta.Source != "src/prompt/volatile.ts" {
		t.Fatalf("oracle 源不符：got=%q（用错 oracle 文件？）", o.Meta.Source)
	}
	if o.Meta.GeneratedBy != "go/testdata/terseness/gen-oracle.ts" {
		t.Fatalf("oracle 生成器不符：got=%q", o.Meta.GeneratedBy)
	}
	if len(o.Resolve) == 0 || len(o.Nudge) == 0 {
		t.Fatalf("oracle 不完整：resolve=%d nudge=%d", len(o.Resolve), len(o.Nudge))
	}
	return &o
}

// TestResolveTersenessFlagsParity —— 逐值对账 TS `resolveTersenessFlags`。
//
// 判定是三态逻辑（对账 volatile.ts:227-238）：
//
//	const raw = env['RIVET_TERSE']
//	const v = raw?.trim().toLowerCase()
//	const optOut = v === '0' || v === 'false' || v === 'off' || v === 'no'
//	if (optOut) return { enabled: false, escalate: false }     // ★ 早返回
//	const optIn = v === '1' || v === 'true' || v === 'on' || v === 'yes'
//	              || ctx.tersenessEnabled === true
//	const escalate = Boolean(ctx.tersenessEscalate)
//	return { enabled: optIn || escalate, escalate }
//
// **★ optOut 的早返回是关键**：`RIVET_TERSE=0` 时**连 escalate 也被压制**
// （返回 `{false, false}` 而非 `{true, true}`）——即环境变量的显式关闭
// 优先于 ctx 的升级信号。oracle 用例 `zero__ctx-escalate` 实测
// `{enabled: false, escalate: false}` 钉住这一点。
func TestResolveTersenessFlagsParity(t *testing.T) {
	o := loadTersenessOracle(t)

	for _, c := range o.Resolve {
		t.Run(c.Name, func(t *testing.T) {
			// 构造 env：nil 表示键不存在
			env := map[string]string{}
			if c.EnvValue != nil {
				env["RIVET_TERSE"] = *c.EnvValue
			}

			// 构造 ctx
			var ctx TersenessContext
			if c.Enabled != nil {
				ctx.Enabled = c.Enabled
			}
			if c.Escalate != nil {
				ctx.Escalate = c.Escalate
			}

			got := ResolveTersenessFlags(ctx, env)

			if got.Enabled != c.Result.Enabled || got.Escalate != c.Result.Escalate {
				t.Errorf("env=%s ctx=%s：got={enabled:%v escalate:%v} want={enabled:%v escalate:%v}",
					c.EnvName, c.CtxName, got.Enabled, got.Escalate,
					c.Result.Enabled, c.Result.Escalate)
			}
		})
	}
}

// TestResolveTersenessFlagsSemantics —— 单独钉住关键语义（不依赖 oracle）。
func TestResolveTersenessFlagsSemantics(t *testing.T) {
	t.Run("★optOut压制escalate（早返回）", func(t *testing.T) {
		// RIVET_TERSE=0 时，即使 ctx.Escalate=true 也返回 {false, false}
		tr := true
		for _, v := range []string{"0", "false", "off", "no", "FALSE", "  off  "} {
			got := ResolveTersenessFlags(TersenessContext{Escalate: &tr}, map[string]string{"RIVET_TERSE": v})
			if got.Enabled || got.Escalate {
				t.Errorf("★ RIVET_TERSE=%q 应早返回 {false,false}（压制 escalate）：got={%v,%v}",
					v, got.Enabled, got.Escalate)
			}
		}
	})

	t.Run("optIn四组取值", func(t *testing.T) {
		for _, v := range []string{"1", "true", "on", "yes", "TRUE", "  On  "} {
			got := ResolveTersenessFlags(TersenessContext{}, map[string]string{"RIVET_TERSE": v})
			if !got.Enabled {
				t.Errorf("RIVET_TERSE=%q 应 optIn：got={%v,%v}", v, got.Enabled, got.Escalate)
			}
		}
	})

	t.Run("★未知值不算optIn", func(t *testing.T) {
		// 'maybe' / '2' / 'yep' 既非 optOut 也非 optIn → enabled 只看 ctx
		for _, v := range []string{"maybe", "2", "yep", ""} {
			got := ResolveTersenessFlags(TersenessContext{}, map[string]string{"RIVET_TERSE": v})
			if got.Enabled {
				t.Errorf("★ 未知值 %q 不该 optIn：got={%v,%v}", v, got.Enabled, got.Escalate)
			}
		}
	})

	t.Run("escalate单独立起enabled", func(t *testing.T) {
		// escalate=true 时 enabled 也为 true（enabled = optIn || escalate）
		esc := true
		got := ResolveTersenessFlags(TersenessContext{Escalate: &esc}, map[string]string{})
		if !got.Enabled || !got.Escalate {
			t.Errorf("escalate=true 应 {true,true}：got={%v,%v}", got.Enabled, got.Escalate)
		}
	})

	t.Run("env缺键不panic", func(t *testing.T) {
		got := ResolveTersenessFlags(TersenessContext{}, map[string]string{})
		if got.Enabled || got.Escalate {
			t.Errorf("空 env + 空 ctx 应 {false,false}：got={%v,%v}", got.Enabled, got.Escalate)
		}
	})
}

// TestRenderTersenessNudgeParity —— 逐字节对账 TS `renderTersenessNudge`。
func TestRenderTersenessNudgeParity(t *testing.T) {
	o := loadTersenessOracle(t)

	for _, c := range o.Nudge {
		t.Run(c.Name, func(t *testing.T) {
			// escalate 为 nil 表示调用方省略参数（TS 默认 false）
			escalate := false
			if c.Escalate != nil {
				escalate = *c.Escalate
			}
			got := RenderTersenessNudge(escalate)
			if got != c.Output {
				t.Errorf("输出不符：\n got(%d)=%q\nwant(%d)=%q",
					len(got), truncateForMsg(got), len(c.Output), truncateForMsg(c.Output))
			}
		})
	}
}

// TestRenderTersenessNudgeSemantics —— 钉住 escalate 分支的差异。
func TestRenderTersenessNudgeSemantics(t *testing.T) {
	plain := RenderTersenessNudge(false)
	strict := RenderTersenessNudge(true)

	if plain == strict {
		t.Fatal("★ escalate 两分支必须产出不同文本")
	}
	// escalate 版应更长（多一段 strict 文案）
	if len(strict) <= len(plain) {
		t.Errorf("escalate 版应更长：plain=%d strict=%d", len(plain), len(strict))
	}
	// 都应以 <output-style> 包裹
	for _, s := range []string{plain, strict} {
		if !strings.HasPrefix(s, "<output-style>") {
			t.Errorf("应以 <output-style> 开头：%q", truncateForMsg(s))
		}
		if !strings.HasSuffix(s, "</output-style>") {
			t.Errorf("应以 </output-style> 结尾：%q", tailOf(s, 30))
		}
	}
}
