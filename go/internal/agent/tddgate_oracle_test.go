package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// tddgate_oracle_test.go —— Go `EvaluateTddGate` 对账 TS 真实输出（第七十二刀）。
//
// # 为什么用 oracle 而不是手写断言
//
// `evaluateTddGate` 的分支密度高（6 个 state 字段 × config 四态 × 工具类 ×
// 目标路径类）。手写断言会把「我以为的语义」写成期望——而 oracle 来自 TS
// **真实执行**。
//
// **oracle 当场抓到一个手写必错的差异**：`enforce-test-target-camelcase`
// （目标 `src/foo_test.go`）的真实输出是 **block**，不是 suggest。原因是
// `tdd-gate.ts` 的 `isTestFile` 正则是 `/\.(test|spec)\./` 或 `__tests__`，
// **不匹配** `foo_test.go`；而 `evidence.go` 的 `testFileRe`（决定
// `hasReadTestFiles`）**含** `_test.` / `test_`。**两者是不同正则**——
// 复用会让「测试文件 RED 步骤豁免」多出一条不该有的路径。
//
// 生成 oracle：
//
//	cd <仓库根> && npx tsx go/testdata/tddgate/gen-oracle.ts

type tddOracleCase struct {
	Name  string `json:"name"`
	Input struct {
		GateState struct {
			FilesModified      int  `json:"filesModified"`
			Verifications      int  `json:"verifications"`
			EditsSinceLastTest int  `json:"editsSinceLastTest"`
			HasFailedTests     bool `json:"hasFailedTests"`
			HasCodeEdits       bool `json:"hasCodeEdits"`
			HasReadTestFiles   bool `json:"hasReadTestFiles"`
		} `json:"gateState"`
		ToolName string `json:"toolName"`
		Config   struct {
			Enabled       bool   `json:"enabled"`
			Mode          string `json:"mode"`
			Threshold     int    `json:"threshold"`
			SkipIfNoTests bool   `json:"skipIfNoTests"`
		} `json:"config"`
		TargetPath *string `json:"targetPath"`
	} `json:"input"`
	Output struct {
		Action  string `json:"action"`
		Message string `json:"message"`
	} `json:"output"`
}

func loadTddOracle(t *testing.T) []tddOracleCase {
	t.Helper()
	p := filepath.Join("..", "..", "testdata", "tddgate", "oracle.json")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读 oracle 失败（先跑 npx tsx go/testdata/tddgate/gen-oracle.ts）: %v", err)
	}
	var doc struct {
		GeneratedBy string          `json:"generatedBy"`
		Cases       []tddOracleCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("解析 oracle 失败: %v", err)
	}
	if len(doc.Cases) == 0 {
		t.Fatal("oracle 用例数为 0——生成器没跑成功")
	}
	return doc.Cases
}

// TestEvaluateTddGateParity —— 逐用例对账 TS 真实决策。
func TestEvaluateTddGateParity(t *testing.T) {
	cases := loadTddOracle(t)
	t.Logf("oracle 用例数：%d", len(cases))

	// 覆盖断言：确保 oracle 真的覆盖了三态（防止生成器退化）
	seen := map[string]int{}
	for _, c := range cases {
		seen[c.Output.Action]++
	}
	for _, want := range []string{"allow", "suggest", "block"} {
		if seen[want] == 0 {
			t.Errorf("oracle 缺 %q 态——生成器覆盖不足（实测分布 %v）", want, seen)
		}
	}

	for _, c := range cases {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			gs := tddGateState{
				FilesModified:      c.Input.GateState.FilesModified,
				Verifications:      c.Input.GateState.Verifications,
				EditsSinceLastTest: c.Input.GateState.EditsSinceLastTest,
				HasFailedTests:     c.Input.GateState.HasFailedTests,
				HasCodeEdits:       c.Input.GateState.HasCodeEdits,
				HasReadTestFiles:   c.Input.GateState.HasReadTestFiles,
			}
			cfg := TddGateConfig{
				Enabled:       c.Input.Config.Enabled,
				Mode:          c.Input.Config.Mode,
				Threshold:     c.Input.Config.Threshold,
				SkipIfNoTests: c.Input.Config.SkipIfNoTests,
			}
			var target string
			if c.Input.TargetPath != nil {
				target = *c.Input.TargetPath
			}

			got := EvaluateTddGate(gs, c.Input.ToolName, cfg, target)

			if got.Action != c.Output.Action {
				t.Errorf("action 不等价：TS=%q Go=%q\n  message: TS=%q Go=%q\n  input: %+v",
					c.Output.Action, got.Action, c.Output.Message, got.Message, c.Input)
			}
			// message 逐字对账——它会进模型的下一轮请求（不是内部日志）
			if got.Message != c.Output.Message {
				t.Errorf("message 不等价：\n  TS=%q\n  Go=%q", c.Output.Message, got.Message)
			}
		})
	}
}

// TestEditToolsSetMatchesTS —— EDIT_TOOLS 集合必须与 TS 一致。
//
// 独立断言（不靠 oracle）：oracle 只覆盖了集合内的工具名，集合本身
// 多一个或少一个都不会被 parity 测试发现。
func TestEditToolsSetMatchesTS(t *testing.T) {
	want := []string{"edit_file", "write_file", "apply_patch", "hash_edit"}
	if len(editTools) != len(want) {
		t.Fatalf("EDIT_TOOLS 大小不等：Go=%d TS=%d（%v）", len(editTools), len(want), want)
	}
	for _, n := range want {
		if !editTools[n] {
			t.Errorf("EDIT_TOOLS 缺 %q", n)
		}
	}
}

// TestIsTddTestFileRegexDiffersFromEvidence —— **钉住两个正则的差异**。
//
// `isTddTestFile`（决定 block→suggest 降级）与 `isTestFile`（evidence.go 里
// 决定 `hasReadTestFiles`）是**不同**的正则。若将来有人「统一」它们，
// 本测试会红——那是**行为回归**，不是重构。
func TestIsTddTestFileRegexDiffersFromEvidence(t *testing.T) {
	// `foo_test.go` 只匹配 evidence 的 testFileRe（含 `_test.`），
	// **不**匹配 tdd-gate 的 isTestFile（只认 `.test.`/`.spec.`/`__tests__`）。
	if !testFileRe.MatchString("src/foo_test.go") {
		t.Error("evidence 的 testFileRe 应匹配 `_test.`（hasReadTestFiles 用）")
	}
	if isTddTestFile("src/foo_test.go") {
		t.Error("tdd-gate 的 isTddTestFile **不该**匹配 `foo_test.go`——" +
			"TS 侧正则只认 `.test.`/`.spec.`/`__tests__`；若这里为 true，" +
			"enforce 模式下改 Go 测试文件会被误豁免")
	}
	// 两者都认的形态
	for _, p := range []string{"src/a.test.ts", "src/a.spec.ts", "src/__tests__/a.ts"} {
		if !isTddTestFile(p) {
			t.Errorf("isTddTestFile 应匹配 %q", p)
		}
	}
}

// TestParseTddGateConfigMatrix —— 原始取值解析矩阵（对账 TS parseTddGateConfig）。
//
// **注意**：直接测 `parseTddGateEnv`（装配层读 env 后传入的原始值）——
// Go 侧内核不读 `os.Getenv`（见 `tddgate.go` 的说明）。
func TestParseTddGateConfigMatrix(t *testing.T) {
	cases := []struct {
		raw         string
		wantEnabled bool
		wantMode    string
	}{
		{"", true, "suggest"},          // 未设 → 默认
		{"suggest", true, "suggest"},   // 显式 suggest
		{"advisory", true, "suggest"},  // 未知 → suggest
		{"xyz", true, "suggest"},       // 未知 → suggest
		{"off", false, "suggest"},      // 关闭
		{"0", false, "suggest"},        // 关闭
		{"false", false, "suggest"},    // 关闭
		{"disabled", false, "suggest"}, // 关闭
		{"enforce", true, "enforce"},   // 硬拦
		{"on", true, "enforce"},        // 硬拦
		{"1", true, "enforce"},         // 硬拦
		{"true", true, "enforce"},      // 硬拦
		{"ENFORCE", true, "enforce"},   // 大小写不敏感
		{"  off  ", false, "suggest"},  // 首尾空白
	}
	for _, c := range cases {
		t.Run("raw="+c.raw, func(t *testing.T) {
			got := parseTddGateEnv(c.raw)
			if got.Enabled != c.wantEnabled {
				t.Errorf("enabled: want %v got %v", c.wantEnabled, got.Enabled)
			}
			if got.Mode != c.wantMode {
				t.Errorf("mode: want %q got %q", c.wantMode, got.Mode)
			}
			// 其余字段恒为默认
			if got.Threshold != DefaultTddGateConfig.Threshold {
				t.Errorf("threshold 应恒为默认 %d，得 %d", DefaultTddGateConfig.Threshold, got.Threshold)
			}
			if got.SkipIfNoTests != DefaultTddGateConfig.SkipIfNoTests {
				t.Errorf("skipIfNoTests 应恒为默认")
			}
		})
	}
}

// TestDefaultTddGateConfigValues —— 默认值逐字段钉住（对账 TS）。
//
// **特别钉 `mode: suggest`**：TS 注释说明默认**不硬拦**（session 05e1500e
// 显示 enforce 在修复中途会把 agent 逼进重写循环）。默认值变了就是行为变更。
func TestDefaultTddGateConfigValues(t *testing.T) {
	d := DefaultTddGateConfig
	if !d.Enabled {
		t.Error("默认应 enabled")
	}
	if d.Mode != "suggest" {
		t.Errorf("默认 mode 应为 suggest（不硬拦），得 %q", d.Mode)
	}
	if d.Threshold != 3 {
		t.Errorf("默认 threshold 应为 3，得 %d", d.Threshold)
	}
	if !d.SkipIfNoTests {
		t.Error("默认 skipIfNoTests 应为 true（无测试的项目不该被永久卡住）")
	}
}
