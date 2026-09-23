package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// skillsOracle 是 TS 侧真实模块导出的 skill 语义快照。
// 生成命令：npx tsx go/testdata/skills/gen-oracle.ts
type skillsOracle struct {
	Builtin []struct {
		Name         string   `json:"name"`
		Description  string   `json:"description"`
		Triggers     []string `json:"triggers"`
		TriggerFlags []string `json:"triggerFlags"`
		Body         string   `json:"body"`
		BuiltIn      bool     `json:"builtIn"`
	} `json:"builtin"`
	Retired []struct {
		Name   string `json:"name"`
		SHA256 string `json:"sha256"`
	} `json:"retired"`
	Parsed map[string]struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Triggers    []string `json:"triggers"`
		Body        string   `json:"body"`
		TierLock    *string  `json:"tierLock"`
	} `json:"parsed"`
	ParseErrors map[string]string `json:"parseErrors"`
	Discovery   struct {
		NoHint          *string `json:"noHint"`
		WithHintMatch   *string `json:"withHintMatch"`
		WithHintNoMatch *string `json:"withHintNoMatch"`
		EmptyRegistry   *string `json:"emptyRegistry"`
		Exclude         *string `json:"exclude"`
		TinyBudget      *string `json:"tinyBudget"`
		ShortDesc       *string `json:"shortDesc"`
		WhitespaceDesc  *string `json:"whitespaceDesc"`
	} `json:"discovery"`
	SkillFiles []struct {
		Path string `json:"path"`
		Kind string `json:"kind"`
	} `json:"skillFiles"`
}

func loadSkillsOracle(t *testing.T) skillsOracle {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "skills", "oracle.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败（%s）：%v\n生成：npx tsx go/testdata/skills/gen-oracle.ts", path, err)
	}
	var o skillsOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return o
}

// deref 取指针值，nil 返回 ""（用于对比 nullable 字段）。
func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// ── 内置技能逐字对账 ──

func TestBuiltinSkillsParity(t *testing.T) {
	o := loadSkillsOracle(t)
	if len(BuiltinSkills) != len(o.Builtin) {
		t.Fatalf("内置技能数：Go=%d TS=%d", len(BuiltinSkills), len(o.Builtin))
	}
	for i, want := range o.Builtin {
		got := BuiltinSkills[i]
		t.Run(want.Name, func(t *testing.T) {
			if got.Name != want.Name {
				t.Errorf("name：Go=%q TS=%q", got.Name, want.Name)
			}
			if got.Description != want.Description {
				t.Errorf("description 不符：\nGo=%q\nTS=%q", got.Description, want.Description)
			}
			if got.Body != want.Body {
				t.Errorf("body 不符（长度 Go=%d TS=%d）", len(got.Body), len(want.Body))
			}
			if got.BuiltIn != want.BuiltIn {
				t.Errorf("builtIn：Go=%v TS=%v", got.BuiltIn, want.BuiltIn)
			}
			if len(got.Triggers) != len(want.Triggers) {
				t.Fatalf("trigger 数：Go=%d TS=%d", len(got.Triggers), len(want.Triggers))
			}
			for j, src := range want.Triggers {
				flags := want.TriggerFlags[j]
				prefix := ""
				if contains(flags, "i") {
					prefix = "(?i)"
				}
				if got.Triggers[j].String() != prefix+src {
					t.Errorf("trigger[%d]：Go=%q TS=%q", j, got.Triggers[j].String(), prefix+src)
				}
			}
		})
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// ── 退役表对账 ──

func TestRetiredSkillsParity(t *testing.T) {
	o := loadSkillsOracle(t)
	if len(RetiredBundledSkills) != len(o.Retired) {
		t.Fatalf("退役表长度：Go=%d TS=%d", len(RetiredBundledSkills), len(o.Retired))
	}
	for i, want := range o.Retired {
		got := RetiredBundledSkills[i]
		if got.Name != want.Name || got.SHA256 != want.SHA256 {
			t.Errorf("[%d]：Go={%q,%q} TS={%q,%q}", i, got.Name, got.SHA256, want.Name, want.SHA256)
		}
	}
}

// ── parseSkillMarkdown 差分 ──

func TestParseSkillMarkdownParity(t *testing.T) {
	o := loadSkillsOracle(t)
	cases := parseOracleCases()
	checked := 0
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			// 先确认 TS 侧确实成功解析了（否则不该出现在 parsed 里）。
			want, ok := o.Parsed[c.label]
			if !ok {
				if msg, isErr := o.ParseErrors[c.label]; isErr {
					// TS 报错 → Go 也必须报错。
					if _, err := ParseSkillMarkdown(c.content, c.fileName); err == nil {
						t.Fatalf("TS 报错 %q，Go 却成功", msg)
					}
					return
				}
				t.Fatalf("oracle 里既无 parsed 也无 parseErrors：%s", c.label)
			}
			checked++
			got, err := ParseSkillMarkdown(c.content, c.fileName)
			if err != nil {
				t.Fatalf("Go 解析失败：%v", err)
			}
			if got.Name != want.Name {
				t.Errorf("name：Go=%q TS=%q", got.Name, want.Name)
			}
			if got.Description != want.Description {
				t.Errorf("description：Go=%q TS=%q", got.Description, want.Description)
			}
			if got.Body != want.Body {
				t.Errorf("body：Go=%q TS=%q", got.Body, want.Body)
			}
			if got.TierLock != deref(want.TierLock) {
				t.Errorf("tierLock：Go=%q TS=%q", got.TierLock, deref(want.TierLock))
			}
			if len(got.Triggers) != len(want.Triggers) {
				t.Fatalf("trigger 数：Go=%d TS=%d", len(got.Triggers), len(want.Triggers))
			}
			for j, src := range want.Triggers {
				if got.Triggers[j].String() != "(?i)"+src {
					t.Errorf("trigger[%d]：Go=%q TS=%q", j, got.Triggers[j].String(), "(?i)"+src)
				}
			}
		})
	}
	if checked == 0 {
		t.Fatal("无成功用例")
	}
	t.Logf("对账 %d 个解析用例", checked)
}

// parseOracleCases 必须与 gen-oracle.ts 的 parseCases 逐条对应。
func parseOracleCases() []struct{ label, content, fileName string } {
	return []struct{ label, content, fileName string }{
		{"basic", "---\nname: foo\ndescription: A foo skill\ntriggers: [\"bar\", \"baz\"]\n---\nDo the thing.\n", "foo.md"},
		{"no-name-falls-back-to-file", "---\ndescription: no name here\n---\nBody text.\n", "fallback.md"},
		{"single-trigger-string", "---\nname: single\ntrigger: \"only-one\"\n---\nBody.\n", "single.md"},
		{"block-scalar-pipe", "---\nname: pipe\ndescription: |\n  line one\n  line two\n---\nBody.\n", "pipe.md"},
		{"block-scalar-folded", "---\nname: folded\ndescription: >\n  folded one\n  folded two\n---\nBody.\n", "folded.md"},
		{"bom-and-crlf", "\uFEFF---\r\nname: bom\r\ndescription: from windows\r\n---\r\nBody with CRLF.\r\n", "bom.md"},
		{"tier-lock-valid", "---\nname: tier\ntierLock: strong\n---\nBody.\n", "tier.md"},
		{"tier-lock-invalid", "---\nname: badtier\ntierLock: nope\n---\nBody.\n", "badtier.md"},
		{"body-trimmed", "---\nname: trim\n---\n\n\n   Padded body   \n\n\n", "trim.md"},
		{"array-bracket-unquoted", "---\nname: arr\ntriggers: [alpha, beta]\n---\nBody.\n", "arr.md"},
	}
}

// ── renderDiscoveryBlock 差分 ──

// mkOracleRegistry 必须与 gen-oracle.ts 的 mkRegistry 一致。
//
// **夹具设计**：相关的 skill 必须**字母序靠后**（zeta 而非 alpha）——
// 否则「relevant 优先排序」与「纯字母序」产出相同结果，排序变异逃逸
// （M136 首轮实测发现）。
func mkOracleRegistry() *Registry {
	r := NewRegistry()
	r.Register(Definition{Name: "alpha", Description: "first alphabetically", Body: "A"})
	r.Register(Definition{Name: "mid", Description: "middle one", Triggers: mustRe("(?i)other"), Body: "M"})
	r.Register(Definition{Name: "zeta", Description: "last alphabetically", Triggers: mustRe("(?i)match-me"), Body: "Z"})
	return r
}

func mustRe(s string) []*regexp.Regexp {
	return compileTriggers([]any{s})
}

func TestRenderDiscoveryBlockParity(t *testing.T) {
	o := loadSkillsOracle(t)

	t.Run("noHint", func(t *testing.T) {
		assertBlockEqual(t, mkOracleRegistry().RenderDiscoveryBlock("", DiscoveryOpts{}), deref(o.Discovery.NoHint))
	})
	t.Run("withHintMatch", func(t *testing.T) {
		assertBlockEqual(t, mkOracleRegistry().RenderDiscoveryBlock("please match-me now", DiscoveryOpts{}), deref(o.Discovery.WithHintMatch))
	})
	t.Run("withHintNoMatch", func(t *testing.T) {
		assertBlockEqual(t, mkOracleRegistry().RenderDiscoveryBlock("nothing relevant", DiscoveryOpts{}), deref(o.Discovery.WithHintNoMatch))
	})
	t.Run("emptyRegistry", func(t *testing.T) {
		assertBlockEqual(t, NewRegistry().RenderDiscoveryBlock("", DiscoveryOpts{}), deref(o.Discovery.EmptyRegistry))
	})
	t.Run("exclude", func(t *testing.T) {
		got := mkOracleRegistry().RenderDiscoveryBlock("", DiscoveryOpts{Exclude: map[string]bool{"mid": true}})
		assertBlockEqual(t, got, deref(o.Discovery.Exclude))
	})
	t.Run("tinyBudget", func(t *testing.T) {
		got := mkOracleRegistry().RenderDiscoveryBlock("", DiscoveryOpts{MaxChars: 60})
		assertBlockEqual(t, got, deref(o.Discovery.TinyBudget))
	})
	t.Run("shortDesc", func(t *testing.T) {
		got := mkOracleRegistry().RenderDiscoveryBlock("", DiscoveryOpts{MaxDescChars: 5})
		assertBlockEqual(t, got, deref(o.Discovery.ShortDesc))
	})
	t.Run("whitespaceDesc", func(t *testing.T) {
		r := NewRegistry()
		r.Register(Definition{Name: "ws", Description: "multi\n  line\t\tdesc", Body: "W"})
		assertBlockEqual(t, r.RenderDiscoveryBlock("", DiscoveryOpts{}), deref(o.Discovery.WhitespaceDesc))
	})
}

func assertBlockEqual(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("发现块不符：\n--- Go ---\n%s\n--- TS ---\n%s", got, want)
	}
}

// ── listSkillFiles 差分 ──

func TestListSkillFilesParity(t *testing.T) {
	o := loadSkillsOracle(t)
	root := filepath.Join("..", "..", "testdata", "skills", "fixtures", "dirskill")
	if _, err := os.Stat(root); err != nil {
		t.Skipf("夹具目录不存在（先跑 gen-oracle.ts）：%v", err)
	}
	got := ListSkillFiles(root, FileListOpts{})
	if len(got) != len(o.SkillFiles) {
		t.Fatalf("条目数：Go=%d TS=%d\nGo=%+v", len(got), len(o.SkillFiles), got)
	}
	for i, want := range o.SkillFiles {
		if got[i].Path != want.Path || got[i].Kind != want.Kind {
			t.Errorf("[%d]：Go={%q,%q} TS={%q,%q}", i, got[i].Path, got[i].Kind, want.Path, want.Kind)
		}
	}
}

// 路径必须用正斜杠（Windows 上 filepath.Rel 返回反斜杠）。
func TestListSkillFilesForwardSlashes(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "references", "a.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := ListSkillFiles(root, FileListOpts{})
	for _, e := range got {
		if contains(e.Path, "\\") {
			t.Errorf("路径含反斜杠：%q", e.Path)
		}
	}
}
