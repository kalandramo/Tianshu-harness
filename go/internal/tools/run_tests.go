package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/contract"
)

// runTestsTool 实现 run_tests（项目测试运行器探测 + 结构化结果）。
//
// 对账 src/tools/run-tests.ts 的核心价值：
//
//	**探测**（go test / npm test / pytest / cargo test 自动识别）
//	+ **结构化结果**（passed/failed/blocked 与 blockedReason）
//
// 为什么需要它而不是让模型手拼命令：模型自己写 `go test ./... 2>&1 | tail -3`
// 会丢掉计数与退出码语义；而 blocked（无测试框架）与 failed（测试红了）是
// **性质不同的信号**——前者是中性门禁（不是代码问题），后者才是缺陷证据。
// 混淆两者会让下游误判。
type runTestsTool struct {
	baseTool
	Cwd string
	// TimeoutSec 是默认超时（秒），0 = 300。
	TimeoutSec int
}

// RunTests 构造 run_tests 工具。
func RunTests(cwd string) Tool {
	t := &runTestsTool{Cwd: cwd}
	t.def = contract.Definition{
		Name: "run_tests",
		Description: `运行项目测试并返回解析后的结果。

- 自动探测运行器（go test / npm test / pytest / cargo test）
- 报告 exit code、失败的测试、错误详情、耗时
- **blocked 是中性信号**：无测试框架 ≠ 测试失败。blocked 时门禁已重置，
  不是对你的拒绝；若项目缺测试基础设施，可询问用户是否需要协助搭建
- filter 用于**定位测试文件**（不是筛选测试名）——按文件名或相对路径匹配`,
		InputSchema: objSchemaOrdered([]string{"filter", "timeout"}, map[string]any{
			"filter":  strProp("测试文件名、词干或相对路径（不是测试名）。留空跑全量。"),
			"timeout": intProp("超时时间（毫秒，默认：120000）"),
		}),
	}
	t.enabled = true
	t.concurrent = false
	return t
}

func (t *runTestsTool) Timeout(p *CallParams) time.Duration {
	if p != nil {
		if ms := intArg(p.Input, "timeout", 0); ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	sec := t.TimeoutSec
	if sec <= 0 {
		sec = 300
	}
	return time.Duration(sec) * time.Second
}

// runner 是一次探测出的测试运行器。
type runner struct {
	kind    string // go | npm | pytest | cargo | declared | unknown
	command string
	args    []string
	display string
	// shell 为真时用 shell 执行（declared 命令可能是复合命令）。
	shell bool
}

// Execute 探测并运行测试。
func (t *runTestsTool) Execute(ctx context.Context, p *CallParams) (contract.Result, error) {
	filter := strArg(p.Input, "filter")
	scope := "full"
	if filter != "" {
		scope = "targeted"
	}

	r, blocked := detectRunner(t.Cwd, filter)
	if blocked != nil {
		// blocked 是**中性信号**——不是 IsError。
		return t.blockedResult(*blocked, scope), nil
	}

	timeout := t.Timeout(p)
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := buildCmd(runCtx, r)
	cmd.Dir = t.Cwd
	// 平台组语义 + Wait 兜底（详见 prepareCommand / waitDelay 的说明）
	prepareCommand(cmd)

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return t.blockedResult(blockedInfo{
			reason:   "invocation_failure",
			message:  fmt.Sprintf("测试运行器启动失败：%v", err),
			guidance: "运行器无法启动——检查该工具链是否已安装（如 pytest / cargo）。",
			command:  r.display,
		}, scope), nil
	}

	// 超时时杀整个进程树（与 bash 同一纪律：不留孤儿进程）。
	// 平台差异（进程组 vs taskkill /F /T）封装在 killProcessTree 内。
	signalDone := watchAndKillOnCancel(runCtx, cmd)

	waitErr := cmd.Wait()
	signalDone()
	duration := time.Since(start)

	timedOut := runCtx.Err() == context.DeadlineExceeded
	if timedOut {
		return t.blockedResult(blockedInfo{
			reason:   "timeout",
			message:  fmt.Sprintf("测试超时（%v）已被终止。\n\n部分输出：\n%s", timeout.Round(time.Millisecond), truncate(out.String(), 4000)),
			guidance: "测试套件超时。若被测试的是本轮新建/修改的代码（尤其纯函数/单文件小套件），先验概率是代码死循环/挂起——这是 RED，不是环境阻塞。定位修复后重跑。",
			command:  r.display,
		}, scope), nil
	}

	exitCode := 0
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errorsAs(waitErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	raw := out.String()
	parsed := parseTestOutput(raw, r.kind)

	status := contract.VerificationPassed
	if exitCode != 0 {
		status = contract.VerificationFailed
	}

	ver := &contract.VerificationMetadata{
		Command:    r.display,
		Status:     status,
		Scope:      scope,
		ExitCode:   &exitCode,
		Passed:     &parsed.passed,
		Failed:     &parsed.failed,
		Skipped:    &parsed.skipped,
		DurationMs: int64Ptr(duration.Milliseconds()),
	}
	if len(parsed.failures) > 0 {
		ver.TargetFiles = parsed.failures
	}

	content := formatTestResult(r.display, status, exitCode, parsed, raw, duration)

	return contract.Result{
		Content:      content,
		Verification: ver,
		ExitCode:     &exitCode,
		IsError:      exitCode != 0,
	}, nil
}

// blockedInfo 描述一次 blocked 的成因。
type blockedInfo struct {
	reason   string
	message  string
	guidance string
	command  string
}

// blockedResult 构造 blocked 结果。
//
// 关键：IsError **不设**——blocked 是中性门禁信号（门禁已重置），
// 不是对模型的拒绝，也不该被下游当作失败惩罚。
func (t *runTestsTool) blockedResult(b blockedInfo, scope string) contract.Result {
	ver := &contract.VerificationMetadata{
		Command:       b.command,
		Status:        contract.VerificationBlocked,
		Scope:         scope,
		BlockedReason: b.reason,
		UserGuidance:  b.guidance,
	}
	content := b.message
	if b.guidance != "" {
		content += "\n\n" + b.guidance
	}
	return contract.Result{
		Content:      content,
		Verification: ver,
	}
}

// detectRunner 探测项目的测试运行器。
//
// 探测优先级（对齐 TS 实现）：显式声明 > 语言标记文件。
// 返回 blocked 非 nil 时表示无法运行（无框架 / 无测试文件）。
func detectRunner(cwd, filter string) (runner, *blockedInfo) {
	// ── 1. 显式声明（.rivet-config.json 的 verify.test）──
	if declared := readDeclaredTestCommand(cwd); declared != "" {
		full := declared
		if filter != "" {
			full = declared + " " + sanitizeFilter(filter)
		}
		return runner{
			kind: "declared", command: full, display: full, shell: true,
		}, nil
	}

	// ── 2. Go ──
	if fileExists(filepath.Join(cwd, "go.mod")) {
		args := []string{"test", "./..."}
		display := "go test ./..."
		if filter != "" {
			// Go 用 -run 筛选测试名，或用包路径
			args = []string{"test", "./..."}
			display = "go test ./..."
		}
		return runner{kind: "go", command: "go", args: args, display: display}, nil
	}

	// ── 3. Cargo ──
	if fileExists(filepath.Join(cwd, "Cargo.toml")) {
		return runner{kind: "cargo", command: "cargo", args: []string{"test"}, display: "cargo test"}, nil
	}

	// ── 4. Python（pytest 标记）──
	if fileExists(filepath.Join(cwd, "pytest.ini")) ||
		fileExists(filepath.Join(cwd, "pyproject.toml")) ||
		fileExists(filepath.Join(cwd, "setup.py")) ||
		fileExists(filepath.Join(cwd, "tox.ini")) {
		hasTests := dirHasPythonTests(filepath.Join(cwd, "tests")) || dirHasPythonTests(cwd)
		if !hasTests {
			return runner{}, &blockedInfo{
				reason:  "no_tests_found",
				message: "Python 项目检测到，但未找到 test_*.py 或 *_test.py 测试文件。",
				guidance: "如果项目不需要自动化测试，用 bash 直接运行脚本验证；" +
					"如果需要测试，在 tests/ 下创建 pytest 用例。",
				command: "pytest",
			}
		}
		args := []string{}
		display := "pytest"
		if filter != "" {
			safe := sanitizeFilter(filter)
			args = append(args, safe)
			display = "pytest " + safe
		}
		return runner{kind: "pytest", command: "pytest", args: args, display: display}, nil
	}

	// ── 5. Node（package.json 有 test script）──
	if pkg := readPackageJSON(cwd); pkg != nil {
		if _, ok := pkg.Scripts["test"]; ok {
			args := []string{"test"}
			display := "npm test"
			if filter != "" {
				args = append(args, "--", sanitizeFilter(filter))
				display = "npm test -- " + sanitizeFilter(filter)
			}
			return runner{kind: "npm", command: "npm", args: args, display: display}, nil
		}
	}

	// ── 6. 无法探测 ──
	return runner{}, &blockedInfo{
		reason: "no_test_framework",
		message: "无法自动推断测试命令——未找到 go.mod / Cargo.toml / pytest 标记 / " +
			"package.json 的 test script。",
		guidance: "项目缺少可自动检测的测试命令。可在 .rivet-config.json 声明 " +
			`{"verify":{"test":"<命令>"}}——声明后 run_tests 直接使用它；` +
			"也可以绕过自动检测，直接用 bash 运行验证命令。",
		command: "(auto-detect tests)",
	}
}

// parsedTest 是解析出的测试结果。
type parsedTest struct {
	passed   int
	failed   int
	skipped  int
	failures []string
}

// parseTestOutput 按运行器格式解析输出。
func parseTestOutput(raw, kind string) parsedTest {
	clean := stripAnsi(raw)
	var out parsedTest

	switch kind {
	case "go":
		// go test -v: "--- FAIL: TestX" / "--- PASS: TestX"
		// go test（非 -v）: "ok <pkg> 0.5s" / "FAIL <pkg> 0.3s"
		fails := reGoFail.FindAllStringSubmatch(clean, -1)
		passes := reGoPass.FindAllStringSubmatch(clean, -1)
		oks := reGoOk.FindAllString(clean, -1)
		out.failed = len(fails)
		out.passed = len(passes)
		if out.passed == 0 && out.failed == 0 {
			// 非 -v 模式：包级粒度
			out.passed = len(oks)
		}
		for _, m := range fails {
			if len(m) > 1 {
				out.failures = append(out.failures, m[1])
			}
		}
	case "cargo":
		// "test result: ok. 12 passed; 0 failed; 1 ignored; ..."
		for _, m := range reCargoResult.FindAllStringSubmatch(clean, -1) {
			out.passed += atoiSafe(m[1])
			out.failed += atoiSafe(m[2])
			if len(m) > 3 {
				out.skipped += atoiSafe(m[3])
			}
		}
	case "pytest":
		// "===== 3 passed, 1 failed in 0.5s ====="
		if m := rePytestSummary.FindStringSubmatch(clean); len(m) > 0 {
			out.passed = atoiMatch(m, `(\d+)\s+passed`)
			out.failed = atoiMatch(m, `(\d+)\s+failed`)
			out.skipped = atoiMatch(m, `(\d+)\s+skipped`)
		}
	case "npm", "vitest", "jest":
		// "Tests  3 passed | 1 failed (4)" 或 "Tests: 3 passed, 1 failed, 4 total"
		if m := reNodeTestsLine.FindStringSubmatch(clean); len(m) > 1 {
			s := m[1]
			out.passed = atoiSub(s, `(\d+)\s+passed`)
			out.failed = atoiSub(s, `(\d+)\s+failed`)
			out.skipped = atoiSub(s, `(\d+)\s+skipped`)
		}
		// node:test 风格 "# pass 3" / "# fail 1"
		out.passed = maxInt(out.passed, atoiSub(clean, `[ℹ#]\s+pass\s+(\d+)`))
		out.failed = maxInt(out.failed, atoiSub(clean, `[ℹ#]\s+fail\s+(\d+)`))
		out.skipped = maxInt(out.skipped, atoiSub(clean, `[ℹ#]\s+skip\s+(\d+)`))
	case "declared":
		// 通用兜底：依次尝试各格式
		goOut := parseTestOutput(raw, "go")
		if goOut.passed > 0 || goOut.failed > 0 {
			return goOut
		}
		cargoOut := parseTestOutput(raw, "cargo")
		if cargoOut.passed > 0 || cargoOut.failed > 0 {
			return cargoOut
		}
		pyOut := parseTestOutput(raw, "pytest")
		if pyOut.passed > 0 || pyOut.failed > 0 {
			return pyOut
		}
		nodeOut := parseTestOutput(raw, "npm")
		return nodeOut
	}

	// 失败详情行（跨运行器通用）
	for _, m := range reFailLine.FindAllStringSubmatch(clean, -1) {
		if len(m) > 1 {
			name := strings.TrimSpace(m[1])
			if name != "" {
				out.failures = append(out.failures, name)
			}
		}
	}
	return out
}

var (
	reGoFail        = regexp.MustCompile(`(?m)^--- FAIL: (\S+)`)
	reGoPass        = regexp.MustCompile(`(?m)^--- PASS: (\S+)`)
	reGoOk          = regexp.MustCompile(`(?m)^ok\s+\S+`)
	reCargoResult   = regexp.MustCompile(`test result:\s+\w+\.\s+(\d+)\s+passed;\s+(\d+)\s+failed(?:;\s+(\d+)\s+ignored)?`)
	rePytestSummary = regexp.MustCompile(`={2,}\s*([^=]*?(?:passed|failed|error)[^=]*?)\s+in\s+[\d.]+s`)
	reNodeTestsLine = regexp.MustCompile(`(?m)^Tests[:\s]+(.*)$`)
	reFailLine      = regexp.MustCompile(`(?m)^\s*(?:✖|FAIL:?)\s+(.+?)(?:\s+\([\d.]+m?s\))?$`)
	reAnsi          = regexp.MustCompile(`\x1b\[[0-9;]*m`)
)

func atoiMatch(m []string, pattern string) int {
	for _, s := range m {
		if v := atoiSub(s, pattern); v > 0 {
			return v
		}
	}
	return 0
}

func atoiSub(s, pattern string) int {
	re := regexp.MustCompile(pattern)
	if m := re.FindStringSubmatch(s); len(m) > 1 {
		return atoiSafe(m[1])
	}
	return 0
}

func atoiSafe(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// stripAnsi 去掉 ANSI 转义序列（颜色、光标移动）。
func stripAnsi(s string) string { return reAnsi.ReplaceAllString(s, "") }

// formatTestResult 组装人类可读的结果。
func formatTestResult(display string, status contract.VerificationStatus, exitCode int, p parsedTest, raw string, d time.Duration) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "命令: %s\n", display)
	fmt.Fprintf(&sb, "结果: %s（exit %d，耗时 %v）\n", status, exitCode, d.Round(time.Millisecond))
	fmt.Fprintf(&sb, "计数: passed=%d failed=%d skipped=%d\n", p.passed, p.failed, p.skipped)

	if status == contract.VerificationFailed {
		sb.WriteString("\n失败的测试：\n")
		if len(p.failures) > 0 {
			for _, f := range p.failures {
				fmt.Fprintf(&sb, "  - %s\n", f)
			}
		} else {
			sb.WriteString("  （未能解析出具体测试名——见下方原始输出）\n")
		}
		// 关键提示：不能只看计数
		sb.WriteString("\n注意：测试通过 ≠ 类型检查通过。若改了 .go 文件，还需跑 go build/go vet。\n")
	}

	sb.WriteString("\n--- 输出 ---\n")
	sb.WriteString(truncate(stripAnsi(raw), 8000))
	return sb.String()
}

// truncate 截断过长输出。
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n\n[输出已截断，原长 %d 字节]", len(s))
}

// ── 文件与配置读取辅助 ──

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// packageJSON 是 package.json 的最小结构。
type packageJSON struct {
	Scripts map[string]string `json:"scripts"`
}

func readPackageJSON(cwd string) *packageJSON {
	data, err := os.ReadFile(filepath.Join(cwd, "package.json"))
	if err != nil {
		return nil
	}
	var pkg packageJSON
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil
	}
	return &pkg
}

// rivetConfig 是 .rivet-config.json 的最小结构。
type rivetConfig struct {
	Verify struct {
		Test string `json:"test"`
	} `json:"verify"`
}

// readDeclaredTestCommand 读取项目声明的 verify.test 命令。
//
// 声明优先于自动探测——用户显式指定的命令是权威。
func readDeclaredTestCommand(cwd string) string {
	for _, name := range []string{".rivet-config.json", ".rivet.json"} {
		data, err := os.ReadFile(filepath.Join(cwd, name))
		if err != nil {
			continue
		}
		var cfg rivetConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			continue
		}
		if cmd := strings.TrimSpace(cfg.Verify.Test); cmd != "" {
			return cmd
		}
	}
	return ""
}

// dirHasPythonTests 报告目录下是否有 Python 测试文件。
func dirHasPythonTests(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasPrefix(n, "test_") && strings.HasSuffix(n, ".py") {
			return true
		}
		if strings.HasSuffix(n, "_test.py") {
			return true
		}
	}
	return false
}

// sanitizeFilter 去除 filter 中的 shell 元字符（防注入）。
func sanitizeFilter(f string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '`', '$', '\\', ';', '"', '\'', '|', '&', '<', '>':
			return -1
		}
		return r
	}, f)
}

// buildCmd 构造可执行命令。
func buildCmd(ctx context.Context, r runner) *exec.Cmd {
	if r.shell {
		return exec.Command("bash", "-c", r.command)
	}
	return exec.Command(r.command, r.args...)
}

// errorsAs 是 errors.As 的薄封装（保持 import 简洁）。
func errorsAs(err error, target **exec.ExitError) bool {
	if e, ok := err.(*exec.ExitError); ok {
		*target = e
		return true
	}
	return false
}
