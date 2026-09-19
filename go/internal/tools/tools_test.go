package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kalandramo/tianshu/go/internal/contract"
)

// call 构造调用参数（审批已放开，聚焦工具行为）。
func call(cwd string, input map[string]any) *CallParams {
	return &CallParams{
		Input:        input,
		Cwd:          cwd,
		ApprovalMode: "dangerously-skip-permissions",
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ── read_file ──

func TestReadFileBasic(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "a.txt"), "line1\nline2\nline3\n")

	tool := ReadFile(root, nil)
	r, err := tool.Execute(context.Background(), call(root, map[string]any{"path": "a.txt"}))
	if err != nil {
		t.Fatal(err)
	}
	if r.IsError {
		t.Fatalf("不应报错：%s", r.Content)
	}
	if !strings.Contains(r.Content, "line1") || !strings.Contains(r.Content, "line3") {
		t.Errorf("内容不完整：%s", r.Content)
	}
}

// offset/limit 读子区间。
func TestReadFileOffsetLimit(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "a.txt"), "l1\nl2\nl3\nl4\nl5\n")

	tool := ReadFile(root, nil)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{
		"path": "a.txt", "offset": 2, "limit": 2,
	}))
	if !strings.Contains(r.Content, "l2") || !strings.Contains(r.Content, "l3") {
		t.Errorf("区间读取错误：%s", r.Content)
	}
	if strings.Contains(r.Content, "l4") {
		t.Errorf("limit 未生效：%s", r.Content)
	}
}

// 反证 A：逃逸路径必须被拦截。
func TestReadFileEscapeBlocked(t *testing.T) {
	root := t.TempDir()
	tool := ReadFile(root, nil)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{"path": "../../etc/passwd"}))
	if !r.IsError {
		t.Fatal("逃逸路径必须被拦截")
	}
	if !strings.Contains(r.Content, "outside project directory") {
		t.Errorf("错误信息应说明越界：%s", r.Content)
	}
}

// 反证 B：敏感文件必须被拦截。
func TestReadFileSensitiveBlocked(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, ".env"), "SECRET=1")
	tool := ReadFile(root, nil)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{"path": ".env"}))
	if !r.IsError {
		t.Fatal("敏感文件必须被拦截")
	}
	if !strings.Contains(r.Content, "Sensitive file blocked") {
		t.Errorf("应报敏感文件：%s", r.Content)
	}
}

// 二进制文件应被拒绝（不把乱码喂给模型）。
func TestReadFileBinaryRejected(t *testing.T) {
	root := t.TempDir()
	bin := []byte{0x00, 0x01, 0x02, 0xFF, 0xFE, 0x00}
	if err := os.WriteFile(filepath.Join(root, "bin.dat"), bin, 0o644); err != nil {
		t.Fatal(err)
	}
	tool := ReadFile(root, nil)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{"path": "bin.dat"}))
	if !r.IsError {
		t.Fatal("二进制文件应被拒绝")
	}
	if !strings.Contains(r.Content, "二进制") {
		t.Errorf("应说明是二进制：%s", r.Content)
	}
}

// 反证 C：超长内容截断必须标记 Lossiness。
//
// 截断的观测不能支撑负向结论——标记是这条纪律的技术保障。
func TestReadFileTruncationMarked(t *testing.T) {
	root := t.TempDir()
	big := strings.Repeat("x", 200_000)
	mustWriteFile(t, filepath.Join(root, "big.txt"), big)

	tool := ReadFile(root, nil)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{"path": "big.txt"}))

	if r.Lossiness == nil {
		t.Fatal("截断必须标记 Lossiness（否则模型会把截断当完整）")
	}
	if *r.Lossiness != contract.LossinessTruncated {
		t.Errorf("Lossiness = %q, want truncated", *r.Lossiness)
	}
	if !strings.Contains(r.Content, "truncated") {
		t.Errorf("内容里应有截断提示：%.200s", r.Content)
	}
}

// 不截断时 Lossiness 必须缺席（缺席 ≠ lossless 的显式声明）。
func TestReadFileNoTruncationNoLossiness(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "small.txt"), "hi")
	tool := ReadFile(root, nil)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{"path": "small.txt"}))
	if r.Lossiness != nil {
		t.Errorf("未截断时 Lossiness 应缺席，实际 %q", *r.Lossiness)
	}
}

// 目录应被拒绝并给出替代建议。
func TestReadFileDirectoryRejected(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	tool := ReadFile(root, nil)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{"path": "sub"}))
	if !r.IsError {
		t.Fatal("目录应被拒绝")
	}
	if !strings.Contains(r.Content, "glob") {
		t.Errorf("应建议用 glob：%s", r.Content)
	}
}

// ── write_file ──

func TestWriteFileCreate(t *testing.T) {
	root := t.TempDir()
	tool := WriteFile(root, nil)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{
		"file_path": "new/dir/f.txt", "content": "hello",
	}))
	if r.IsError {
		t.Fatalf("写入失败：%s", r.Content)
	}
	data, err := os.ReadFile(filepath.Join(root, "new", "dir", "f.txt"))
	if err != nil {
		t.Fatalf("父目录未自动创建：%v", err)
	}
	if string(data) != "hello" {
		t.Errorf("内容 = %q", string(data))
	}
}

func TestWriteFileAppend(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "f.txt"), "a\n")
	tool := WriteFile(root, nil)
	_, _ = tool.Execute(context.Background(), call(root, map[string]any{
		"file_path": "f.txt", "content": "b\n", "mode": "append",
	}))
	data, _ := os.ReadFile(filepath.Join(root, "f.txt"))
	if string(data) != "a\nb\n" {
		t.Errorf("追加结果 = %q", string(data))
	}
}

// 反证 D：写操作的逃逸必须被拦截（且按 write 模式）。
func TestWriteFileEscapeBlocked(t *testing.T) {
	root := t.TempDir()
	tool := WriteFile(root, nil)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{
		"file_path": "../outside.txt", "content": "x",
	}))
	if !r.IsError {
		t.Fatal("写逃逸必须被拦截")
	}
}

// OnFileWrite 必须被回调（证据追踪依赖它）。
func TestWriteFileRegistersWrite(t *testing.T) {
	root := t.TempDir()
	tool := WriteFile(root, nil)
	var registered []string
	p := call(root, map[string]any{"file_path": "f.txt", "content": "x"})
	p.OnFileWrite = func(path string) { registered = append(registered, path) }

	_, _ = tool.Execute(context.Background(), p)
	if len(registered) != 1 {
		t.Fatalf("OnFileWrite 应被调用 1 次，实际 %d", len(registered))
	}
}

// 审批模式：非放开档位时需要批准。
func TestWriteFileRequiresApproval(t *testing.T) {
	root := t.TempDir()
	tool := WriteFile(root, nil)
	if !tool.RequiresApproval(&CallParams{ApprovalMode: "auto-safe"}) {
		t.Error("auto-safe 档位下写操作应需批准")
	}
	if tool.RequiresApproval(&CallParams{ApprovalMode: "dangerously-skip-permissions"}) {
		t.Error("放开档位下写操作不应需批准")
	}
}

// ── edit_file ──

func TestEditFileReplace(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "f.txt"), "hello world\n")
	tool := EditFile(root, nil)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{
		"file_path": "f.txt", "old_string": "world", "new_string": "Go",
	}))
	if r.IsError {
		t.Fatalf("编辑失败：%s", r.Content)
	}
	data, _ := os.ReadFile(filepath.Join(root, "f.txt"))
	if string(data) != "hello Go\n" {
		t.Errorf("结果 = %q", string(data))
	}
}

// 反证 E：old_string 不唯一时必须拒绝（避免误改）。
func TestEditFileAmbiguousRejected(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "f.txt"), "x\nx\nx\n")
	tool := EditFile(root, nil)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{
		"file_path": "f.txt", "old_string": "x", "new_string": "y",
	}))
	if !r.IsError {
		t.Fatal("不唯一的 old_string 必须被拒绝")
	}
	if !strings.Contains(r.Content, "不唯一") {
		t.Errorf("应说明不唯一：%s", r.Content)
	}
	// 文件不应被改动
	data, _ := os.ReadFile(filepath.Join(root, "f.txt"))
	if string(data) != "x\nx\nx\n" {
		t.Errorf("被拒绝的编辑不应改动文件：%q", string(data))
	}
}

// replace_all 应替换全部。
func TestEditFileReplaceAll(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "f.txt"), "x\nx\nx\n")
	tool := EditFile(root, nil)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{
		"file_path": "f.txt", "old_string": "x", "new_string": "y", "replace_all": true,
	}))
	if r.IsError {
		t.Fatalf("replace_all 失败：%s", r.Content)
	}
	data, _ := os.ReadFile(filepath.Join(root, "f.txt"))
	if string(data) != "y\ny\ny\n" {
		t.Errorf("结果 = %q", string(data))
	}
}

// old_string 未找到时给出可行动建议。
func TestEditFileNotFoundActionable(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "f.txt"), "hello\n")
	tool := EditFile(root, nil)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{
		"file_path": "f.txt", "old_string": "nonexistent", "new_string": "x",
	}))
	if !r.IsError {
		t.Fatal("未找到应报错")
	}
	if !strings.Contains(r.Content, "read_file") {
		t.Errorf("应建议先 read_file 确认：%s", r.Content)
	}
}

// ── glob ──

func TestGlobBasic(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "a.ts"), "x")
	mustWriteFile(t, filepath.Join(root, "b.ts"), "x")
	mustWriteFile(t, filepath.Join(root, "c.md"), "x")

	tool := Glob(root)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{"pattern": "*.md"}))
	if r.IsError {
		t.Fatalf("glob 失败：%s", r.Content)
	}
	if !strings.Contains(r.Content, "c.md") {
		t.Errorf("应匹配 c.md：%s", r.Content)
	}
	if strings.Contains(r.Content, "a.ts") {
		t.Errorf("不应匹配 a.ts：%s", r.Content)
	}
}

// ** 递归匹配。
func TestGlobRecursive(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "src", "deep", "a.ts"), "x")
	mustWriteFile(t, filepath.Join(root, "b.md"), "x")

	tool := Glob(root)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{"pattern": "src/**/*.ts"}))
	if !strings.Contains(r.Content, "a.ts") {
		t.Errorf("** 递归匹配失败：%s", r.Content)
	}
}

// 重目录应被跳过。
func TestGlobSkipsHeavyDirs(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "node_modules", "pkg", "a.js"), "x")
	mustWriteFile(t, filepath.Join(root, "src", "b.js"), "x")

	tool := Glob(root)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{"pattern": "**/*.js"}))
	if strings.Contains(r.Content, "node_modules") {
		t.Errorf("应跳过 node_modules：%s", r.Content)
	}
	if !strings.Contains(r.Content, "b.js") {
		t.Errorf("应包含 src/b.js：%s", r.Content)
	}
}

// ── grep ──

func TestGrepBasic(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "a.ts"), "func foo() {}\nfunc bar() {}\n")

	tool := Grep(root)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{"pattern": "func foo"}))
	if r.IsError {
		t.Fatalf("grep 失败：%s", r.Content)
	}
	if !strings.Contains(r.Content, "a.ts") || !strings.Contains(r.Content, "foo") {
		t.Errorf("结果错误：%s", r.Content)
	}
}

// 无匹配时必须提示「无匹配 ≠ 不存在」。
func TestGrepNoMatchHonestMessage(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "a.ts"), "hello\n")

	tool := Grep(root)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{"pattern": "zzz_not_found"}))
	if r.IsError {
		t.Fatalf("无匹配不是错误：%s", r.Content)
	}
	if !strings.Contains(r.Content, "不等于") {
		t.Errorf("应提示「无匹配不等于不存在」：%s", r.Content)
	}
}

// literal 模式应转义正则元字符。
func TestGrepLiteralMode(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "a.ts"), "a.b\naxb\n")

	tool := Grep(root)
	// 正则模式下 a.b 会匹配 axb
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{"pattern": "a.b"}))
	if !strings.Contains(r.Content, "axb") {
		t.Errorf("正则模式应匹配 axb：%s", r.Content)
	}
	// literal 模式只匹配字面 a.b
	r, _ = tool.Execute(context.Background(), call(root, map[string]any{"pattern": "a.b", "literal": true}))
	if strings.Contains(r.Content, "axb") {
		t.Errorf("literal 模式不应匹配 axb：%s", r.Content)
	}
}

// 上下文行。
func TestGrepContextLines(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "a.ts"), "line1\nTARGET\nline3\n")

	tool := Grep(root)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{
		"pattern": "TARGET", "context_lines": 1,
	}))
	if !strings.Contains(r.Content, "line1") || !strings.Contains(r.Content, "line3") {
		t.Errorf("上下文行未包含：%s", r.Content)
	}
}

// glob 过滤。
func TestGrepGlobFilter(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "a.ts"), "target\n")
	mustWriteFile(t, filepath.Join(root, "b.md"), "target\n")

	tool := Grep(root)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{
		"pattern": "target", "glob": "*.ts",
	}))
	if !strings.Contains(r.Content, "a.ts") {
		t.Errorf("应匹配 a.ts：%s", r.Content)
	}
	if strings.Contains(r.Content, "b.md") {
		t.Errorf("glob 过滤未生效（含 b.md）：%s", r.Content)
	}
}

// 正则编译失败应给出可行动提示。
func TestGrepInvalidRegexActionable(t *testing.T) {
	root := t.TempDir()
	tool := Grep(root)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{"pattern": "[unclosed"}))
	if !r.IsError {
		t.Fatal("非法正则应报错")
	}
	if !strings.Contains(r.Content, "literal") {
		t.Errorf("应提示可用 literal=true：%s", r.Content)
	}
}

// ── 注册表 ──

func TestRegistryExecuteAndAlias(t *testing.T) {
	root := t.TempDir()
	r := NewDefaultRegistry(Options{Cwd: root})

	// 直接调用
	if _, err := r.Execute(context.Background(), "read_file", call(root, map[string]any{"path": "x"})); err != nil {
		t.Errorf("read_file 应可调用：%v", err)
	}
	// 未知名应给 did-you-mean
	_, err := r.Execute(context.Background(), "read_fil", call(root, nil))
	if err == nil {
		t.Fatal("未知名应报错")
	}
	if !strings.Contains(err.Error(), "Did you mean") {
		t.Errorf("应给 did-you-mean：%v", err)
	}
}

// 反证 F：别名必须在门禁之前解析（否则 deny 规则可被绕过）。
func TestAliasResolvedBeforeGates(t *testing.T) {
	root := t.TempDir()
	r := NewDefaultRegistry(Options{Cwd: root})

	// 注册一个 delegate_task 以便别名有目标
	r.Register(newStub("delegate_task"))

	if got := r.ResolveName("task"); got != "delegate_task" {
		t.Errorf("ResolveName(task) = %q, want delegate_task", got)
	}
	if got := r.ResolveName("delegate_task"); got != "delegate_task" {
		t.Errorf("规范名不应被改写：%q", got)
	}
	if got := r.ResolveName("unknown_thing"); got != "unknown_thing" {
		t.Errorf("无别名时应原样返回：%q", got)
	}
}

// Definitions 按名升序（请求体字节稳定的前提）。
func TestDefinitionsSorted(t *testing.T) {
	root := t.TempDir()
	r := NewDefaultRegistry(Options{Cwd: root})
	defs := r.Definitions()
	if len(defs) < 5 {
		t.Fatalf("应至少注册 5 个工具，实际 %d", len(defs))
	}
	for i := 1; i < len(defs); i++ {
		if defs[i-1].Name > defs[i].Name {
			t.Fatalf("definitions 未按名升序：%s > %s", defs[i-1].Name, defs[i].Name)
		}
	}
}

// Filter：白名单含未注册工具名应报错（fail-closed）。
func TestFilterFailClosed(t *testing.T) {
	root := t.TempDir()
	r := NewDefaultRegistry(Options{Cwd: root})

	if _, err := Filter(r, []string{"read_file", "grep"}); err != nil {
		t.Errorf("合法白名单应通过：%v", err)
	}
	if _, err := Filter(r, []string{"read_file", "no_such_tool"}); err == nil {
		t.Error("白名单含未注册工具应报错（静默会掩盖拼写错误）")
	}
}

// stubTool 是测试用的最小工具。
type stubTool struct {
	baseTool
}

func (s *stubTool) Execute(context.Context, *CallParams) (contract.Result, error) {
	return contract.Result{Content: "stub"}, nil
}

func (s *stubTool) Timeout(*CallParams) time.Duration { return 0 }

// newStub 构造一个启用中的最小工具。
func newStub(name string) Tool {
	s := &stubTool{}
	s.def = contract.Definition{Name: name, Description: "stub tool"}
	s.enabled = true
	return s
}
