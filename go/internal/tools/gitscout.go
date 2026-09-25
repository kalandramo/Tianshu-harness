// gitscout.go —— git_scout：只读 git 史实侦察工具。
//
// 对账 TS `src/tools/git-scout.ts`（331 行）。
//
// # 为什么需要它（TS 文件头的归因，主线同样成立）
//
// readonly profile（code_scout / doc_scout / reviewer / architect）的工具集里
// **没有 bash、也没有 git**——主线的 `git` 工具含写动作（commit / stash /
// stash_pop），整把交给 readonly 会破坏子代理隔离信任链（`WRITE_CAPABLE_TOOLS`
// 把 `git` 算写权）；而 `read_file` 硬拒 `.git/`（DEFAULT_IGNORE 含 `.git`）。
// 结果：侦察 worker 完全无法做 git 史实取证（log / tag / branch / merge-base /
// rev-list），只能退到 web 旁证——而「这行代码是什么时候、为什么变成这样的」
// 恰恰只能从 git 历史回答。
//
// 本工具是纯只读动作子集：所有 action 只跑 git 的查询命令，schema 层就没有写
// 动作；execute 对写动作防御性拒绝。工具名**不进** `WRITE_CAPABLE_TOOLS` /
// `FILE_EDIT_TOOLS`，故 readonly / plan-mode 语义判定不受影响
// （`RequiresApproval: false`）。
//
// # 与 git.go 的关系
//
// 复用同包的 `SpawnGit`（环境消毒 + 可执行路径发现）与 `watchAndKillOnCancel`
// （树杀：git 会拉起 pager / fsmonitor 子进程）。**不 import** 含写动作的
// git 工具模块——隔离边界更干净（对账 TS 的同一取舍）。
package tools

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/contract"
)

// gitScoutActions 对账 TS `ACTIONS`（git-scout.ts:29-39）——**顺序即 schema 的 enum 序**。
var gitScoutActions = []string{
	"log",        // 提交历史（compact：%h|%ad|%s，带日期），支持 range/path/maxCount
	"count",      // 提交数（rev-list --count）
	"show",       // 单提交详情
	"tags",       // tag 列表（版本自然序）
	"branches",   // 分支列表，可过滤 contains
	"ancestry",   // a 是否为 b 的祖先
	"merge_base", // 分叉点
	"resolve",    // ref → commit sha
	"diff",       // --stat 差异
}

// 对账 TS 的常量（git-scout.ts:41-47）。
const (
	gitScoutMaxLog    = 1000
	gitScoutMaxOutput = 50_000
	gitScoutMaxRefLen = 200
)

// gitScoutTimeout 对账 TS `runGit` 的默认 `timeoutMs = 15_000`（git-scout.ts:57）。
//
// **与 git.go 的 `gitTimeout`(10s) 不同**——两处是独立常量，不要合并。
const gitScoutTimeout = 15 * time.Second

// gitScoutBadRef 是参数防注入正则（对账 TS `BAD_REF`，git-scout.ts:52）。
//
// 拒以 '-' 开头的值（防 flag 注入）与 shell 元字符（防御纵深——虽走参数数组
// 无 shell，仍显式拒绝）。
//
// **注意不要把 `~` / `^` / `:` 加进来**：`HEAD~1`、`main..HEAD`、`a:b` 都是合法
// rev 语法，拒了会让 ancestry/merge_base/range 全线不可用（TS 注释记录本批实测
// 踩过）。故 Go 侧逐字复刻 TS 的字符类，不"顺手"扩展。
//
// TS 是 `/^-|[\s"'` + "`" + `$&;|<>]/`。**JS 的 `\s` ≠ Go 的 `\s`**（JS 含 `\v`、
// NBSP、U+FEFF 等 Unicode 空白）——故显式展开 JS 的 \s 字符类，避免边界字节
// 差异（本项目已记录同类坑：summarize 的 jsWS 常量）。
var gitScoutBadRef = regexp.MustCompile("^-|[\t\n\v\f\r \u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000\ufeff\"'`$&;|<>]")

// gitScoutTool 实现 git_scout。
type gitScoutTool struct {
	def contract.Definition
}

// GitScout 构造 git_scout 工具。
func GitScout() Tool { return &gitScoutTool{} }

func (t *gitScoutTool) Definition() contract.Definition {
	if t.def.Name == "" {
		t.def = contract.Definition{
			Name: "git_scout",
			Description: "只读 git 史实侦察（不修改仓库任何状态）。Actions:\n" +
				"- log: 提交历史，每行 `<短sha>|<月-日 时:分>|<主题>`（range 如 \"A..B\" 只列 B 可达而 A 不可达的提交；path 限定文件；maxCount 默认 50 上限 1000）\n" +
				"- count: 提交数（rev-list --count range；缺省 range 数 HEAD 全部历史）\n" +
				"- show: 单提交详情（sha | iso 日期 | refs | subject）\n" +
				"- tags: tag 列表（版本自然序）\n" +
				"- branches: 分支列表；contains 传 ref 时只列包含该提交的分支\n" +
				"- ancestry: 判定 a 是否为 b 的祖先 → YES/NO\n" +
				"- merge_base: a 与 b 的分叉点 sha\n" +
				"- resolve: ref/tag/branch/HEAD → 40-hex commit sha\n" +
				"- diff: a 与 b 间的 --stat（缺省 a/b 时对工作树）\n" +
				"\n纯查询：无 commit/checkout/reset/stash 等任何写动作。",
			// 属性声明序**逐字对账 TS**（git-scout.ts:167-205）——键序不同会
			// 让工具定义变化打掉整个前缀缓存（src/api/openai-client.ts:630）。
			//
			// **未纳入 toolschema oracle**（欠账）：该 oracle 的 PORTED 列表是
			// TS 侧显式枚举，新增工具需改 gen-oracle.ts 并重跑（需 tsx，本机
			// node_modules 为空）。故此处以**显式断言测试**锁定键序与必填项
			// （见 gitscout_test.go 的 TestGitScoutSchemaShape）。
			InputSchema: objSchemaOrdered([]string{
				"action", "range", "ref", "commit", "a", "b", "path", "contains", "maxCount",
			}, map[string]any{
				"action":   enumPropOrdered("要执行的只读 git 操作", gitScoutActions),
				"range":    strProp("提交区间（如 \"A..B\" 或 \"HEAD\"；log/count 用）"),
				"ref":      strProp("ref 名/tag/sha（resolve/branches.contains 用）"),
				"commit":   strProp("commit ref（show 用，默认 HEAD）"),
				"a":        strProp("diff/ancestry/merge_base 的左侧 ref"),
				"b":        strProp("diff/ancestry/merge_base 的右侧 ref（diff 缺省对工作树）"),
				"path":     strProp("限定 log/diff 到单个仓库内路径"),
				"contains": strProp("branches 过滤：只列包含该 ref 的分支"),
				"maxCount": numProp("log 最大条数（默认 50，上限 1000）"),
			}, "action"),
		}
	}
	return t.def
}

// gitScoutResult 是一次 git 调用的结果。
type gitScoutResult struct {
	code   int
	stdout string
	stderr string
}

// gitScoutRun 跑一条 git 读命令并收集输出。
//
// 对账 TS `runGit`（git-scout.ts:56-88）。**三个必须复刻的细节**：
//
//  1. **树杀两段式**：git 会拉起 pager / fsmonitor 子进程，只杀父进程会留孤儿。
//     TS 是 SIGTERM → 3s 后 SIGKILL；Go 用 `watchAndKillOnCancel`（与 git.go 同纪律）。
//  2. **超时返回码 124**（对账 TS 的 `finish(124)`）；中止返回 130。
//  3. **stdout / stderr 都 trim**（TS 的 `.trim()`）。
func gitScoutRun(args []string, cwd string, parent context.Context) gitScoutResult {
	cmd := SpawnGit(args, cwd)

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return gitScoutResult{code: 127, stderr: err.Error()}
	}

	runCtx, cancel := context.WithTimeout(parent, gitScoutTimeout)
	defer cancel()
	signalDone := watchAndKillOnCancel(runCtx, cmd)

	waitErr := cmd.Wait()
	signalDone()

	if runCtx.Err() == context.DeadlineExceeded {
		return gitScoutResult{code: 124, stdout: strings.TrimSpace(stdout.String()), stderr: strings.TrimSpace(stderr.String())}
	}
	if runCtx.Err() == context.Canceled {
		return gitScoutResult{code: 130, stdout: strings.TrimSpace(stdout.String()), stderr: strings.TrimSpace(stderr.String())}
	}

	code := 0
	if waitErr != nil {
		code = 1
		if ee, ok := waitErr.(interface{ ExitCode() int }); ok {
			code = ee.ExitCode()
		}
	}
	return gitScoutResult{
		code:   code,
		stdout: strings.TrimSpace(stdout.String()),
		stderr: strings.TrimSpace(stderr.String()),
	}
}

// gitScoutClip 把输出截断到上限（对账 TS `clip`，git-scout.ts:94-95）。
func gitScoutClip(text string) string {
	if len(text) <= gitScoutMaxOutput {
		return text
	}
	return text[:gitScoutMaxOutput] + "\n…(输出已截断到 " + strconv.Itoa(gitScoutMaxOutput) + " 字符)"
}

// gitScoutSafe 成功（exit 0）返回 stdout；失败返回 stderr（供模型自纠）。
//
// 对账 TS `runGitSafe`（git-scout.ts:91-101）。
func gitScoutSafe(args []string, cwd string, ctx context.Context) (string, bool) {
	r := gitScoutRun(args, cwd, ctx)
	if r.code == 0 {
		return gitScoutClip(r.stdout), true
	}
	msg := r.stderr
	if msg == "" {
		msg = "git " + args[0] + " 退出码 " + strconv.Itoa(r.code)
	}
	return gitScoutClip(msg), false
}

// gitScoutValidateRef 校验 ref（对账 TS `validateRef`，git-scout.ts:108-113）。
//
// 返回非空串 = 错误消息。
func gitScoutValidateRef(name, label string) string {
	if name == "" || len(name) > gitScoutMaxRefLen || gitScoutBadRef.MatchString(name) {
		return label + "非法：必须是 ref 名/tag/commit sha（不以 - 开头、不含空白与 shell 元字符）"
	}
	return ""
}

// gitScoutValidateMaxCount 校验 maxCount（对账 TS `validateMaxCount`）。
//
// 返回 ok=false 表示非法（TS 用 null）。
func gitScoutValidateMaxCount(input map[string]any) (int, bool) {
	v, present := input["maxCount"]
	if !present || v == nil {
		return 50, true
	}
	// TS 的 `typeof value !== 'number'` —— JSON 数字在 Go 里是 float64。
	f, ok := v.(float64)
	if !ok {
		return 0, false
	}
	// TS 的 `Number.isInteger` —— 拒小数。
	if f != float64(int(f)) {
		return 0, false
	}
	n := int(f)
	if n < 1 || n > gitScoutMaxLog {
		return 0, false
	}
	return n, true
}

// gitScoutValidatePathArg 宽松校验 path（对账 TS `validatePathArg`）。
//
// path 总在参数数组的 `--` 分隔之后传给 git——无 flag 注入、无 shell 注入面
// （不走 shell）；git 对仓库外 pathspec 自身报错不越界。故只拒空串与超长，
// 空格/&/;/引号等合法文件名字符一概放行。
func gitScoutValidatePathArg(path string) string {
	if path == "" || len(path) > gitScoutMaxRefLen {
		return "path 非法：必须是仓库内相对路径"
	}
	return ""
}

// gitScoutStrInput 取字符串型入参（缺失或非字符串时返回空串）。
func gitScoutStrInput(input map[string]any, key string) string {
	s, _ := input[key].(string)
	return s
}

func (t *gitScoutTool) Execute(ctx context.Context, p *CallParams) (contract.Result, error) {
	input := p.Input
	action := gitScoutStrInput(input, "action")
	cwd := p.Cwd

	// 写动作防御性拒绝（schema 层已无写动作，此处纵深）。
	if !containsString(gitScoutActions, action) {
		return contract.Result{
			Content: `git_scout 是只读侦察工具，不支持 action "` + action + `"。支持：` +
				strings.Join(gitScoutActions, ", ") + `。写操作请走主控的 git/bash 工具。`,
			IsError: true,
		}, nil
	}

	switch action {
	case "log":
		maxCount, ok := gitScoutValidateMaxCount(input)
		if !ok {
			return contract.Result{Content: "maxCount 必须是 1-1000 的整数", IsError: true}, nil
		}
		rangeVal := gitScoutStrInput(input, "range")
		if rangeVal != "" {
			if err := gitScoutValidateRef(rangeVal, "range"); err != "" {
				return contract.Result{Content: err, IsError: true}, nil
			}
		}
		pathVal := gitScoutStrInput(input, "path")
		if pathVal != "" {
			if err := gitScoutValidatePathArg(pathVal); err != "" {
				return contract.Result{Content: err, IsError: true}, nil
			}
		}
		args := []string{"log", "-n", strconv.Itoa(maxCount), "--date=format:%m-%d %H:%M", "--pretty=format:%h|%ad|%s"}
		if rangeVal != "" {
			args = append(args, rangeVal)
		}
		if pathVal != "" {
			args = append(args, "--", pathVal)
		}
		out, ok2 := gitScoutSafe(args, cwd, ctx)
		if !ok2 {
			return contract.Result{Content: out, IsError: true}, nil
		}
		if out == "" {
			out = "(空——该区间/路径无提交)"
		}
		return contract.Result{Content: out}, nil

	case "count":
		rangeVal := gitScoutStrInput(input, "range")
		if rangeVal != "" {
			if err := gitScoutValidateRef(rangeVal, "range"); err != "" {
				return contract.Result{Content: err, IsError: true}, nil
			}
		}
		args := []string{"rev-list", "--count", "HEAD"}
		if rangeVal != "" {
			args = []string{"rev-list", "--count", rangeVal}
		}
		out, ok := gitScoutSafe(args, cwd, ctx)
		if !ok {
			return contract.Result{Content: out, IsError: true}, nil
		}
		return contract.Result{Content: out}, nil

	case "show":
		commit := gitScoutStrInput(input, "commit")
		if commit == "" {
			commit = "HEAD"
		}
		if err := gitScoutValidateRef(commit, "commit"); err != "" {
			return contract.Result{Content: err, IsError: true}, nil
		}
		out, ok := gitScoutSafe(
			[]string{"show", "-s", "--format=%H | %ad | %D | %s", "--date=iso", commit}, cwd, ctx)
		if !ok {
			return contract.Result{Content: out, IsError: true}, nil
		}
		return contract.Result{Content: out}, nil

	case "tags":
		out, ok := gitScoutSafe([]string{"tag", "-l", "--sort=v:refname"}, cwd, ctx)
		if !ok {
			return contract.Result{Content: out, IsError: true}, nil
		}
		if out == "" {
			out = "(无 tag)"
		}
		return contract.Result{Content: out}, nil

	case "branches":
		contains := gitScoutStrInput(input, "contains")
		if contains != "" {
			if err := gitScoutValidateRef(contains, "contains"); err != "" {
				return contract.Result{Content: err, IsError: true}, nil
			}
		}
		args := []string{"branch", "-a"}
		if contains != "" {
			args = []string{"branch", "-a", "--contains", contains}
		}
		out, ok := gitScoutSafe(args, cwd, ctx)
		if !ok {
			return contract.Result{Content: out, IsError: true}, nil
		}
		return contract.Result{Content: out}, nil

	case "ancestry":
		a := gitScoutStrInput(input, "a")
		b := gitScoutStrInput(input, "b")
		if err := gitScoutValidateRef(a, "a"); err != "" {
			return contract.Result{Content: err, IsError: true}, nil
		}
		if err := gitScoutValidateRef(b, "b"); err != "" {
			return contract.Result{Content: err, IsError: true}, nil
		}
		r := gitScoutRun([]string{"merge-base", "--is-ancestor", a, b}, cwd, ctx)
		if r.code == 0 {
			return contract.Result{Content: "YES"}, nil
		}
		if r.code == 1 {
			return contract.Result{Content: "NO"}, nil
		}
		return contract.Result{
			Content: "ancestry 判定失败（exit " + strconv.Itoa(r.code) + "）：" + r.stderr,
			IsError: true,
		}, nil

	case "merge_base":
		a := gitScoutStrInput(input, "a")
		b := gitScoutStrInput(input, "b")
		if err := gitScoutValidateRef(a, "a"); err != "" {
			return contract.Result{Content: err, IsError: true}, nil
		}
		if err := gitScoutValidateRef(b, "b"); err != "" {
			return contract.Result{Content: err, IsError: true}, nil
		}
		out, ok := gitScoutSafe([]string{"merge-base", a, b}, cwd, ctx)
		if !ok {
			if out == "" {
				out = "两 ref 无共同祖先"
			}
			return contract.Result{Content: out, IsError: true}, nil
		}
		return contract.Result{Content: out}, nil

	case "resolve":
		ref := gitScoutStrInput(input, "ref")
		if err := gitScoutValidateRef(ref, "ref"); err != "" {
			return contract.Result{Content: err, IsError: true}, nil
		}
		out, ok := gitScoutSafe([]string{"rev-parse", "--verify", ref + "^{commit}"}, cwd, ctx)
		if !ok {
			return contract.Result{Content: `无法解析 ref "` + ref + `"`, IsError: true}, nil
		}
		return contract.Result{Content: out}, nil

	case "diff":
		a := gitScoutStrInput(input, "a")
		b := gitScoutStrInput(input, "b")
		if a != "" {
			if err := gitScoutValidateRef(a, "a"); err != "" {
				return contract.Result{Content: err, IsError: true}, nil
			}
		}
		if b != "" {
			if err := gitScoutValidateRef(b, "b"); err != "" {
				return contract.Result{Content: err, IsError: true}, nil
			}
		}
		pathVal := gitScoutStrInput(input, "path")
		if pathVal != "" {
			if err := gitScoutValidatePathArg(pathVal); err != "" {
				return contract.Result{Content: err, IsError: true}, nil
			}
		}
		args := []string{"diff", "--stat"}
		if a != "" {
			args = append(args, a)
		}
		if b != "" {
			args = append(args, b)
		}
		if pathVal != "" {
			args = append(args, "--", pathVal)
		}
		out, ok := gitScoutSafe(args, cwd, ctx)
		if !ok {
			return contract.Result{Content: out, IsError: true}, nil
		}
		if out == "" {
			out = "(无差异)"
		}
		return contract.Result{Content: out}, nil
	}

	// switch 全覆盖后的兜底（对账 TS 穷尽检查）。
	return contract.Result{Content: "未知 action：" + action, IsError: true}, nil
}

// 纯只读工具：永不要求审批（无写动作），可并发执行（readonly 并行侦察语义）。
func (t *gitScoutTool) RequiresApproval(*CallParams) bool { return false }
func (t *gitScoutTool) ConcurrencySafe() bool             { return true }
func (t *gitScoutTool) Enabled() bool                     { return true }
func (t *gitScoutTool) Timeout(*CallParams) time.Duration { return gitScoutTimeout + 5*time.Second }
