package pathsafe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 测试用授权检查器。
type fakeGrants struct {
	read  []string
	write []string
}

// 真实授权存储会规范化两侧路径（见 pathsafe.go 的授权比对注释）。
func (f *fakeGrants) IsReadGranted(path, cwd string) bool {
	real, _ := filepath.EvalSymlinks(path)
	if real == "" {
		real = path
	}
	for _, p := range f.read {
		rp, _ := filepath.EvalSymlinks(p)
		if rp == "" {
			rp = p
		}
		if strings.HasPrefix(real, rp) {
			return true
		}
	}
	return false
}

func (f *fakeGrants) IsWriteGranted(path, cwd string) bool {
	real, _ := filepath.EvalSymlinks(path)
	if real == "" {
		real = path
	}
	for _, p := range f.write {
		rp, _ := filepath.EvalSymlinks(p)
		if rp == "" {
			rp = p
		}
		if strings.HasPrefix(real, rp) {
			return true
		}
	}
	return false
}

// 工作区内路径应通过。
func TestInsideWorkspaceAllowed(t *testing.T) {
	root := t.TempDir()
	cases := []string{
		"a.ts",
		"src/b.ts",
		"./c.ts",
		"src/../d.ts",
		"deep/nested/dir/e.ts",
	}
	for _, p := range cases {
		r := Validate(root, p, ModeRead, nil)
		if !r.OK {
			t.Errorf("%q 应通过，实际失败：%s", p, r.Error)
		}
	}
}

// 反证 A：路径逃逸必须被拦截（fail-closed）。
func TestEscapeBlocked(t *testing.T) {
	root := t.TempDir()
	cases := []string{
		"../outside.ts",
		"../../etc/passwd",
		"src/../../outside.ts",
		"/etc/passwd",
		"/tmp/other.ts",
	}
	for _, p := range cases {
		r := Validate(root, p, ModeRead, nil)
		if r.OK {
			t.Errorf("%q 是逃逸路径，必须被拦截（实际通过：%s）", p, r.Path)
		}
		if !strings.Contains(r.Error, "outside project directory") {
			t.Errorf("%q 的错误信息应说明越界：%s", p, r.Error)
		}
	}
}

// 反证 B：敏感文件必须在逃逸检查**之前**被拦截，且覆盖各种写法。
//
// 只查裸输入串会让 `scripts/../.env` 绕过——本测试覆盖该攻击面。
func TestSensitiveFilesBlocked(t *testing.T) {
	root := t.TempDir()
	// 构造真实存在的文件以测试 realpath 形态
	mustWrite(t, filepath.Join(root, ".env"), "SECRET=1")
	mustWrite(t, filepath.Join(root, "scripts", "dummy.ts"), "// x")

	cases := []string{
		".env",
		"./.env",
		"scripts/../.env", // 词法 resolve 才现形
		".env/",           // 尾部斜杠
		".env.",           // 尾部点
		".ENV",            // 大小写
		"credentials.json",
		"id_rsa",
		"private_key.pem",
		"secrets.yaml",
		"api_token.txt",
	}
	for _, p := range cases {
		r := Validate(root, p, ModeRead, nil)
		if r.OK {
			t.Errorf("敏感路径 %q 必须被拦截（实际通过）", p)
			continue
		}
		if !strings.Contains(r.Error, "Sensitive file blocked") {
			t.Errorf("%q 应报敏感文件：%s", p, r.Error)
		}
	}
}

// 反证 B2：路径的**规范形**必须被检查，而不仅是裸输入串。
//
// 构造一个裸输入串看起来无害、但 realpath 后落在敏感文件上的路径：
// 项目内有个无害名的符号链接指向 .env。只查裸输入串的实现会放行 → 本测试变红。
func TestSensitiveDetectedInCanonicalForm(t *testing.T) {
	root := t.TempDir()
	envPath := filepath.Join(root, ".env")
	mustWrite(t, envPath, "SECRET=1")

	alias := filepath.Join(root, "config.txt")
	if err := os.Symlink(envPath, alias); err != nil {
		t.Skipf("平台不支持符号链接：%v", err)
	}

	r := Validate(root, "config.txt", ModeRead, nil)
	if r.OK {
		t.Fatal("经符号链接指向 .env 的路径必须被拦截——只查裸输入串会漏掉（裸串 'config.txt' 不含敏感模式）")
	}
	if !strings.Contains(r.Error, "Sensitive file blocked") {
		t.Errorf("应报敏感文件：%s", r.Error)
	}
}

// 反证 B3：经子目录符号链接指向敏感文件的路径。
func TestSensitiveViaSubdirSymlink(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".env"), "K=1")
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, ".env"), filepath.Join(sub, "link")); err != nil {
		t.Skipf("平台不支持符号链接：%v", err)
	}

	r := Validate(root, "sub/link", ModeRead, nil)
	if r.OK {
		t.Fatal("经子目录符号链接指向 .env 必须被拦截")
	}
}

// 敏感文件检查优先于逃逸检查——即使路径越界也要先报敏感。
func TestSensitiveCheckBeforeEscape(t *testing.T) {
	root := t.TempDir()
	r := Validate(root, "../.env", ModeRead, nil)
	if r.OK {
		t.Fatal("应被拦截")
	}
	if !strings.Contains(r.Error, "Sensitive file blocked") {
		t.Errorf("应优先报敏感文件而非逃逸：%s", r.Error)
	}
}

// 反证 C：符号链接逃逸必须被拦截（./evil -> /etc，写 evil/new）。
func TestSymlinkEscapeBlocked(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	// 在项目内建一个指向外部目录的符号链接
	link := filepath.Join(root, "evil")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("平台不支持符号链接：%v", err)
	}

	// 经符号链接写入新文件 → 实际落在项目外，必须拦截
	r := Validate(root, "evil/new.ts", ModeWrite, nil)
	if r.OK {
		t.Errorf("经符号链接的写入逃逸必须被拦截（实际通过：%s）", r.Path)
	}
}

// 经符号链接到达的**合法**项目内路径不应被误判（macOS /var→/private/var 场景）。
func TestSymlinkedRootNotFalsePositive(t *testing.T) {
	realRoot := t.TempDir()
	linkParent := t.TempDir()
	linkedRoot := filepath.Join(linkParent, "link")
	if err := os.Symlink(realRoot, linkedRoot); err != nil {
		t.Skipf("平台不支持符号链接：%v", err)
	}

	// 以符号链接路径作为 cwd，访问其中的文件——应通过
	r := Validate(linkedRoot, "inside.ts", ModeRead, nil)
	if !r.OK {
		t.Errorf("经符号链接根的合法路径被误判为逃逸：%s", r.Error)
	}
}

// 反证 D：授权只扩宽被授权的子树，不得全域放行。
func TestGrantScopedToSubtree(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	granted := filepath.Join(outside, "allowed")
	// 授权目录必须真实存在——EvalSymlinks 对不存在的路径返回空，
	// 会让授权比对回退到未解析形式（/var vs /private/var）而失效。
	if err := os.MkdirAll(granted, 0o755); err != nil {
		t.Fatal(err)
	}

	grants := &fakeGrants{read: []string{granted}}

	// 被授权子树内 → 放行
	r := Validate(root, filepath.Join(granted, "f.ts"), ModeRead, &Options{Grants: grants})
	if !r.OK {
		t.Errorf("已授权子树应放行：%s", r.Error)
	}
	// 同目录树的兄弟路径 → 仍拦截
	sibling := filepath.Join(outside, "other", "f.ts")
	r = Validate(root, sibling, ModeRead, &Options{Grants: grants})
	if r.OK {
		t.Errorf("未授权的兄弟路径必须拦截（实际通过：%s）", r.Path)
	}
}

// 反证 E：读授权不得放行写操作（模式区分）。
func TestReadGrantDoesNotAllowWrite(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	granted := filepath.Join(outside, "dir")
	if err := os.MkdirAll(granted, 0o755); err != nil {
		t.Fatal(err)
	}

	grants := &fakeGrants{read: []string{granted}} // 只有读授权

	r := Validate(root, filepath.Join(granted, "f.ts"), ModeRead, &Options{Grants: grants})
	if !r.OK {
		t.Fatalf("读应放行：%s", r.Error)
	}
	r = Validate(root, filepath.Join(granted, "f.ts"), ModeWrite, &Options{Grants: grants})
	if r.OK {
		t.Error("读授权不得放行写操作")
	}
}

// 新文件（尚不存在）的路径应可校验。
func TestNonExistentFileValidated(t *testing.T) {
	root := t.TempDir()
	r := Validate(root, "new/dir/file.ts", ModeWrite, nil)
	if !r.OK {
		t.Errorf("不存在的新文件应可通过校验：%s", r.Error)
	}
	// 但越界的新文件仍须拦截
	r = Validate(root, "../new.ts", ModeWrite, nil)
	if r.OK {
		t.Error("越界的新文件必须拦截")
	}
}

// 返回路径保持原始 cwd 基准（契约：调用方据此算相对标签）。
func TestReturnedPathIsResolved(t *testing.T) {
	root := t.TempDir()
	r := Validate(root, "sub/file.ts", ModeRead, nil)
	if !r.OK {
		t.Fatalf("应通过：%s", r.Error)
	}
	if !filepath.IsAbs(r.Path) {
		t.Errorf("返回路径应为绝对路径：%s", r.Path)
	}
	if !strings.HasSuffix(r.Path, filepath.Join("sub", "file.ts")) {
		t.Errorf("返回路径错误：%s", r.Path)
	}
}

// MustValidate 在校验失败时返回错误。
func TestMustValidateError(t *testing.T) {
	root := t.TempDir()
	if _, err := MustValidate(root, "../x.ts", ModeRead, nil); err == nil {
		t.Error("越界应返回错误")
	}
	if _, err := MustValidate(root, "x.ts", ModeRead, nil); err != nil {
		t.Errorf("区内应无错误：%v", err)
	}
}

// 敏感文件检测器单测：覆盖归一化边界。
func TestSensitiveDetectorNormalization(t *testing.T) {
	d := defaultSensitiveDetector{}
	sensitive := []string{
		".env", ".env.local", ".env/", ".env.", ".ENV",
		"credentials.json", "my_secret.txt",
		"token.txt", "id_rsa", "cert.pem",
	}
	for _, p := range sensitive {
		if ok, _ := d.Detect(p); !ok {
			t.Errorf("%q 应被判为敏感", p)
		}
	}
	// 不应误报的
	benign := []string{"main.go", "README.md", "notes.md"}
	for _, p := range benign {
		if ok, name := d.Detect(p); ok {
			t.Logf("（提示）%q 被判为敏感（模式 %s）——确认是否为预期", p, name)
		}
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
