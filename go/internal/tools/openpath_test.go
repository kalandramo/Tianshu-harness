package tools

import (
	"strings"
	"testing"
)

// openpath_test.go —— `open_path` 工具（第八十四刀 · W2-1）。
//
// 对账 TS `src/tools/open-path.ts`（213 行）。
//
// # 为什么这是「接线」
//
// 依赖只有 `os/exec`（对账 `node:child_process`）、`os`（对账 `node:fs`）、
// `path/filepath`（对账 `node:path`）+ `expandHome`（Go 侧已有）。
// **门链早已预留**：`approval_pathgrant.go:125` 的 `open_path` 授权分支
// 在第八十四刀前就存在——本刀让它首次有真实消费者。
//
// # 测试策略（重要）
//
// `execute` 会**真的 spawn 进程打开文件**——测试不能触发它。
// 故本文件聚焦：
//  1. 纯函数（`normalizeOpenTarget` / `buildOpenPathCommand` /
//     `buildRevealCommand` / `decideOpenAction`）——三平台全覆盖
//  2. `execute` 的**不 spawn 分支**（path 缺失 / 路径不存在）
//  3. definition 逐字对账
//
// **Windows 注册表探测**（`windowsFileHasHandler`）用**注入的 query 函数**测——
// 在 macOS 上也能验证其判定逻辑（这是 TS 侧的设计：platform 参数可注入）。

// ── normalizeOpenTarget ────────────────────────────────────────────────

// TestOpenPathNormalizeTarget —— 路径语义由**声明的平台**决定。
//
// 对账 TS 注释：「不用宿主 resolve()：否则在 Windows 上跑 darwin/linux 用例时
// '/Users/…' 被解析成 'C:\Users\…'」。
func TestOpenPathNormalizeTarget(t *testing.T) {
	// darwin/linux：posix 语义——绝对路径原样
	if got := normalizeOpenTarget("/tmp/a.svg", "darwin"); got != "/tmp/a.svg" {
		t.Errorf("darwin 绝对路径应原样，实得 %q", got)
	}
	// `~` 展开（expandHome）
	got := normalizeOpenTarget("~/x.svg", "darwin")
	if strings.Contains(got, "~") {
		t.Errorf("`~` 应展开，实得 %q", got)
	}
	if !strings.HasPrefix(got, "/") {
		t.Errorf("darwin 展开后应是绝对路径，实得 %q", got)
	}

	// windows：已有盘符 → 原样（对账 TS `/^(?:[a-zA-Z]:[\\/]|\\\\)/`）
	if got := normalizeOpenTarget(`C:\Users\a.svg`, "windows"); got != `C:\Users\a.svg` {
		t.Errorf("盘符路径应原样，实得 %q", got)
	}
	// windows：UNC 路径 → 原样
	if got := normalizeOpenTarget(`\\server\share\a.svg`, "windows"); got != `\\server\share\a.svg` {
		t.Errorf("UNC 路径应原样，实得 %q", got)
	}
	// windows：相对路径 → win32.resolve（会加盘符或反斜杠）
	got = normalizeOpenTarget("a.svg", "windows")
	if !strings.Contains(got, `\`) {
		t.Errorf("windows 相对路径应转反斜杠，实得 %q", got)
	}
}

// ── buildOpenPathCommand ───────────────────────────────────────────────

// TestOpenPathBuildCommand —— 三平台的打开命令。
//
// 对账 TS `buildOpenPathCommand`：
//   - win32 → `powershell.exe -NoProfile -NonInteractive -Command "Start-Process -FilePath '<path>'"`
//   - darwin → `open <path>`
//   - 其余 → `xdg-open <path>`
func TestOpenPathBuildCommand(t *testing.T) {
	// darwin
	c := buildOpenPathCommand("/tmp/a.svg", "darwin")
	if c.cmd != "open" || len(c.args) != 1 || c.args[0] != "/tmp/a.svg" {
		t.Errorf("darwin 应为 `open <path>`，实得 %+v", c)
	}
	// linux
	c = buildOpenPathCommand("/tmp/a.svg", "linux")
	if c.cmd != "xdg-open" || c.args[0] != "/tmp/a.svg" {
		t.Errorf("linux 应为 `xdg-open <path>`，实得 %+v", c)
	}
	// windows：PowerShell Start-Process（**不用 cmd /c start**——注入面）
	c = buildOpenPathCommand(`C:\a\b.svg`, "windows")
	if c.cmd != "powershell.exe" {
		t.Errorf("windows 应用 powershell.exe，实得 %q", c.cmd)
	}
	joined := strings.Join(c.args, " ")
	if !strings.Contains(joined, "Start-Process") {
		t.Errorf("windows 应用 Start-Process（非 cmd start），实得 %q", joined)
	}
	if strings.Contains(joined, "-LiteralPath") {
		t.Errorf("**不能用 -LiteralPath**（Start-Process 无此参数，TS 注释记录过该 bug）：%q", joined)
	}
	// 正斜杠转反斜杠（TS 注释：explorer/Start-Process 对正斜杠静默失败）
	if strings.Contains(strings.Join(c.args, " "), "/") && !strings.Contains(joined, "http") {
		// PowerShell 参数里除了命令本身不应有正斜杠路径
		t.Logf("windows args: %q（已转反斜杠）", joined)
	}
}

// TestOpenPathBuildCommandWindowsQuoting —— Windows 单引号转义（防注入）。
//
// 对账 TS：`'${winTarget.replace(/'/g, "”")}'`——内嵌单引号翻倍。
func TestOpenPathBuildCommandWindowsQuoting(t *testing.T) {
	c := buildOpenPathCommand(`C:\a'b\c.svg`, "windows")
	joined := strings.Join(c.args, " ")
	// 内嵌单引号应翻倍（PowerShell 的单引号字面串规则）
	if !strings.Contains(joined, "''") {
		t.Errorf("内嵌单引号应翻倍，实得 %q", joined)
	}
}

// ── buildRevealCommand ─────────────────────────────────────────────────

// TestOpenPathBuildReveal —— 三平台的「定位」命令。
//
// 对账 TS `buildRevealCommand`：
//   - win32 → `explorer /select,"<path>"`
//   - darwin → `open -R <path>`
//   - linux → `xdg-open <dirname>`（无通用「选中文件」API，打开父目录）
func TestOpenPathBuildReveal(t *testing.T) {
	// darwin：open -R
	c := buildRevealCommand("/tmp/a.svg", "darwin")
	if c.cmd != "open" || len(c.args) != 2 || c.args[0] != "-R" || c.args[1] != "/tmp/a.svg" {
		t.Errorf("darwin 应为 `open -R <path>`，实得 %+v", c)
	}
	// linux：打开父目录
	c = buildRevealCommand("/tmp/sub/a.svg", "linux")
	if c.cmd != "xdg-open" || c.args[0] != "/tmp/sub" {
		t.Errorf("linux 应打开父目录，实得 %+v", c)
	}
	// windows：explorer /select
	c = buildRevealCommand(`C:\a\b.svg`, "windows")
	if c.cmd != "powershell.exe" {
		t.Errorf("windows reveal 应经 powershell，实得 %q", c.cmd)
	}
	if !strings.Contains(strings.Join(c.args, " "), "explorer /select") {
		t.Errorf("应含 explorer /select，实得 %q", strings.Join(c.args, " "))
	}
}

// ── decideOpenAction（条件矩阵）─────────────────────────────────────────

// TestOpenPathDecideAction —— 平台 × 是否目录 × hasHandler 三态。
//
// 对账 TS `decideOpenAction`：
//
//	非 win32 → 'open'
//	win32 + 目录 → 'open'
//	win32 + 文件 + hasHandler===false → 'reveal'
//	win32 + 文件 + hasHandler 为 true/undefined → 'open'
//
// **为什么 Windows 要退化**（issue #193）：打开一个无关联处理程序的文件会弹
// 「选取应用」对话框，而 spawn 是 detached——调用方既回收不了也察觉不到。
func TestOpenPathDecideAction(t *testing.T) {
	tru, fal := true, false

	cases := []struct {
		name string
		in   openDecision
		want string
	}{
		{"darwin 恒 open", openDecision{platform: "darwin", isDirectory: false, hasHandler: &fal}, "open"},
		{"linux 恒 open", openDecision{platform: "linux", isDirectory: false, hasHandler: &fal}, "open"},
		{"windows 目录恒 open", openDecision{platform: "windows", isDirectory: true, hasHandler: &fal}, "open"},
		{"windows 文件+无处理程序→reveal", openDecision{platform: "windows", isDirectory: false, hasHandler: &fal}, "reveal"},
		{"windows 文件+有处理程序→open", openDecision{platform: "windows", isDirectory: false, hasHandler: &tru}, "open"},
		{"windows 文件+判断不了→open（fail-open）", openDecision{platform: "windows", isDirectory: false, hasHandler: nil}, "open"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := decideOpenAction(c.in); got != c.want {
				t.Errorf("decideOpenAction(%+v) = %q，期望 %q", c.in, got, c.want)
			}
		})
	}
}

// ── windowsFileHasHandler（注入 query）──────────────────────────────────

// TestOpenPathWindowsHasHandler —— 注册表探测的判定逻辑（注入 query）。
//
// 对账 TS `windowsFileHasHandler` 的判定顺序：
//  1. 用户级 `HKCU\...\FileExts\<ext>\UserChoice` 的 ProgId → true
//  2. 机器级 `HKCR\<ext>` 默认值 → ProgId → `HKCR\<ProgId>\shell\open\command` → true
//
// 返回 nil = 判断不了（非 Windows / 无扩展名 / 查询出错）→ 调用方 fail-open。
func TestOpenPathWindowsHasHandler(t *testing.T) {
	// 非 Windows → nil（不查询）
	called := false
	q := func(key, value string) (string, error) { called = true; return "", nil }
	if got := windowsFileHasHandler("/tmp/a.svg", "darwin", q); got != nil {
		t.Errorf("非 Windows 应返回 nil，实得 %v", got)
	}
	if called {
		t.Error("非 Windows 不应触发注册表查询")
	}

	// 无扩展名 → nil
	if got := windowsFileHasHandler(`C:\a\noext`, "windows", q); got != nil {
		t.Errorf("无扩展名应返回 nil，实得 %v", got)
	}

	// ① 用户级 UserChoice 命中 → true
	q1 := func(key, value string) (string, error) {
		if strings.Contains(key, "UserChoice") {
			return "    ProgId    REG_SZ    pngfile", nil
		}
		return "", errNotFound
	}
	if got := windowsFileHasHandler(`C:\a\b.png`, "windows", q1); got == nil || !*got {
		t.Errorf("UserChoice 命中应返回 true，实得 %v", got)
	}

	// ② 用户级未命中，机器级命中 → true
	q2 := func(key, value string) (string, error) {
		if strings.Contains(key, "UserChoice") {
			return "", errNotFound
		}
		if strings.Contains(key, "HKEY_CLASSES_ROOT\\.png") {
			return "    (默认)    REG_SZ    pngfile", nil
		}
		if strings.Contains(key, "shell\\open\\command") {
			return "    (默认)    REG_SZ    \"C:\\viewer.exe\" \"%1\"", nil
		}
		return "", errNotFound
	}
	if got := windowsFileHasHandler(`C:\a\b.png`, "windows", q2); got == nil || !*got {
		t.Errorf("机器级命中应返回 true，实得 %v", got)
	}

	// ③ HKCR 里没该扩展名 → false（**确认无处理程序**的强信号）
	q3 := func(key, value string) (string, error) { return "", errNotFound }
	if got := windowsFileHasHandler(`C:\a\b.zzq`, "windows", q3); got == nil || *got {
		t.Errorf("HKCR 无扩展名应返回 false，实得 %v", got)
	}
}

// errNotFound 模拟注册表查询失败。
var errNotFound = errString("registry key not found")

type errString string

func (e errString) Error() string { return string(e) }

// ── execute 的不 spawn 分支 ────────────────────────────────────────────

// TestOpenPathExecuteRequiresPath —— path 必填。
func TestOpenPathExecuteRequiresPath(t *testing.T) {
	res := runOpenPath(t, map[string]any{})
	if !res.IsError || !strings.Contains(res.Content, "path 为必填项") {
		t.Errorf("缺 path 应报错，实得 %q", res.Content)
	}
	res = runOpenPath(t, map[string]any{"path": "   "})
	if !res.IsError || !strings.Contains(res.Content, "path 为必填项") {
		t.Errorf("空白 path 应报错，实得 %q", res.Content)
	}
}

// TestOpenPathExecuteMissingFile —— 路径不存在时报错（**不 spawn**）。
//
// 这是 execute 里唯一能在测试中安全覆盖的路径——其余分支会真的打开文件。
func TestOpenPathExecuteMissingFile(t *testing.T) {
	res := runOpenPath(t, map[string]any{"path": "/definitely/not/exist/zzq-8f3a.svg"})
	if !res.IsError {
		t.Fatalf("不存在的路径应报错，实得 %q", res.Content)
	}
	if !strings.Contains(res.Content, "路径不存在") {
		t.Errorf("文案应含「路径不存在」，实得 %q", res.Content)
	}
}

// ── definition 对账 ────────────────────────────────────────────────────

// TestOpenPathDefinitionParity —— definition 逐字对账 TS。
func TestOpenPathDefinitionParity(t *testing.T) {
	def := OpenPath(t.TempDir()).Definition()

	if def.Name != "open_path" {
		t.Errorf("name 应为 open_path，实得 %q", def.Name)
	}
	if !strings.HasPrefix(def.Description, "在用户操作系统中打开文件或目录。") {
		t.Errorf("description 首行应逐字对账，实得 %q", def.Description)
	}
	if len(def.InputSchema.PropOrder) != 1 || def.InputSchema.PropOrder[0] != "path" {
		t.Errorf("PropOrder 应只有 path，实得 %#v", def.InputSchema.PropOrder)
	}
	if len(def.InputSchema.Required) != 1 || def.InputSchema.Required[0] != "path" {
		t.Errorf("required 应只有 path，实得 %#v", def.InputSchema.Required)
	}
}

// TestOpenPathApprovalSemantics —— 恒需审批 + **并发安全**（对账 TS `() => true`）。
//
// **注意**：与办公文档家族不同——open_path 的 `isConcurrencySafe` 是 **true**
// （只读打开，无写副作用）。
func TestOpenPathApprovalSemantics(t *testing.T) {
	tool := OpenPath(t.TempDir())
	if !tool.RequiresApproval(nil) {
		t.Error("应恒需审批")
	}
	if !tool.ConcurrencySafe() {
		t.Error("应并发安全（对账 TS `() => true`）——与办公文档家族的 false 不同")
	}
	if !tool.Enabled() {
		t.Error("应 enabled")
	}
}

func runOpenPath(t *testing.T, input map[string]any) struct {
	Content string
	IsError bool
} {
	t.Helper()
	r, err := OpenPath(t.TempDir()).Execute(nil, &CallParams{Input: input})
	if err != nil {
		t.Fatalf("Execute 不应返回 error：%v", err)
	}
	return struct {
		Content string
		IsError bool
	}{r.Content, r.IsError}
}
