package context

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// extractorOracleEntry 是 oracle 里一项。
type extractorOracleEntry struct {
	Proposals []extractorProposal `json:"proposals"`
}

// extractorProposal 是 scrub 后的提案形态。
type extractorProposal struct {
	Kind       string           `json:"kind"`
	Scope      string           `json:"scope"`
	Text       string           `json:"text"`
	Confidence float64          `json:"confidence"`
	Fitness    float64          `json:"fitness"`
	Source     ClaimSource      `json:"source"`
	Tags       []string         `json:"tags"`
	TTL        any              `json:"ttl"`
	Evidence   []map[string]any `json:"evidence"`
}

// extractorCase 是 cases 里一项。
type extractorCase struct {
	Ctx struct {
		ToolName string         `json:"toolName"`
		Input    map[string]any `json:"input"`
		Result   string         `json:"result"`
		IsError  bool           `json:"isError"`
	} `json:"ctx"`
	Existing []string `json:"existing"`
}

func loadExtractorOracle(t *testing.T) map[string]extractorOracleEntry {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "claims", "extractor-oracle.json"))
	if err != nil {
		t.Fatalf("读 oracle 失败（先跑 node_modules/.bin/tsx go/testdata/claims/gen-extractor-oracle.ts）：%v", err)
	}
	var out map[string]extractorOracleEntry
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	if len(out) == 0 {
		t.Fatal("oracle 为空")
	}
	return out
}

func loadExtractorCases(t *testing.T) map[string]extractorCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "claims", "extractor-cases.json"))
	if err != nil {
		t.Fatalf("读 cases 失败：%v", err)
	}
	var out map[string]extractorCase
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析 cases 失败：%v", err)
	}
	return out
}

// TestClaimExtractorOracleParity —— **提取器对账**（27 用例）。
func TestClaimExtractorOracleParity(t *testing.T) {
	oracle := loadExtractorOracle(t)
	cases := loadExtractorCases(t)

	if len(cases) != len(oracle) {
		t.Fatalf("用例数不匹配：cases=%d oracle=%d", len(cases), len(oracle))
	}

	for name, entry := range oracle {
		t.Run(name, func(t *testing.T) {
			spec, ok := cases[name]
			if !ok {
				t.Fatalf("oracle 有 %q 但 cases 没有", name)
			}

			var existing map[string]bool
			if len(spec.Existing) > 0 {
				existing = map[string]bool{}
				for _, p := range spec.Existing {
					existing[p] = true
				}
			}

			got := ExtractClaimsFromToolResult(
				ToolResultContext{
					ToolName: spec.Ctx.ToolName,
					Input:    spec.Ctx.Input,
					Result:   spec.Ctx.Result,
					IsError:  spec.Ctx.IsError,
				},
				ClaimExtractionMeta{SessionID: "sess-x", Turn: 3, EventID: "ev-42"},
				existing,
				T0,
			)

			if len(got) != len(entry.Proposals) {
				t.Fatalf("提案数：Go=%d TS=%d\nGo:  %+v", len(got), len(entry.Proposals), got)
			}

			for i, want := range entry.Proposals {
				g := got[i]
				if string(g.Kind) != want.Kind {
					t.Errorf("[%d] kind：Go=%q TS=%q", i, g.Kind, want.Kind)
				}
				if string(g.Scope) != want.Scope {
					t.Errorf("[%d] scope：Go=%q TS=%q", i, g.Scope, want.Scope)
				}
				if g.Text != want.Text {
					t.Errorf("[%d] text：\nGo: %q\nTS: %q", i, g.Text, want.Text)
				}
				if g.Confidence != want.Confidence {
					t.Errorf("[%d] confidence：Go=%v TS=%v", i, g.Confidence, want.Confidence)
				}
				if g.Fitness != want.Fitness {
					t.Errorf("[%d] fitness：Go=%v TS=%v", i, g.Fitness, want.Fitness)
				}
				if g.Source.Actor != want.Source.Actor {
					t.Errorf("[%d] source.actor：Go=%q TS=%q", i, g.Source.Actor, want.Source.Actor)
				}
				if g.Source.SessionID != want.Source.SessionID {
					t.Errorf("[%d] source.sessionId：Go=%q TS=%q", i, g.Source.SessionID, want.Source.SessionID)
				}
				if g.Source.Turn != want.Source.Turn {
					t.Errorf("[%d] source.turn：Go=%d TS=%d", i, g.Source.Turn, want.Source.Turn)
				}
				if g.Source.EventID != want.Source.EventID {
					t.Errorf("[%d] source.eventId：Go=%q TS=%q", i, g.Source.EventID, want.Source.EventID)
				}
				// tags
				if len(g.Tags) != len(want.Tags) {
					t.Errorf("[%d] tags 数：Go=%v TS=%v", i, g.Tags, want.Tags)
				} else {
					for j := range g.Tags {
						if g.Tags[j] != want.Tags[j] {
							t.Errorf("[%d] tags[%d]：Go=%q TS=%q", i, j, g.Tags[j], want.Tags[j])
						}
					}
				}
				// ttl（相对值）
				switch v := want.TTL.(type) {
				case nil:
					if g.ExpiresAt != 0 {
						t.Errorf("[%d] TTL 应为 Infinity（ExpiresAt=0），Go=%d", i, g.ExpiresAt)
					}
				case string:
					if v == "Infinity" && g.ExpiresAt != 0 {
						t.Errorf("[%d] TTL 应为 Infinity，Go=%d", i, g.ExpiresAt)
					}
				case float64:
					gotTTL := g.ExpiresAt - g.CreatedAt
					if gotTTL != int64(v) {
						t.Errorf("[%d] TTL：Go=%d TS=%v", i, gotTTL, v)
					}
				}
				// evidence
				if len(g.Evidence) != len(want.Evidence) {
					t.Fatalf("[%d] evidence 数：Go=%d TS=%d", i, len(g.Evidence), len(want.Evidence))
				}
				for j, we := range want.Evidence {
					ge := g.Evidence[j]
					if we["id"] != nil && ge.ID != we["id"].(string) {
						t.Errorf("[%d] evidence[%d].id：Go=%q TS=%v", i, j, ge.ID, we["id"])
					}
					if we["kind"] != nil && string(ge.Kind) != we["kind"].(string) {
						t.Errorf("[%d] evidence[%d].kind：Go=%q TS=%v", i, j, ge.Kind, we["kind"])
					}
					if we["summary"] != nil && ge.Summary != we["summary"].(string) {
						t.Errorf("[%d] evidence[%d].summary：\nGo: %q\nTS: %v", i, j, ge.Summary, we["summary"])
					}
					if we["path"] != nil && ge.Path != we["path"].(string) {
						t.Errorf("[%d] evidence[%d].path：Go=%q TS=%v", i, j, ge.Path, we["path"])
					}
				}
			}
		})
	}
}

// TestClaimExtractorSkipTools —— SKIP_TOOLS 全覆盖（显式意图清单）。
func TestClaimExtractorSkipTools(t *testing.T) {
	for _, tool := range []string{"grep", "glob", "diff", "inspect_project", "repo_map", "related_tests", "recall"} {
		t.Run(tool, func(t *testing.T) {
			got := ExtractClaimsFromToolResult(
				ToolResultContext{ToolName: tool, Result: strings.Repeat("a", 100)},
				ClaimExtractionMeta{SessionID: "s", Turn: 1, EventID: "e"}, nil, T0)
			if len(got) != 0 {
				t.Errorf("%s 应被跳过，得到 %d 条", tool, len(got))
			}
		})
	}
}

// TestClaimExtractorSymbolCap —— 符号上限 10（text 里只显示 8）。
func TestClaimExtractorSymbolCap(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 20; i++ {
		sb.WriteString("export const s")
		sb.WriteString(strconv.Itoa(i))
		sb.WriteString(" = ")
		sb.WriteString(strconv.Itoa(i))
		sb.WriteString(";\n")
	}
	got := ExtractClaimsFromToolResult(
		ToolResultContext{ToolName: "read_file", Input: map[string]any{"file_path": "x.ts"}, Result: sb.String()},
		ClaimExtractionMeta{SessionID: "s", Turn: 1, EventID: "e"}, nil, T0)

	if len(got) != 1 {
		t.Fatalf("应提取 1 条，得到 %d", len(got))
	}
	// text 里最多 8 个符号
	if n := strings.Count(got[0].Text, ",") + 1; n > 8 {
		t.Errorf("text 里符号数应 ≤ 8，得到 %d：%q", n, got[0].Text)
	}
}

// TestClaimExtractorCommitHashAnchoring —— **hash 锚定**（TS 注释里的关键修复）。
//
// 正文里嵌的其他 hash **不应**被误取。
func TestClaimExtractorCommitHashAnchoring(t *testing.T) {
	// 正文含 deadbeef1234，但方括号里是 real123（含非 hex 'l' → 不匹配 → unknown）
	got := ExtractClaimsFromToolResult(
		ToolResultContext{
			ToolName: "git",
			Input:    map[string]any{"action": "commit", "message": "feat: revert"},
			Result:   "[main real123] feat: revert\n reverts deadbeef1234 (previous)\n 3 files changed\n a | 1\n b | 2\n c | 3\n",
		},
		ClaimExtractionMeta{SessionID: "s", Turn: 1, EventID: "e"}, nil, T0)

	if len(got) != 1 {
		t.Fatalf("应提取 1 条，得到 %d", len(got))
	}
	// 不应取正文里的 deadbeef1234
	if strings.Contains(got[0].Text, "deadbeef") {
		t.Errorf("不应取正文里的 hash：%q", got[0].Text)
	}
}
