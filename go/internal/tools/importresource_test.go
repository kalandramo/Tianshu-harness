package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// importresource_test.go —— Wave 1 纯函数（第一百刀）。
//
// 对账 TS `src/tools/import-resource.ts` 的 `parseGitHubUrl` / `subpathEscapesContainer` /
// `isSafeGitRef` / `simpleHash` / `importTargetName`。

// ── parseGitHubURL ──────────────────────────────────────────────────────

func TestParseGitHubURL(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantOK  bool
		owner   string
		repo    string
		ref     string
		subpath string
	}{
		{"裸域形态", "github.com/user/repo", true, "user", "repo", "", ""},
		{"https 全形", "https://github.com/user/repo", true, "user", "repo", "", ""},
		{"http 全形", "http://github.com/user/repo", true, "user", "repo", "", ""},
		{".git 后缀被剥", "https://github.com/user/repo.git", true, "user", "repo", "", ""},
		{"tree 段", "https://github.com/user/repo/tree/main", true, "user", "repo", "main", ""},
		{"tree + 子路径", "https://github.com/user/repo/tree/main/src/lib",
			true, "user", "repo", "main", "src/lib"},
		{"blob 段", "https://github.com/user/repo/blob/v1.2.3/README.md",
			true, "user", "repo", "v1.2.3", "README.md"},
		{"blob + 深层子路径", "github.com/o/r/blob/abc/sub/dir/file.ts",
			true, "o", "r", "abc", "sub/dir/file.ts"},
		{"非 GitHub 域", "https://gitlab.com/user/repo", false, "", "", "", ""},
		{"缺 repo", "github.com/user", false, "", "", "", ""},
		{"只有域", "github.com", false, "", "", "", ""},
		{"空串", "", false, "", "", "", ""},
		{"多一段无关键字", "github.com/user/repo/extra", false, "", "", "", ""},
		{"releases 段不识别", "github.com/user/repo/releases/tag/v1", false, "", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parseGitHubURL(c.in)
			if ok != c.wantOK {
				t.Fatalf("parseGitHubURL(%q) ok = %v，期望 %v", c.in, ok, c.wantOK)
			}
			if !ok {
				return
			}
			if got.Owner != c.owner || got.Repo != c.repo || got.Ref != c.ref || got.Subpath != c.subpath {
				t.Errorf("实得 %+v，期望 owner=%q repo=%q ref=%q subpath=%q",
					got, c.owner, c.repo, c.ref, c.subpath)
			}
		})
	}
}

// TestParseGitHubURLOnlyStripsTrailingDotGit —— `.git` 只在**末尾**剥一次。
//
// 对账 TS 的 `.replace(/\.git$/, ”)`（带 `$` 锚）。
// 若误用 `strings.ReplaceAll`，`github.com/a.git/b` 会被破坏成 `github.com/a/b`。
func TestParseGitHubURLOnlyStripsTrailingDotGit(t *testing.T) {
	got, ok := parseGitHubURL("github.com/a.git/b")
	if !ok {
		t.Fatal("应解析成功（用户名可含 .git）")
	}
	if got.Owner != "a.git" {
		t.Errorf("owner 应为 a.git（.git 不该被中间剥离），实得 %q", got.Owner)
	}
}

// TestParseGitHubURLSchemeCaseSensitive —— 对账 TS：`/^https?:\/\//` 大小写敏感。
func TestParseGitHubURLSchemeCaseSensitive(t *testing.T) {
	// 大写 HTTPS 不被剥 → 正则不匹配 → 失败
	if _, ok := parseGitHubURL("HTTPS://github.com/user/repo"); ok {
		t.Error("大写 scheme 不该被剥离（对账 TS 大小写敏感）")
	}
}

// ── subpathEscapesContainer ─────────────────────────────────────────────

// TestSubpathEscapesLexicalTraversal —— **词法层**拦 `..` 穿越。
//
// 这是 issue #119 的主场景：`blob/main/../../../../etc/passwd`。
func TestSubpathEscapesLexicalTraversal(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name    string
		subpath string
		want    bool
	}{
		{"空子路径放行", "", false},
		{"正常子路径", "src/lib", false},
		{"单层上行但未越出", "src/../lib", false},
		{"越出一层", "../x", true},
		{"深层穿越", "../../../../etc/passwd", true},
		{"伪装的中间穿越", "src/../../etc/passwd", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := subpathEscapesContainer(root, c.subpath); got != c.want {
				t.Errorf("subpathEscapesContainer(%q) = %v，期望 %v", c.subpath, got, c.want)
			}
		})
	}
}

// TestSubpathEscapesSymlinkInContainer —— **真实层**拦容器内符号链接外指。
//
// clone 下来的仓库可自带 symlink 指向容器外——纯字符串 resolve 拦不住。
// 这是「词法检查」无法覆盖的那一半。
func TestSubpathEscapesSymlinkInContainer(t *testing.T) {
	base := t.TempDir()
	container := filepath.Join(base, "repo")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(container, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 容器内建一个指向外部的符号链接
	link := filepath.Join(container, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("当前环境不支持建符号链接（%v）", err)
	}

	// 词法上 `escape/secret.txt` 在容器内，但真实路径在容器外 → 必须判逃逸
	if !subpathEscapesContainer(container, "escape/secret.txt") {
		t.Error("容器内符号链接外指必须被判逃逸（词法层拦不住，靠 realpath 层）")
	}
	// 对照：容器内真实存在的普通文件不该判逃逸
	if err := os.WriteFile(filepath.Join(container, "ok.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if subpathEscapesContainer(container, "ok.txt") {
		t.Error("容器内真实文件不该判逃逸")
	}
}

// TestSubpathEscapesMissingPathAllowed —— 路径不存在时**词法结论为准**（放行）。
//
// 对账 TS 的 `catch { return false }`：尚未 clone / 子路径缺失时，
// 上层会另行报「未找到子路径」，此处不判逃逸。
func TestSubpathEscapesMissingPathAllowed(t *testing.T) {
	root := t.TempDir()
	// 容器内不存在的路径——不该因 EvalSymlinks 失败就判逃逸
	if subpathEscapesContainer(root, "not/yet/cloned") {
		t.Error("不存在的路径不该判逃逸（词法层已通过）")
	}
}

// TestIsInsideContainerPrefixBoundary —— **前缀必须带分隔符**的边界。
//
// `/a/bc` 不该被判为在 `/a/b` 之内。
func TestIsInsideContainerPrefixBoundary(t *testing.T) {
	sep := string(filepath.Separator)
	cases := []struct {
		root, child string
		want        bool
	}{
		{"/a/b", "/a/b", true},
		{"/a/b", "/a/b" + sep + "c", true},
		{"/a/b", "/a/bc", false}, // ← 关键：不带分隔符的前缀不算包含
		{"/a/b", "/a", false},
	}
	for _, c := range cases {
		if got := isInsideContainer(c.root, c.child); got != c.want {
			t.Errorf("isInsideContainer(%q, %q) = %v，期望 %v", c.root, c.child, got, c.want)
		}
	}
}

// ── isSafeGitRef ────────────────────────────────────────────────────────

func TestIsSafeGitRef(t *testing.T) {
	cases := []struct {
		name string
		ref  string
		want bool
	}{
		{"正常分支", "main", true},
		{"版本 tag", "v1.2.3", true},
		{"带斜杠", "feature/foo-bar", true},
		{"带下划线与点", "release_1.0.x", true},
		{"裸 commit hash", "abc1234", true},

		{"空串拒绝", "", false},
		{"超 255 拒绝", strings.Repeat("a", 256), false},
		{"恰好 255 放行", strings.Repeat("a", 255), true},

		{"★ 选项注入 -开头", "--upload-pack=/bin/sh", false},
		{"单横线也拒", "-x", false},

		{"含空格", "a b", false},
		{"含制表符", "a\tb", false},
		{"含换行", "a\nb", false},
		{"含 NUL", "a\x00b", false},
		{"含 DEL", "a\x7fb", false},

		{"含波浪号", "a~b", false},
		{"含脱字符", "a^b", false},
		{"含冒号", "a:b", false},
		{"含问号", "a?b", false},
		{"含星号", "a*b", false},
		{"含左方括号", "a[b", false},
		{"含反斜杠", `a\b`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isSafeGitRef(c.ref); got != c.want {
				t.Errorf("isSafeGitRef(%q) = %v，期望 %v", c.ref, got, c.want)
			}
		})
	}
}

// TestIsSafeGitRefRejectsInvalidUTF8 —— Go 侧额外防御（TS 无此情形）。
//
// TS 的字符串恒为 UTF-16，不存在无效字节序列；Go 侧显式拒绝，保持 fail-closed。
func TestIsSafeGitRefRejectsInvalidUTF8(t *testing.T) {
	bad := string([]byte{0xff, 0xfe})
	if isSafeGitRef(bad) {
		t.Error("无效 UTF-8 应被拒（Go 侧防御）")
	}
}

// TestIsSafeGitRefLengthIsUTF16Units —— 长度按 **UTF-16 code unit**（对账 TS `.length`）。
func TestIsSafeGitRefLengthIsUTF16Units(t *testing.T) {
	// 128 个 emoji = 256 UTF-16 unit → 超 255 → 拒
	emoji := strings.Repeat("😀", 128)
	if UTF16Len(emoji) != 256 {
		t.Fatalf("前置断言错：UTF16Len = %d，期望 256", UTF16Len(emoji))
	}
	if isSafeGitRef(emoji) {
		t.Error("256 UTF-16 unit 应超 255 上限被拒")
	}
	// 127 个 emoji = 254 unit → 放行
	if !isSafeGitRef(strings.Repeat("😀", 127)) {
		t.Error("254 UTF-16 unit 应在限内放行")
	}
}

// ── simpleHash ──────────────────────────────────────────────────────────

// TestSimpleHashMatchesJS —— 对账 TS 的 `simpleHash`（32 位有符号 + `| 0` + `Math.abs`）。
func TestSimpleHashMatchesJS(t *testing.T) {
	// 手工按 JS 语义算：h=0; h = ((0<<5)-0+'a')|0 = 97 → abs=97
	if got := simpleHash("a"); got != 97 {
		t.Errorf("simpleHash(\"a\") = %d，期望 97", got)
	}
	// 非负不变量
	for _, s := range []string{"", "x", "/tmp/design.png", "https://github.com/u/r", "中文路径/文件.pdf"} {
		if h := simpleHash(s); h < 0 {
			t.Errorf("simpleHash(%q) = %d，不该为负（对账 Math.abs）", s, h)
		}
	}
}

// TestSimpleHashIsNonNegativeEvenForLargeInputs —— 大输入下 `| 0` 回环后仍非负。
func TestSimpleHashIsNonNegativeEvenForLargeInputs(t *testing.T) {
	long := strings.Repeat("abcdefghij", 500)
	if h := simpleHash(long); h < 0 {
		t.Errorf("长输入下 simpleHash = %d，不该为负", h)
	}
}

// ── importTargetName ────────────────────────────────────────────────────

func TestImportTargetName(t *testing.T) {
	cases := []struct {
		name   string
		source string
		// 校验形状而非具体 hash（hash 由 simpleHash 单测覆盖）
		wantPrefix string
		wantSuffix string
	}{
		{"带扩展名", "/tmp/design.png", "design-", ".png"},
		{"无扩展名", "/tmp/README", "README-", ""},
		{"含非法字符被替换", "/tmp/my file(1).png", "my_file_1_-", ".png"},
		// 对账 TS `:284`：GitHub 分支传的是 `${owner}/${repo}`——
		// `basename` 取的是 **repo**，不是 owner。故前缀是 "b-"。
		{"GitHub 形态取 repo", "github.com/a/b", "b-", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := importTargetName(c.source)
			if !strings.HasPrefix(got, c.wantPrefix) {
				t.Errorf("importTargetName(%q) = %q，应以 %q 开头", c.source, got, c.wantPrefix)
			}
			if c.wantSuffix != "" && !strings.HasSuffix(got, c.wantSuffix) {
				t.Errorf("importTargetName(%q) = %q，应以 %q 结尾", c.source, got, c.wantSuffix)
			}
		})
	}
}

// TestImportTargetNamePreservesExtension —— **扩展名必须保留**。
//
// 这是 `importTargetName` 存在的理由：`read_file` 等工具靠扩展名判断类型。
func TestImportTargetNamePreservesExtension(t *testing.T) {
	for _, c := range []struct{ src, ext string }{
		{"/a/b/spec.pdf", ".pdf"},
		{"/a/b/x.tar.gz", ".gz"},
		{"/a/b/noext", ""},
	} {
		got := importTargetName(c.src)
		if filepath.Ext(got) != c.ext {
			t.Errorf("importTargetName(%q) = %q，扩展名应为 %q", c.src, got, c.ext)
		}
	}
}

// TestImportTargetNameTruncatesTo40Units —— 截断按 **UTF-16 unit**（对账 TS `slice(0,40)`）。
func TestImportTargetNameTruncatesTo40Units(t *testing.T) {
	long := "/tmp/" + strings.Repeat("n", 200) + ".txt"
	got := importTargetName(long)
	base := strings.TrimSuffix(got, filepath.Ext(got))
	// base 形态是 raw-hash；raw 被截到 40 → base 最多 40 + 1 + hash长度
	if UTF16Len(base) > 40+1+7 {
		t.Errorf("base 过长（未截断到 40）：%q（%d unit）", base, UTF16Len(base))
	}
}

// TestSimpleHashDiffersForDifferentSources —— 不同 source 应（通常）给不同 hash。
//
// 这是「同名不同源不互相覆盖」的基础。
func TestSimpleHashDiffersForDifferentSources(t *testing.T) {
	a := simpleHash("/tmp/a.png")
	b := simpleHash("/tmp/b.png")
	if a == b {
		t.Errorf("不同 source 的 hash 不该相同（%d）", a)
	}
}

// ── ensureImportDir ─────────────────────────────────────────────────────

// TestEnsureImportDirCreatesAndReturns —— 建目录并返回路径。
func TestEnsureImportDirCreatesAndReturns(t *testing.T) {
	cwd := t.TempDir()
	dir, err := ensureImportDir(cwd)
	if err != nil {
		t.Fatalf("不应出错：%v", err)
	}
	want := filepath.Join(cwd, ".rivet", "external")
	if dir != want {
		t.Errorf("实得 %q，期望 %q", dir, want)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Errorf(".rivet/external 应被创建：err=%v", err)
	}
}

// TestEnsureImportDirIdempotent —— 重复调用不报错（MkdirAll 语义）。
func TestEnsureImportDirIdempotent(t *testing.T) {
	cwd := t.TempDir()
	if _, err := ensureImportDir(cwd); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureImportDir(cwd); err != nil {
		t.Errorf("重复调用不该报错：%v", err)
	}
}

// ── 常量对账 ────────────────────────────────────────────────────────────

func TestImportResourceConstants(t *testing.T) {
	if importDirRel != ".rivet/external" {
		t.Errorf("IMPORT_DIR 应为 .rivet/external，实得 %q", importDirRel)
	}
	if previewBytes != 4000 {
		t.Errorf("PREVIEW_BYTES 应为 4000，实得 %d", previewBytes)
	}
}

// ── base36 ──────────────────────────────────────────────────────────────

func TestBase36(t *testing.T) {
	cases := []struct {
		in   uint32
		want string
	}{
		{0, "0"},
		{1, "1"},
		{9, "9"},
		{10, "a"},
		{35, "z"},
		{36, "10"},
		{37, "11"},
	}
	for _, c := range cases {
		if got := base36(c.in); got != c.want {
			t.Errorf("base36(%d) = %q，期望 %q", c.in, got, c.want)
		}
	}
}
