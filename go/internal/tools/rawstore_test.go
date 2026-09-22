package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// rawstore_test.go —— 对账 TS `persistRawOutput` / `cleanStaleRawOutputs` /
// `rawOutputDir` / `safeRawFileName`，以及配套的 runtime read grant。

// TestRawOutputDirOutsideWorkspace —— raw 目录在项目之外（临时目录）。
//
// **这是 grant 必需性的前提**：目录在项目外 → read_file 会拦 → 必须 grant。
func TestRawOutputDirOutsideWorkspace(t *testing.T) {
	dir := RawOutputDir()
	if dir == "" {
		t.Fatal("RawOutputDir 不应为空")
	}
	if !strings.HasSuffix(dir, "rivet-raw") {
		t.Errorf("目录应以 rivet-raw 结尾（对账 TS）：%q", dir)
	}
	// 应等于 os.TempDir()/rivet-raw。
	want := filepath.Join(os.TempDir(), "rivet-raw")
	if dir != want {
		t.Errorf("RawOutputDir=%q want %q", dir, want)
	}
}

// TestSafeRawFileNameShape —— 文件名是 sha256 前 24 hex + `.raw`。
func TestSafeRawFileNameShape(t *testing.T) {
	name := safeRawFileName("tooluse-abc")
	if !strings.HasSuffix(name, ".raw") {
		t.Errorf("应以 .raw 结尾：%q", name)
	}
	base := strings.TrimSuffix(name, ".raw")
	if len(base) != rawHashLen {
		t.Errorf("哈希段应为 %d 字符，实得 %d：%q", rawHashLen, len(base), name)
	}
	// 应为 hex（小写 0-9a-f）。
	for _, c := range base {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("哈希段应为小写 hex，实得含 %q：%q", c, name)
		}
	}
	// 同 id 应稳定（幂等）。
	if safeRawFileName("x") != safeRawFileName("x") {
		t.Error("同 id 的文件名应稳定")
	}
	// 不同 id 应不同。
	if safeRawFileName("x") == safeRawFileName("y") {
		t.Error("不同 id 应产出不同文件名")
	}
	// 空 id 应仍产出合法名（对账 TS 的 randomUUID 分支）。
	empty := safeRawFileName("")
	if len(strings.TrimSuffix(empty, ".raw")) != rawHashLen {
		t.Errorf("空 id 也应产出合法名：%q", empty)
	}
}

// TestPersistRawOutputWritesFile —— 落盘内容与原文逐字节一致。
func TestPersistRawOutputWritesFile(t *testing.T) {
	ResetRawPersistStateForTests()
	content := "line1\nline2\n中文内容\n"
	path := PersistRawOutput("test-"+t.Name(), content)
	if path == "" {
		t.Fatal("落盘应成功")
	}
	defer os.Remove(path)

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回失败：%v", err)
	}
	if string(got) != content {
		t.Errorf("内容不符：got=%q want=%q", string(got), content)
	}
	// 应落在 RawOutputDir 下。
	if filepath.Dir(path) != RawOutputDir() {
		t.Errorf("应在 RawOutputDir 下：%q", path)
	}
}

// TestPersistRawOutputGracefulOnFailure —— 落盘失败返回空串（不 panic）。
//
// 对账 TS 的 `persistRawSafe`（try/catch → undefined）。
//
// **失败注入方式**：不能靠改 `TMPDIR`——Go 的 `os.TempDir()` 在**进程启动时
// 缓存**了该值，测试内 `t.Setenv` 改不动（实测：改了仍写入原目录）。
// 改用「目标路径已被一个**目录**占据」——`os.WriteFile` 会因 EISDIR 失败。
func TestPersistRawOutputGracefulOnFailure(t *testing.T) {
	ResetRawPersistStateForTests()
	dir := RawOutputDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	// 让目标文件路径变成一个**目录** → WriteFile 必失败。
	id := "graceful-fail-" + t.Name()
	target := filepath.Join(dir, safeRawFileName(id))
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(target)

	got := PersistRawOutput(id, "content")
	if got != "" {
		t.Errorf("落盘失败应返回空串，实得 %q", got)
	}
}

// TestCleanStaleRawOutputs —— 超 TTL 的删、未超的留。
func TestCleanStaleRawOutputs(t *testing.T) {
	ResetRawPersistStateForTests()
	dir := RawOutputDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	// 造两个文件：一个「旧」（2 小时前）、一个「新」。
	oldPath := filepath.Join(dir, "stale-test.raw")
	newPath := filepath.Join(dir, "fresh-test.raw")
	for _, p := range []string{oldPath, newPath} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// 把旧的 mtime 拨回 2 小时前。
	twoHoursAgo := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(oldPath, twoHoursAgo, twoHoursAgo); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(newPath)

	CleanStaleRawOutputs(time.Now())

	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Errorf("超 TTL 的文件应被删除，实得 err=%v", err)
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Errorf("未超 TTL 的文件应保留，实得 err=%v", err)
	}
}

// TestCleanStaleRawOutputsMissingDir —— 目录不存在时静默返回（对账 TS catch）。
//
// **注意**：不能靠改 `TMPDIR` 制造「目录不存在」——`os.TempDir()` 在进程启动时
// 缓存。故改为直接测 `os.ReadDir` 失败路径的等价情形：删掉目录后调用（若目录
// 恰好被其他测试创建过，本测试仍应安全通过——它只断言「不 panic」）。
func TestCleanStaleRawOutputsMissingDir(t *testing.T) {
	// 直接调用即可——若目录存在且为空，ReadDir 成功但无文件可删；
	// 若不存在，走 catch 分支。两种路径都不应 panic。
	CleanStaleRawOutputs(time.Now())

	// 反向断言：删掉目录后调用仍不 panic。
	dir := RawOutputDir()
	if err := os.RemoveAll(dir); err != nil {
		t.Skipf("无法删除 raw 目录（可能被占用）：%v", err)
	}
	CleanStaleRawOutputs(time.Now())
}

// TestGrantRuntimeReadPathsIdempotent —— grant 幂等（不重复追加）。
func TestGrantRuntimeReadPathsIdempotent(t *testing.T) {
	dir := RawOutputDir()

	once := GrantRuntimeReadPaths(nil)
	if len(once) != 1 || once[0] != dir {
		t.Fatalf("首次应追加 raw 目录：%v", once)
	}
	twice := GrantRuntimeReadPaths(once)
	if len(twice) != 1 {
		t.Errorf("幂等：已授权不应重复追加，实得 %v", twice)
	}
	// 保留既有授权。
	withOther := GrantRuntimeReadPaths([]string{"/other/path"})
	if len(withOther) != 2 {
		t.Errorf("应保留既有授权并追加：%v", withOther)
	}
}

// TestStaticGrantCheckerSegmentBoundary —— **前缀匹配必须按段边界**。
//
// 防 fail-open：`/tmp/rivet-raw-evil` 不应被 `/tmp/rivet-raw` 判为已授权。
func TestStaticGrantCheckerSegmentBoundary(t *testing.T) {
	root := filepath.Join(os.TempDir(), "rivet-raw")
	g := &StaticGrantChecker{ReadPaths: []string{root}}

	// 正例：目录内。
	if !g.IsReadGranted(filepath.Join(root, "abc.raw"), "/cwd") {
		t.Error("目录内的文件应被授权")
	}
	// 正例：目录本身。
	if !g.IsReadGranted(root, "/cwd") {
		t.Error("目录本身应被授权")
	}
	// **反例（fail-open 洞）**：同前缀但不同目录。
	if g.IsReadGranted(root+"-evil/x.raw", "/cwd") {
		t.Error("同前缀的兄弟目录不应被授权（fail-open 洞）")
	}
	// 反例：父目录。
	if g.IsReadGranted(filepath.Dir(root), "/cwd") {
		t.Error("父目录不应被授权")
	}
	// 反例：完全无关。
	if g.IsReadGranted("/somewhere/else", "/cwd") {
		t.Error("无关路径不应被授权")
	}
}

// TestBashPersistsRawAndHintsRecovery —— **端到端**：bash 落盘 + 提示含路径。
func TestBashPersistsRawAndHintsRecovery(t *testing.T) {
	ResetRawPersistStateForTests()
	dir := t.TempDir()
	tool := Bash(dir)

	// 造足够长的输出触发截断（>200 行 → head+tail 分支）。
	cmd := "seq 1 300"
	p := call(dir, map[string]any{"command": cmd})
	p.ToolUseID = "e2e-" + t.Name()
	r, _ := tool.Execute(t.Context(), p)
	if r.IsError {
		t.Skipf("命令不可用：%.200s", r.Content)
	}

	// 应返回 rawPath。
	if r.RawPath == "" {
		t.Fatal("bash 应落盘原始输出并返回 RawPath")
	}
	defer os.Remove(r.RawPath)

	// recovery 提示应含该路径。
	if !strings.Contains(r.Content, r.RawPath) {
		t.Errorf("截断 footer 应含恢复路径 %q：%.400s", r.RawPath, r.Content)
	}
	if !strings.Contains(r.Content, "不要重跑命令") {
		t.Errorf("恢复提示应劝止重跑：%.400s", r.Content)
	}

	// **落盘内容应是完整原文**（300 行都在）。
	raw, err := os.ReadFile(r.RawPath)
	if err != nil {
		t.Fatalf("读回失败：%v", err)
	}
	if !strings.Contains(string(raw), "300") {
		t.Errorf("落盘应是完整原文（含第 300 行）：%.200s", tailSnippet(string(raw), 200))
	}
}

// TestBashRawPathReadableViaGrant —— **端到端闭环**：落盘的文件经 grant 后可读。
//
// 这是本刀的核心价值验证——只落盘不 grant 会让 recovery 提示误导。
func TestBashRawPathReadableViaGrant(t *testing.T) {
	ResetRawPersistStateForTests()
	dir := t.TempDir()
	tool := Bash(dir)
	p := call(dir, map[string]any{"command": "seq 1 300"})
	p.ToolUseID = "grant-" + t.Name()
	r, _ := tool.Execute(t.Context(), p)
	if r.IsError || r.RawPath == "" {
		t.Skipf("前置失败：%.200s", r.Content)
	}
	defer os.Remove(r.RawPath)

	// 构造带 grant 的 checker（模拟装配层注入），传给 ReadFile 构造器。
	checker := &StaticGrantChecker{ReadPaths: GrantRuntimeReadPaths(nil)}

	// 用 read_file 读回——应成功（不被越界拦截）。
	rf := ReadFile(dir, checker)
	rp := call(dir, map[string]any{"file_path": r.RawPath})
	res, _ := rf.Execute(t.Context(), rp)

	if res.IsError {
		t.Fatalf("grant 后应能读回 raw 文件，实得：%.300s", res.Content)
	}
	if !strings.Contains(res.Content, "300") {
		t.Errorf("应读到完整内容：%.300s", res.Content)
	}
}

// TestBashRawPathNotReadableWithoutGrant —— **反证**：无 grant 时读不到。
//
// 这条证明 grant 不是可有可无的——若读得到，说明越界检查失效。
func TestBashRawPathNotReadableWithoutGrant(t *testing.T) {
	ResetRawPersistStateForTests()
	dir := t.TempDir()
	tool := Bash(dir)
	p := call(dir, map[string]any{"command": "seq 1 300"})
	p.ToolUseID = "nogrant-" + t.Name()
	r, _ := tool.Execute(t.Context(), p)
	if r.IsError || r.RawPath == "" {
		t.Skipf("前置失败：%.200s", r.Content)
	}
	defer os.Remove(r.RawPath)

	// **不注入 grant**。
	rf := ReadFile(dir, nil)
	rp := call(dir, map[string]any{"file_path": r.RawPath})
	res, _ := rf.Execute(t.Context(), rp)

	if !res.IsError {
		t.Fatal("无 grant 时应被越界拦截——若读得到，说明 grant 检查失效（fail-open）")
	}
	if !strings.Contains(res.Content, "outside project directory") {
		t.Errorf("应报越界：%.300s", res.Content)
	}
}
