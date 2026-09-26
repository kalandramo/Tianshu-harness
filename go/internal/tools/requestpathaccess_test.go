package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// requestpathaccess_test.go —— `request_path_access` 工具（第八十一刀）。
//
// 对账 TS `src/tools/request-path-access.ts`（104 行）。
//
// # 它补的缺口
//
// Go 侧门链（`loop.go` 的 pathGrant 门）在**非 skip 档**遇到工作区外路径时
// **直接拒绝**——因为「无提示通道」。模型没有**主动申请**授权的入口，
// 只能让用户自己操作。本工具补上这个入口（TS 侧同样如此：它是
// 「bash / 多路径 / 主动授予」的统一机制）。
//
// # 安全边界（issue #117）
//
// 授权必须有**上界**：`request_path_access(path='/')` 缺省 write 会把本会话
// 写权限放大到全磁盘。故有 `isForbiddenGrantRoot` 黑名单（根/系统目录）
// + 敏感文件检测两道闸。

// ── isForbiddenGrantRoot（纯函数，对账 TS）──────────────────────────────

// TestForbiddenGrantRootPOSIX —— POSIX 根与系统目录被拒。
//
// 对账 TS `FORBIDDEN_GRANT_ROOTS` + `isForbiddenGrantRoot`：
// 尾部斜杠归一后，空串（即 `/`）、盘根 `C:`、黑名单目录都拒。
func TestForbiddenGrantRootPOSIX(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/", true},
		{"//", true},
		{"/etc", true},
		{"/etc/", true},
		{"/usr", true},
		{"/bin", true},
		{"/var", true},
		{"/opt", true},
		{"/root", true},
		{"/home", true},
		{"/dev", true},
		{"/proc", true},
		{"/sys", true},
		{"/boot", true},
		{"/System", true},  // 大小写不敏感（macOS）
		{"/Library", true}, // 同上
		{"/private", true}, // 同上
		// 非黑名单——用户自己的目录树不受影响。
		{"/Users/someone/projects", false},
		{"/tmp/scratch", false},
		{"/etc-fake", false}, // **不得**被 `/etc` 前缀误伤（段边界）
		{"/usr-local", false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := isForbiddenGrantRoot(tc.path); got != tc.want {
				t.Errorf("isForbiddenGrantRoot(%q) = %v，期望 %v", tc.path, got, tc.want)
			}
		})
	}
}

// TestForbiddenGrantRootWindows —— Windows 盘根与系统目录被拒。
//
// **平台无关的纯函数测试**（不门控 `runtime.GOOS`——纯函数在任意平台都可测，
// 这是本仓库的既有纪律：门控是覆盖损失）。
func TestForbiddenGrantRootWindows(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{`C:`, true},
		{`D:`, true},
		{`c:`, true},
		{`C:\Windows`, true},
		{`C:\Windows\`, true},
		{`C:\Program Files`, true},
		{`C:\Program Files (x86)`, true},
		{`C:\Users`, true},
		// 非黑名单。
		{`C:\Users\someone\projects`, false},
		{`D:\code\thing`, false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := isForbiddenGrantRoot(tc.path); got != tc.want {
				t.Errorf("isForbiddenGrantRoot(%q) = %v，期望 %v", tc.path, got, tc.want)
			}
		})
	}
}

// ── 工具 definition ──────────────────────────────────────────────────────

// TestRequestPathAccessDefinitionParity —— definition 逐字对账 TS。
//
// 工具 definition 进系统提示词前缀——必须字节稳定。
func TestRequestPathAccessDefinitionParity(t *testing.T) {
	def := RequestPathAccess(t.TempDir()).Definition()

	if def.Name != "request_path_access" {
		t.Errorf("name 应为 request_path_access，实得 %q", def.Name)
	}
	wantPrefix := "请求用户授权访问当前工作区之外的路径。"
	if !strings.HasPrefix(def.Description, wantPrefix) {
		t.Errorf("description 首行应逐字对账：\n实得 %q", def.Description)
	}
	if def.InputSchema == nil {
		t.Fatal("应有 InputSchema")
	}
	props := def.InputSchema.Properties
	for _, k := range []string{"path", "mode", "remember"} {
		if _, ok := props[k]; !ok {
			t.Errorf("应有 %s 属性", k)
		}
	}
	// required 只有 path。
	if len(def.InputSchema.Required) != 1 || def.InputSchema.Required[0] != "path" {
		t.Errorf("required 应只有 path，实得 %#v", def.InputSchema.Required)
	}
	// 属性声明序对账 TS（前缀缓存依赖键序）。
	wantOrder := []string{"path", "mode", "remember"}
	if len(def.InputSchema.PropOrder) != len(wantOrder) {
		t.Fatalf("PropOrder 应有 %d 项，实得 %#v", len(wantOrder), def.InputSchema.PropOrder)
	}
	for i, w := range wantOrder {
		if def.InputSchema.PropOrder[i] != w {
			t.Errorf("PropOrder[%d] 应为 %q，实得 %q", i, w, def.InputSchema.PropOrder[i])
		}
	}
	// mode 的 enum 对账 + **嵌套键序对账**。
	//
	// **为什么必须校验键序**（第八十一刀审查订正）：`mode` 此前用裸
	// `map[string]any`，`wire.writeValue` 会排序键 → 产出
	// `description → enum → type`，而 TS 是 `type → enum → description`。
	// 键序不同会让工具定义**字节不稳定**（打的是整个前缀）。
	// 而旧版本测试只校验顶层 PropOrder 与 enum **值**，不校验嵌套键序——
	// 那是审查指出的「假信心」。
	mode, ok := props["mode"].(interface{ Marshal() string })
	if !ok {
		t.Fatalf("mode 应是有序结构（enumPropOrdered 的 *wire.OrderedMap），实得 %T", props["mode"])
	}
	modeRaw := mode.Marshal()
	// 键序：type → enum → description（对账 TS）。
	wantModePrefix := `{"type":"string","enum":["read","write"],"description":`
	if !strings.HasPrefix(modeRaw, wantModePrefix) {
		t.Errorf("mode 键序应逐字对账 TS（type→enum→description）：\n实得 %s\n期望前缀 %s", modeRaw, wantModePrefix)
	}
	if !strings.Contains(modeRaw, `"read"`) || !strings.Contains(modeRaw, `"write"`) {
		t.Errorf("mode enum 应含 read/write，实得 %s", modeRaw)
	}
}

// TestRequestPathAccessRequiresApproval —— 恒需审批（对账 TS `() => true`）。
//
// **为什么**：它做的正是「扩大权限边界」——必须走审批回合。
func TestRequestPathAccessRequiresApproval(t *testing.T) {
	tool := RequestPathAccess(t.TempDir())
	if !tool.RequiresApproval(nil) {
		t.Error("request_path_access 应恒需审批（对账 TS requiresApproval: () => true）")
	}
	if tool.ConcurrencySafe() {
		t.Error("不应并发安全（对账 TS isConcurrencySafe: () => false）")
	}
	if !tool.Enabled() {
		t.Error("应 enabled")
	}
}

// ── 工具 execute：拒绝分支 ───────────────────────────────────────────────

// TestRequestPathAccessMissingPath —— path 缺失报错。
//
// 对账 TS：`'错误：path 必填'`。
func TestRequestPathAccessMissingPath(t *testing.T) {
	for _, input := range []map[string]any{{}, {"path": ""}, {"path": "   "}, {"path": 123}} {
		res, err := RequestPathAccess(t.TempDir()).Execute(context.Background(), &CallParams{Input: input})
		if err != nil {
			t.Fatalf("不应返回 error：%v", err)
		}
		if !res.IsError {
			t.Errorf("input=%#v 应 isError", input)
		}
		if res.Content != "错误：path 必填" {
			t.Errorf("文案应逐字对账：实得 %q", res.Content)
		}
	}
}

// TestRequestPathAccessRejectsForbiddenRoot —— 系统目录/根被拒。
//
// 对账 TS 的 `isForbiddenGrantRoot(target) || isForbiddenGrantRoot(root)` 分支。
//
// **这是 issue #117 的核心防线**：没有它，`path='/'` + 缺省 write
// 会把写权限放大到全磁盘。
func TestRequestPathAccessRejectsForbiddenRoot(t *testing.T) {
	cases := []string{"/etc", "/usr", "/var", "/etc/passwd"}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			res, _ := RequestPathAccess(t.TempDir()).Execute(context.Background(), &CallParams{
				Input: map[string]any{"path": p, "mode": "write"},
			})
			if !res.IsError {
				t.Fatalf("%s 应被拒（系统目录），实得：%q", p, res.Content)
			}
			if !strings.Contains(res.Content, "拒绝授权") {
				t.Errorf("应说明拒绝原因，实得：%q", res.Content)
			}
		})
	}
}

// TestRequestPathAccessRejectsSensitiveFile —— 敏感文件被拒。
//
// 对账 TS 的 `detectSensitiveFile(target)` 分支。**授权根查 target 本身**
// （`/home/u/.ssh/id_rsa` 的根是 `/home/u/.ssh`，但敏感检测查的是 target）。
func TestRequestPathAccessRejectsSensitiveFile(t *testing.T) {
	// `.ssh/id_rsa` 与 `.env` 是本仓库 `DetectSensitiveFile` 的已知模式。
	cases := []string{
		filepath.Join(t.TempDir(), ".ssh", "id_rsa"),
		filepath.Join(t.TempDir(), ".env"),
	}
	for _, p := range cases {
		t.Run(filepath.Base(p), func(t *testing.T) {
			res, _ := RequestPathAccess(t.TempDir()).Execute(context.Background(), &CallParams{
				Input: map[string]any{"path": p},
			})
			if !res.IsError {
				t.Fatalf("%s 应被拒（敏感文件），实得：%q", p, res.Content)
			}
			if !strings.Contains(res.Content, "敏感") {
				t.Errorf("应说明是敏感文件，实得：%q", res.Content)
			}
		})
	}
}

// ── 工具 execute：授予分支 ───────────────────────────────────────────────

// TestRequestPathAccessGrantsDirectorySubtree —— 目录授权其子树。
//
// 对账 TS：`root = target`（是目录时）；`grantPath(root, mode, {cwd})`。
func TestRequestPathAccessGrantsDirectorySubtree(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "outside")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	granted := &fakeGranter{}
	tool := RequestPathAccess(dir)
	// 注入 grant 能力（工具经 CallParams 拿到）。
	res, _ := tool.Execute(context.Background(), &CallParams{
		Input:     map[string]any{"path": target, "mode": "write"},
		Cwd:       dir,
		GrantPath: granted.grant,
	})
	if res.IsError {
		t.Fatalf("应成功授权，实得：%q", res.Content)
	}
	if granted.root != target {
		t.Errorf("应授权该目录本身，实得 root=%q", granted.root)
	}
	if granted.mode != GrantWrite {
		t.Errorf("mode 应为 write，实得 %q", granted.mode)
	}
	if granted.cwd != dir {
		t.Errorf("cwd 应绑定会话工作区，实得 %q", granted.cwd)
	}
	if !strings.Contains(res.Content, "已授予 write 访问") {
		t.Errorf("成功文案应逐字对账，实得：%q", res.Content)
	}
	if !strings.Contains(res.Content, "仅本会话") {
		t.Errorf("未持久化时应说明「仅本会话」，实得：%q", res.Content)
	}
}

// TestRequestPathAccessGrantsParentForFile —— 文件授权其父目录。
//
// 对账 TS：target 不是目录时 `root = dirname(target)`——授权父目录使
// 该文件与其兄弟可达。
func TestRequestPathAccessGrantsParentForFile(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "note.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	granted := &fakeGranter{}
	res, _ := RequestPathAccess(dir).Execute(context.Background(), &CallParams{
		Input:     map[string]any{"path": target},
		Cwd:       dir,
		GrantPath: granted.grant,
	})
	if res.IsError {
		t.Fatalf("应成功授权，实得：%q", res.Content)
	}
	if granted.root != outside {
		t.Errorf("文件的授权根应是父目录 %q，实得 %q", outside, granted.root)
	}
	// 缺省 mode 是 read（最小权限，issue #117）。
	if granted.mode != GrantRead {
		t.Errorf("缺省 mode 应为 read（最小权限），实得 %q", granted.mode)
	}
}

// TestRequestPathAccessNonexistentPathUsesParent —— 不存在的路径授权其父目录。
//
// 对账 TS：`existsSync(target) && statSync(target).isDirectory()` 为假时
// `root = dirname(target)`——覆盖「将要创建的文件」。
func TestRequestPathAccessNonexistentPathUsesParent(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "will-be-created.txt") // 不存在

	granted := &fakeGranter{}
	res, _ := RequestPathAccess(dir).Execute(context.Background(), &CallParams{
		Input:     map[string]any{"path": target},
		Cwd:       dir,
		GrantPath: granted.grant,
	})
	if res.IsError {
		t.Fatalf("应成功授权，实得：%q", res.Content)
	}
	if granted.root != outside {
		t.Errorf("不存在的路径应授权其父目录 %q，实得 %q", outside, granted.root)
	}
}

// TestRequestPathAccessRememberDowngrades —— remember=true 在无持久化时降级为会话级 + 明示。
//
// **这是 Go 侧的诚实降级**：TS 的 `grantPath(..., {persist: remember})` 支持
// 持久化，Go 侧 `GrantPath` **无 persist 参数**（持久化未移植，见
// `pathgrants.go:18-23` 的声明）。故 remember=true 时**不能**谎称「已持久化」。
func TestRequestPathAccessRememberDowngrades(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}

	granted := &fakeGranter{}
	res, _ := RequestPathAccess(dir).Execute(context.Background(), &CallParams{
		Input:     map[string]any{"path": outside, "remember": true},
		Cwd:       dir,
		GrantPath: granted.grant,
	})
	if res.IsError {
		t.Fatalf("应成功授权，实得：%q", res.Content)
	}
	// **不得**谎称已持久化。
	if strings.Contains(res.Content, "已为本工作区持久化") {
		t.Errorf("Go 侧持久化未移植，不得声称已持久化，实得：%q", res.Content)
	}
	if !strings.Contains(res.Content, "仅本会话") {
		t.Errorf("应明示降级为会话级，实得：%q", res.Content)
	}
}

// TestRequestPathAccessTildeExpansion —— `~` 展开为 home。
//
// 对账 TS：`resolve(expandHome(raw.trim()))`。
func TestRequestPathAccessTildeExpansion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("`~` 展开语义在 Windows 上由 expandHome 的平台分支决定，此处只验 POSIX")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("无法取 home")
	}
	// `~/nonexistent-probe-dir/x` → 展开后其父是 home（不是字面 `~`）。
	// 注意：这里只断言**展开发生了**（root 不含字面 `~`），不断言具体授权成功
	// ——home 本身可能命中敏感检测或被拒。
	granted := &fakeGranter{}
	res, _ := RequestPathAccess(t.TempDir()).Execute(context.Background(), &CallParams{
		Input:     map[string]any{"path": "~/nonexistent-probe-dir/x.txt"},
		Cwd:       t.TempDir(),
		GrantPath: granted.grant,
	})
	if res.IsError {
		// 被拒也是可接受结果（若 home 命中敏感/系统目录）——但不得是「path 必填」。
		if strings.Contains(res.Content, "path 必填") {
			t.Fatalf("`~` 应被接受（展开后非空），实得：%q", res.Content)
		}
		return
	}
	if strings.Contains(granted.root, "~") {
		t.Errorf("`~` 应已展开，实得 root=%q", granted.root)
	}
	if !strings.HasPrefix(granted.root, home) {
		t.Errorf("展开后应位于 home 之下，实得 root=%q home=%q", granted.root, home)
	}
}

// TestRequestPathAccessNoGranterGraceful —— 无 grant 能力时不 panic（降级提示）。
func TestRequestPathAccessNoGranterGraceful(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := RequestPathAccess(dir).Execute(context.Background(), &CallParams{
		Input: map[string]any{"path": outside},
		Cwd:   dir,
		// GrantPath 为 nil
	})
	if err != nil {
		t.Fatalf("不应返回 error：%v", err)
	}
	if !res.IsError {
		t.Error("无 grant 能力时应 isError（不能假装授权成功）")
	}
	if !strings.Contains(res.Content, "不可用") {
		t.Errorf("应说明授权能力不可用，实得：%q", res.Content)
	}
}

// fakeGranter 记录 GrantPath 回调的调用（测试缝）。
//
// **为什么是回调而非接口对象**：`tools` 包**不得** import `internal/agent`
// （`agent` 已依赖 `tools`，反向会成 import 环）。本仓库既有的跨包解耦模式
// 就是 `CallParams` 上的 `func` 回调字段（见 `EnterPlanMode`/`ExitPlanMode`）。
type fakeGranter struct {
	root string
	mode GrantMode
	cwd  string
	n    int
}

func (f *fakeGranter) grant(root string, mode GrantMode, cwd string) {
	f.root, f.mode, f.cwd = root, mode, cwd
	f.n++
}
