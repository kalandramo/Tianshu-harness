package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// approvalriskOracle 是 oracle.json 的结构。
//
// 由 `go/testdata/approvalrisk/gen-oracle.ts` 从**真实 TS 代码路径**导出。
// 手抄 golden 会引入自洽假绿（Go 与手抄双方同错）——本项目已因此出过事故。
type approvalriskOracle struct {
	Commands []struct {
		Label                       string `json:"label"`
		Cmd                         string `json:"cmd"`
		NormalizeBashCommand        string `json:"normalizeBashCommand"`
		MatchesDangerousBash        bool   `json:"matchesDangerousBash"`
		MatchesForegroundOnlyHazard bool   `json:"matchesForegroundOnlyHazard"`
		MatchesInputSynthesis       bool   `json:"matchesInputSynthesis"`
		BashCommandMayWrite         bool   `json:"bashCommandMayWrite"`
		IsSafeWriteOnly             bool   `json:"isSafeWriteOnly"`
		HasOutOfWorkspaceWriteTgt   bool   `json:"hasOutOfWorkspaceWriteTarget"`
		BashGitBypassesScope        bool   `json:"bashGitBypassesScope"`
		RequiresBashWriteApproval   bool   `json:"requiresBashWriteApproval"`
	} `json:"commands"`
	GitActions []struct {
		Action string `json:"action"`
		Result bool   `json:"result"`
	} `json:"gitActions"`
	DestructiveGitViaBash []struct {
		Cmd    string `json:"cmd"`
		Result bool   `json:"result"`
	} `json:"destructiveGitViaBash"`
	Unconditional []struct {
		ToolName string         `json:"toolName"`
		Input    map[string]any `json:"input"`
		Result   bool           `json:"result"`
	} `json:"unconditional"`
	RiskBaseline []struct {
		ToolName        string         `json:"toolName"`
		Input           map[string]any `json:"input"`
		Level           string         `json:"level"`
		SuggestedAction string         `json:"suggestedAction"`
	} `json:"riskBaseline"`
	DoomLoop []struct {
		DoomLoopLevel   string         `json:"doomLoopLevel"`
		ToolName        string         `json:"toolName"`
		Input           map[string]any `json:"input"`
		Level           string         `json:"level"`
		SuggestedAction string         `json:"suggestedAction"`
	} `json:"doomLoop"`
}

func loadApprovalriskOracle(t *testing.T) *approvalriskOracle {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "approvalrisk", "oracle.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败（先跑 npx tsx go/testdata/approvalrisk/gen-oracle.ts）：%v", err)
	}
	var o approvalriskOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	if len(o.Commands) == 0 {
		t.Fatal("oracle 命令集为空——前置失败，测试无意义")
	}
	return &o
}

// TestApprovalRiskCommandParity —— 逐命令对账 9 个判定函数。
//
// 这是 RE2 改写正确性的**唯一强制手段**：TS 的 6 条 lookaround 正则被改写为
// 结构化判定，语义是否等价只有对账能证明。
func TestApprovalRiskCommandParity(t *testing.T) {
	o := loadApprovalriskOracle(t)

	for _, c := range o.Commands {
		t.Run(c.Label, func(t *testing.T) {
			if got := NormalizeBashCommand(c.Cmd); got != c.NormalizeBashCommand {
				t.Errorf("NormalizeBashCommand\n  got  %q\n  want %q", got, c.NormalizeBashCommand)
			}
			if got := MatchesDangerousBash(c.Cmd); got != c.MatchesDangerousBash {
				t.Errorf("MatchesDangerousBash = %v, want %v（cmd=%q）", got, c.MatchesDangerousBash, c.Cmd)
			}
			if got := MatchesForegroundOnlyHazard(c.Cmd); got != c.MatchesForegroundOnlyHazard {
				t.Errorf("MatchesForegroundOnlyHazard = %v, want %v", got, c.MatchesForegroundOnlyHazard)
			}
			if got := MatchesInputSynthesis(c.Cmd); got != c.MatchesInputSynthesis {
				t.Errorf("MatchesInputSynthesis = %v, want %v", got, c.MatchesInputSynthesis)
			}
			if got := BashCommandMayWrite(c.Cmd); got != c.BashCommandMayWrite {
				t.Errorf("BashCommandMayWrite = %v, want %v（cmd=%q）", got, c.BashCommandMayWrite, c.Cmd)
			}
			if got := IsSafeWriteOnly(c.Cmd); got != c.IsSafeWriteOnly {
				t.Errorf("IsSafeWriteOnly = %v, want %v（cmd=%q）", got, c.IsSafeWriteOnly, c.Cmd)
			}
			if got := HasOutOfWorkspaceWriteTarget(c.Cmd); got != c.HasOutOfWorkspaceWriteTgt {
				t.Errorf("HasOutOfWorkspaceWriteTarget = %v, want %v（cmd=%q）", got, c.HasOutOfWorkspaceWriteTgt, c.Cmd)
			}
			if got := BashGitBypassesScope(c.Cmd); got != c.BashGitBypassesScope {
				t.Errorf("BashGitBypassesScope = %v, want %v", got, c.BashGitBypassesScope)
			}
			if got := RequiresBashWriteApproval("bash", map[string]any{"command": c.Cmd}); got != c.RequiresBashWriteApproval {
				t.Errorf("RequiresBashWriteApproval = %v, want %v", got, c.RequiresBashWriteApproval)
			}
		})
	}
}

// TestApprovalRiskDestructiveGitParity —— 对账 IsDestructiveGitAction 两个分支。
func TestApprovalRiskDestructiveGitParity(t *testing.T) {
	o := loadApprovalriskOracle(t)

	for _, g := range o.GitActions {
		got := IsDestructiveGitAction("git", map[string]any{"action": g.Action})
		if got != g.Result {
			t.Errorf("IsDestructiveGitAction(git, %q) = %v, want %v", g.Action, got, g.Result)
		}
	}
	for _, g := range o.DestructiveGitViaBash {
		got := IsDestructiveGitAction("bash", map[string]any{"command": g.Cmd})
		if got != g.Result {
			t.Errorf("IsDestructiveGitAction(bash, %q) = %v, want %v", g.Cmd, got, g.Result)
		}
	}
}

// TestApprovalRiskUnconditionalParity —— 对账 RequiresUnconditionalApproval。
func TestApprovalRiskUnconditionalParity(t *testing.T) {
	o := loadApprovalriskOracle(t)

	for i, u := range o.Unconditional {
		got := RequiresUnconditionalApproval(u.ToolName, u.Input)
		if got != u.Result {
			t.Errorf("用例[%d] RequiresUnconditionalApproval(%q, %v) = %v, want %v",
				i, u.ToolName, u.Input, got, u.Result)
		}
	}
}

// TestApprovalRiskBaselineParity —— 对账 AssessToolRisk 的基线分支。
//
// **范围说明**：只对账无 sensorium/antibody/MCP 的用例（那些输入 Go 侧
// 尚无）。sensorium 升级与 MCP 策略分支未实现，传入 nil 等价于 TS 省略。
func TestApprovalRiskBaselineParity(t *testing.T) {
	o := loadApprovalriskOracle(t)

	for i, r := range o.RiskBaseline {
		got := AssessToolRisk(r.ToolName, r.Input, "none")
		if string(got.Level) != r.Level {
			t.Errorf("用例[%d] %s %v: level = %q, want %q（reasons=%v）",
				i, r.ToolName, r.Input, got.Level, r.Level, got.Reasons)
		}
		if got.SuggestedAction != r.SuggestedAction {
			t.Errorf("用例[%d] %s %v: suggestedAction\n  got  %q\n  want %q",
				i, r.ToolName, r.Input, got.SuggestedAction, r.SuggestedAction)
		}
	}
}

// TestApprovalRiskDoomLoopParity —— 对账 doom loop 窗口（三档 × 三种工具）。
func TestApprovalRiskDoomLoopParity(t *testing.T) {
	o := loadApprovalriskOracle(t)

	for i, d := range o.DoomLoop {
		got := AssessToolRisk(d.ToolName, d.Input, d.DoomLoopLevel)
		if string(got.Level) != d.Level {
			t.Errorf("用例[%d] doomLoop=%s %s: level = %q, want %q（reasons=%v）",
				i, d.DoomLoopLevel, d.ToolName, got.Level, d.Level, got.Reasons)
		}
		if got.SuggestedAction != d.SuggestedAction {
			t.Errorf("用例[%d] doomLoop=%s %s: suggestedAction\n  got  %q\n  want %q",
				i, d.DoomLoopLevel, d.ToolName, got.SuggestedAction, d.SuggestedAction)
		}
	}
}
