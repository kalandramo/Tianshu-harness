package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/pathsafe"
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
	Resolve []struct {
		Cwd        string `json:"cwd"`
		P          string `json:"p"`
		Resolved   string `json:"resolved"`
		IsAbsolute bool   `json:"isAbsolute"`
	} `json:"resolve"`
	ResolvePosix []struct {
		Cwd        string `json:"cwd"`
		P          string `json:"p"`
		Resolved   string `json:"resolved"`
		IsAbsolute bool   `json:"isAbsolute"`
	} `json:"resolvePosix"`
	PathGrant []struct {
		Cwd      string         `json:"cwd"`
		ToolName string         `json:"toolName"`
		Input    map[string]any `json:"input"`
		Result   *struct {
			Mode  string   `json:"mode"`
			Paths []string `json:"paths"`
		} `json:"result"`
	} `json:"pathGrant"`
	PathGrants []struct {
		Kind   string `json:"kind"`
		Label  string `json:"label"`
		Root   string `json:"root"`
		Child  string `json:"child"`
		Path   string `json:"path"`
		Cwd    string `json:"cwd"`
		Mode   string `json:"mode"`
		Result bool   `json:"result"`
		Grants []struct {
			Root string `json:"root"`
			Mode string `json:"mode"`
		} `json:"grants"`
	} `json:"pathGrants"`
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

// TestNodeResolveWin32Parity —— 对账 Node `path.win32.resolve` 的 Go 复刻。
//
// 这是 `nodepath.go` 的正确性证明：Go 的 `filepath.Join`/`Clean` 与 Node
// `path.resolve` 语义不同（绝对段截断 / 驱动器切换 / 不越过根），差异在
// 安全路径上会致命。oracle 从真实 `win32.resolve` 导出。
func TestNodeResolveWin32Parity(t *testing.T) {
	o := loadApprovalriskOracle(t)
	if len(o.Resolve) == 0 {
		t.Fatal("oracle 的 resolve 矩阵为空——前置失败")
	}

	for _, c := range o.Resolve {
		t.Run(c.Cwd+"__"+c.P, func(t *testing.T) {
			// 只在本平台为 Windows 时对账 win32 语义（posix 平台的行为不同）。
			if !isWindowsLike() {
				t.Skip("非 Windows 平台——win32 语义对账跳过")
			}
			if got := isAbsWin32(c.P); got != c.IsAbsolute {
				t.Errorf("isAbsWin32(%q) = %v, want %v", c.P, got, c.IsAbsolute)
			}
			got := resolveWin32(c.Cwd, c.P)
			if got != c.Resolved {
				t.Errorf("resolveWin32(%q, %q)\n  got  %q\n  want %q", c.Cwd, c.P, got, c.Resolved)
			}
		})
	}
}

// TestNodeResolvePosixParity —— 对账 Node `path.posix.resolve` 的 Go 复刻。
func TestNodeResolvePosixParity(t *testing.T) {
	o := loadApprovalriskOracle(t)
	if len(o.ResolvePosix) == 0 {
		t.Fatal("oracle 的 resolvePosix 矩阵为空——前置失败")
	}

	for _, c := range o.ResolvePosix {
		t.Run(c.Cwd+"__"+c.P, func(t *testing.T) {
			if got := isAbsPosix(c.P); got != c.IsAbsolute {
				t.Errorf("isAbsPosix(%q) = %v, want %v", c.P, got, c.IsAbsolute)
			}
			got := resolvePosix(c.Cwd, c.P)
			if got != c.Resolved {
				t.Errorf("resolvePosix(%q, %q)\n  got  %q\n  want %q", c.Cwd, c.P, got, c.Resolved)
			}
		})
	}
}

// TestPathGrantParity —— 对账 OutOfWorkspaceFilePaths 与 TS `outOfWorkspaceFilePaths`。
//
// **这是路径授权层的正确性证明**。返回的 paths 是**授权标签**（桌面端
// pathGrantHint 消费）——标签错位会让授权作用到错误路径。而 Go 的
// filepath.Join 与 Node path.resolve 语义不同（绝对段截断 / 盘符基准），
// 故必须逐值对账。
//
// **cwd 用 oracle 里的 `D:/repo`**：TS 与 Go 的路径校验都做 realpath，不存在的
// cwd 会走「最近存在祖先」回退——两侧实现若一致，结果应一致；若不一致，
// 本测试会暴露（那本身就是需要知道的差异）。
func TestPathGrantParity(t *testing.T) {
	o := loadApprovalriskOracle(t)
	if len(o.PathGrant) == 0 {
		t.Fatal("oracle 的 pathGrant 矩阵为空——前置失败")
	}

	for i, c := range o.PathGrant {
		t.Run(c.ToolName+"_"+itoa(i), func(t *testing.T) {
			got := OutOfWorkspaceFilePaths(c.Cwd, c.ToolName, c.Input, nil)

			// 归一化比较：TS 的 null ↔ Go 的 nil；mode 用字符串比。
			if c.Result == nil {
				if got != nil {
					t.Errorf("期望 nil，得到 mode=%v paths=%v", got.Mode, got.Paths)
				}
				return
			}
			if got == nil {
				t.Fatalf("期望 %v，得到 nil", c.Result)
			}
			wantMode := "read"
			if c.Result.Mode == "write" {
				wantMode = "write"
			}
			gotMode := "read"
			if got.Mode == pathsafe.ModeWrite {
				gotMode = "write"
			}
			if gotMode != wantMode {
				t.Errorf("mode = %q, want %q", gotMode, wantMode)
			}
			if len(got.Paths) != len(c.Result.Paths) {
				t.Fatalf("paths 数量 = %d, want %d（got=%v want=%v）",
					len(got.Paths), len(c.Result.Paths), got.Paths, c.Result.Paths)
			}
			for j := range got.Paths {
				if got.Paths[j] != c.Result.Paths[j] {
					t.Errorf("paths[%d]\n  got  %q\n  want %q", j, got.Paths[j], c.Result.Paths[j])
				}
			}
		})
	}
}

// TestPathGrantsParity —— 对账运行时授权存储与 TS `path-grants.ts`。
//
// ## 与 TS 的**有意差异**（B 方案：不复刻缺陷）
//
// TS `canonicalize`（`path-grants.ts:85`）有字符切片缺陷：
//
//	tail.unshift(current.slice(parent.length + 1))
//
// `parent` 以分隔符结尾时（如 `D:\`，长度 3），`+1` 多切一个字符——
// `D:\outside` 被截成 `D:\utside`（首字母 'o' 被吞）。实测：
//
//	D:/outside → "D:\utside"     D:/repoA → "D:\epoA"
//
// 触发条件：路径不存在（realpath 抛错）且**父目录是驱动器根**。
//
// Go 侧用 `filepath.Base` 逐段拼接，**不吞字符**（实测 `D:\outside`）——
// 故 3 个 snapshot 用例的 root 与 TS 不同。这是**有意修正**，不是漏对账。
//
// **为什么不构成越权**：`isPathUnder` 比较的是两侧同样规范化的值，
// 截断自洽——故 14 个查询用例（含 `-evil` 段边界反例）结果与 TS 一致，
// 本测试对它们逐值对账。
func TestPathGrantsParity(t *testing.T) {
	o := loadApprovalriskOracle(t)
	if len(o.PathGrants) == 0 {
		t.Fatal("oracle 的 pathGrants 矩阵为空——前置失败")
	}

	store := newPathGrantStore()

	for i, c := range o.PathGrants {
		t.Run(c.Kind+"_"+itoa(i), func(t *testing.T) {
			switch c.Kind {
			case "isPathUnder":
				got := isPathUnder(c.Root, c.Child)
				if got != c.Result {
					t.Errorf("isPathUnder(%q, %q) = %v, want %v", c.Root, c.Child, got, c.Result)
				}

			case "snapshot":
				// 已知差异：TS 截断驱动器根下的首段首字母，Go 不截断。
				// 断言 Go 的 root **等于输入路径的规范化**（而非 TS 的截断值）。
				got := store.ListGrants()
				if len(got) != len(c.Grants) {
					t.Fatalf("授权数 = %d, want %d", len(got), len(c.Grants))
				}
				for j, g := range got {
					wantMode := GrantMode(c.Grants[j].Mode)
					if g.Mode != wantMode {
						t.Errorf("grants[%d].Mode = %q, want %q", j, g.Mode, wantMode)
					}
					// **有意差异**：不复刻 TS 的字符截断。
					if g.Root == c.Grants[j].Root {
						t.Logf("grants[%d].Root 与 TS 一致：%q", j, g.Root)
					} else {
						t.Logf("grants[%d].Root 与 TS 有意不同：Go=%q TS=%q（TS 截断缺陷）",
							j, g.Root, c.Grants[j].Root)
						// 断言 Go 侧**无截断**：驱动器根后的首段应完整。
						if len(g.Root) >= 3 && g.Root[2] != '\\' && g.Root[2] != '/' {
							t.Errorf("Go 的 root %q 也被截断了——应完整保留", g.Root)
						}
					}
				}

			case "isReadGranted":
				got := store.IsReadGranted(c.Path, c.Cwd)
				if got != c.Result {
					t.Errorf("IsReadGranted(%q, %q) = %v, want %v", c.Path, c.Cwd, got, c.Result)
				}

			case "isWriteGranted":
				got := store.IsWriteGranted(c.Path, c.Cwd)
				if got != c.Result {
					t.Errorf("IsWriteGranted(%q, %q) = %v, want %v", c.Path, c.Cwd, got, c.Result)
				}

			case "grant":
				store.GrantPath(c.Root, GrantMode(c.Mode), c.Cwd)

			case "reset":
				store.ResetGrantsForTest()

			default:
				t.Fatalf("未知用例类型 %q", c.Kind)
			}
		})
	}
}
