package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// projmap_oracle_test.go —— repo_map / inspect_project / file_info 的差分对账。
//
// oracle 由 `testdata/projmap/gen-oracle.ts` 真跑 TS 产出。**夹具路径固定**
// （`testdata/projmap/fixtures/`），两侧用同一套建树逻辑，故可逐字节比对。

const projmapOraclePath = "../../testdata/projmap/oracle.json"
const projmapFixturesDir = "../../testdata/projmap/fixtures"

type classifyCase struct {
	Input  string `json:"input"`
	Tier   string `json:"tier"`
	Silent bool   `json:"silent"`
	Reason string `json:"reason"`
}

type scanExcludeCase struct {
	Name     string `json:"name"`
	Excluded bool   `json:"excluded"`
}

type repoMapCase struct {
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Content string          `json:"content"`
	IsError bool            `json:"isError"`
}

type fileInfoCase struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Content   string `json:"content"`
	UIContent string `json:"uiContent"`
	IsError   bool   `json:"isError"`
}

type permCase struct {
	Mode     uint32 `json:"mode"`
	Platform string `json:"platform"`
	Out      string `json:"out"`
}

type projmapOracle struct {
	ClassifyPath      []classifyCase    `json:"classifyPath"`
	ScanExcludes      []scanExcludeCase `json:"scanExcludes"`
	ScanExcludesProbe []scanExcludeCase `json:"scanExcludesProbe"`
	RepoMap           []repoMapCase     `json:"repoMap"`
	InspectProject    struct {
		Content string `json:"content"`
		IsError bool   `json:"isError"`
	} `json:"inspectProject"`
	InspectProjectNonNode struct {
		Content string `json:"content"`
		IsError bool   `json:"isError"`
	} `json:"inspectProjectNonNode"`
	FileInfo       []fileInfoCase `json:"fileInfo"`
	FileInfoNoPath struct {
		Content string `json:"content"`
		IsError bool   `json:"isError"`
	} `json:"fileInfoNoPath"`
	FormatPermissions []permCase `json:"formatPermissions"`
}

func loadProjmapOracle(t *testing.T) *projmapOracle {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(projmapOraclePath))
	if err != nil {
		t.Fatalf("读取 oracle 失败（需先跑 gen-oracle.ts）：%v", err)
	}
	var o projmapOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return &o
}

// ── classifyPath ──

// TestOracleClassifyPath —— 注意力分级全分支对账。
func TestOracleClassifyPath(t *testing.T) {
	o := loadProjmapOracle(t)
	if len(o.ClassifyPath) == 0 {
		t.Fatal("oracle 无 classifyPath 用例")
	}
	for _, c := range o.ClassifyPath {
		got := ClassifyPath(c.Input)
		if string(got.Tier) != c.Tier || got.Silent != c.Silent || got.Reason != c.Reason {
			t.Errorf("ClassifyPath(%q)\n  TS: tier=%s silent=%v reason=%s\n  Go: tier=%s silent=%v reason=%s",
				c.Input, c.Tier, c.Silent, c.Reason, got.Tier, got.Silent, got.Reason)
		}
	}
	t.Logf("classifyPath 对账 %d 例", len(o.ClassifyPath))
}

// TestOracleScanExcludes —— 剪枝基线对账。
func TestOracleScanExcludes(t *testing.T) {
	o := loadProjmapOracle(t)
	for _, c := range o.ScanExcludes {
		if got := IsScanExcludedDir(c.Name); got != c.Excluded {
			t.Errorf("IsScanExcludedDir(%q) TS=%v Go=%v", c.Name, c.Excluded, got)
		}
	}
	for _, c := range o.ScanExcludesProbe {
		if got := IsScanExcludedDir(c.Name); got != c.Excluded {
			t.Errorf("IsScanExcludedDir(%q) TS=%v Go=%v", c.Name, c.Excluded, got)
		}
	}
	// 基线集合大小一致（防漏项）。
	if len(ScanExcludeDirs) != len(o.ScanExcludes) {
		t.Errorf("基线大小不符：TS=%d Go=%d", len(o.ScanExcludes), len(ScanExcludeDirs))
	}
	t.Logf("scanExcludes 对账 %d+%d 例", len(o.ScanExcludes), len(o.ScanExcludesProbe))
}

// ── repo_map ──

// makeRepoMapFixture 建出与 oracle 完全相同的目录树。
//
// **必须与 gen-oracle.ts 的 `mk` 调用序列一致**——任一处增删都会让对账失效。
func makeRepoMapFixture(t *testing.T) string {
	t.Helper()
	root := filepath.Join(filepath.FromSlash(projmapFixturesDir), "repomap")
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}

	mk := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	mk("src/main.ts", "x")
	mk("src/index.tsx", "x")
	mk("src/app.tsx", "x")
	mk("src/agent/loop.ts", "x")
	mk("src/agent/__tests__/loop.test.ts", "x")
	mk("src/agent/helper.spec.ts", "x")
	mk("tsconfig.json", "x")
	mk("package.json", "x")
	mk("vite.config.ts", "x")
	mk("README.md", "x")
	mk("docs/guide.md", "x")
	mk("a.ts", "x")
	mk("z.ts", "x")
	mk("m.ts", "x")
	mk("node_modules/pkg/index.js", "x")
	mk("dist/out.js", "x")
	mk(".git/config", "x")
	mk("target/debug/app", "x")
	mk(".gitignore", "x")
	mk(".env.example", "x")
	mk(".hidden/secret.ts", "x")
	mk("x.log", "x")
	mk(".rivet/sessions/s.jsonl", "x")
	if err := os.MkdirAll(filepath.Join(root, "emptydir"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestOracleRepoMap —— repo_map 逐例对账（**逐字节比对 content**）。
//
// **已知偏离（在 Windows 上）**：`path-*` 用例的 TS 期望值**全部是
// 「path 必须位于项目目录内」**——这是 TS 的 **Windows 缺陷**，不是意图：
//
//	TS: const safeCwd = resolve(params.cwd) + '/'
//	    → Windows 上 resolve 返回反斜杠路径，拼 '/' 得混合分隔符
//	      （`...\repomap/`），而 root 是纯反斜杠 → startsWith **恒为 false**
//
// 实测确认（tsx 探针）：
//
//	root      = "D:\\...\\repomap\\src"
//	safeCwd   = "D:\\...\\repomap/"
//	startsWith= false
//
// 即 **`repo_map({path: ...})` 在 Windows 上完全不可用**。作者在 macOS/Linux
// 开发（那里 `+ '/'` 恰好正确），故该缺陷从未暴露。
//
// Go 侧用 `filepath.Separator` 平台无关地表达「路径必须在 cwd 子树内」的
// **意图**，故这些用例在 Go 侧是**成功路径**。偏离已在
// `knownRepoMapDeviations` 登记，并在此**单独断言 Go 侧的真实行为**——
// 不静默跳过，也不假装与 TS 一致。
var knownRepoMapDeviations = map[string]string{
	"path-src":         "TS 在 Windows 上因分隔符拼接缺陷恒报错；Go 修正为真实子树（成功）",
	"path-agent":       "同上（成功）",
	"path-hidden":      "同上（成功）",
	"path-nonexistent": "TS 恒报「必须在项目内」；Go 修正后走到「目录不存在」（仍报错，原因不同）",
	"path-escape":      "TS 恒报「必须在项目内」；Go 修正后**同样**报该错（成因不同但文案一致）",
}

// repoMapDeviationExpectError 列出偏离用例中**期望仍报错**的项。
//
// **不能一刀切「偏离用例都应成功」**——`path-nonexistent` 的目录真不存在，
// 修正后自然走到「目录不存在」分支；`path-escape` 则必须继续被拒（安全语义）。
var repoMapDeviationExpectError = map[string]bool{
	"path-nonexistent": true,
	"path-escape":      true,
}

func TestOracleRepoMap(t *testing.T) {
	o := loadProjmapOracle(t)
	if len(o.RepoMap) == 0 {
		t.Fatal("oracle 无 repoMap 用例")
	}
	root := makeRepoMapFixture(t)

	for _, c := range o.RepoMap {
		var input map[string]any
		if len(c.Input) > 0 {
			if err := json.Unmarshal(c.Input, &input); err != nil {
				t.Fatalf("[%s] 解析 input 失败：%v", c.Name, err)
			}
		}
		res, err := RepoMap().Execute(context.Background(), &CallParams{Cwd: root, Input: input})
		if err != nil {
			t.Fatalf("[%s] Execute 报错：%v", c.Name, err)
		}

		if _, deviating := knownRepoMapDeviations[c.Name]; deviating {
			// **登记偏离**：TS 期望值是 Windows 缺陷产物，Go 侧是修正后的行为。
			// 此处按**每项的期望方向**断言（不比对 content）：
			//   - 期望报错的项（不存在 / 逃逸）：必须仍报错，且逃逸的文案须一致
			//   - 期望成功的项（真实子树）：必须成功——这正是修正的意义
			if repoMapDeviationExpectError[c.Name] {
				if !res.IsError {
					t.Errorf("[%s] 应仍被拒绝，实得成功：%q", c.Name, res.Content)
				}
				if c.Name == "path-escape" && res.Content != c.Content {
					t.Errorf("[%s] 逃逸用例文案应一致\nTS: %q\nGo: %q", c.Name, c.Content, res.Content)
				}
				continue
			}
			if res.IsError {
				t.Errorf("[%s] 修正后应成功（TS 在 Windows 上恒报错是缺陷），实得：%q", c.Name, res.Content)
			}
			continue
		}

		if res.IsError != c.IsError {
			t.Errorf("[%s] isError TS=%v Go=%v（内容：%q）", c.Name, c.IsError, res.IsError, res.Content)
		}
		if res.Content != c.Content {
			t.Errorf("[%s] content 不一致\n--- TS ---\n%s\n--- Go ---\n%s", c.Name, c.Content, res.Content)
		}
	}
	t.Logf("repoMap 对账 %d 例（其中 %d 例为登记的 Windows 偏离）", len(o.RepoMap), len(knownRepoMapDeviations))
}

// TestRepoMapPathOnWindowsWorks —— **修正后的行为**：`path` 参数真实可用。
//
// 这是 TS 在 Windows 上做不到的。锁定 Go 侧的正确性。
func TestRepoMapPathOnWindowsWorks(t *testing.T) {
	root := makeRepoMapFixture(t)
	res, err := RepoMap().Execute(context.Background(), &CallParams{
		Cwd: root, Input: map[string]any{"path": "src"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("path 应可用（TS 在 Windows 上不可用是本刀修正的缺陷），实得：%q", res.Content)
	}
	// header 应是「<cwd base>/src/」，**不含绝对路径**。
	firstLine := strings.SplitN(res.Content, "\n", 2)[0]
	wantHeader := "repomap/src/"
	if firstLine != wantHeader {
		t.Errorf("header 应为 %q（不含绝对路径），实得 %q", wantHeader, firstLine)
	}
	if !strings.Contains(res.Content, "loop.ts") {
		t.Errorf("应含 src 下的文件：%q", res.Content)
	}
}

// ── inspect_project ──

// makeInspectFixture 建出与 oracle 的 `inspectRoot` 段相同的 Node 项目夹具。
func makeInspectFixture(t *testing.T) string {
	t.Helper()
	root := filepath.Join(filepath.FromSlash(projmapFixturesDir), "inspect")
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	pkg := `{
  "name": "oracle-fixture",
  "scripts": {
    "build": "tsc",
    "test": "node --test",
    "lint": "eslint .",
    "dev": "tsx watch",
    "start": "node .",
    "typecheck": "tsc --noEmit",
    "other": "echo x"
  },
  "dependencies": {
    "react": "^18",
    "express": "^4"
  },
  "devDependencies": {
    "typescript": "^5",
    "vitest": "^1",
    "eslint": "^8"
  }
}`
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(pkg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pnpm-lock.yaml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "index.ts"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestOracleInspectProject —— inspect_project 逐字节对账。
//
// 夹具必须与 `gen-oracle.ts` 的 `inspectRoot` 段**逐项一致**。
func TestOracleInspectProject(t *testing.T) {
	o := loadProjmapOracle(t)
	root := makeInspectFixture(t)
	res, err := InspectProject().Execute(context.Background(), &CallParams{Cwd: root, Input: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != o.InspectProject.Content {
		t.Errorf("content 不一致\n--- TS ---\n%s\n--- Go ---\n%s", o.InspectProject.Content, res.Content)
	}
	if res.IsError != o.InspectProject.IsError {
		t.Errorf("isError TS=%v Go=%v", o.InspectProject.IsError, res.IsError)
	}
}

// TestOracleInspectProjectNonNode —— 非 Node 项目的固定文案。
//
// **这条独立于目录内容**——只验「无 package.json」的分支。
func TestOracleInspectProjectNonNode(t *testing.T) {
	o := loadProjmapOracle(t)
	dir := filepath.Join(filepath.FromSlash(projmapFixturesDir), "nonnode")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := InspectProject().Execute(context.Background(), &CallParams{Cwd: dir, Input: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != o.InspectProjectNonNode.Content {
		t.Errorf("非 Node 项目文案不符\nTS: %q\nGo: %q", o.InspectProjectNonNode.Content, res.Content)
	}
	if res.IsError != o.InspectProjectNonNode.IsError {
		t.Errorf("isError TS=%v Go=%v", o.InspectProjectNonNode.IsError, res.IsError)
	}
}

// ── file_info ──

// makeFileInfoFixture 建出与 oracle 相同的 file_info 夹具。
func makeFileInfoFixture(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(filepath.FromSlash(projmapFixturesDir), "fileinfo")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel string, data []byte) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.ts", []byte("const a = 1\n"))
	write("b.png", []byte{0x89, 0x50, 0x4e, 0x47})
	write("empty.txt", nil)
	write("big.bin", make([]byte, 2048))
	write("sub/x.ts", []byte("x"))
	write("sub/y.log", []byte("y"))
	write("sub/node_modules/p.js", []byte("p"))
	write("sub/.hidden/h.ts", []byte("h"))
	return dir
}

// TestOracleFileInfo —— file_info 逐例对账（**除 Modified 行**，见下）。
//
// **已知不可逐字节比对的字段**：`Modified:` 是文件 mtime——两侧建树时刻不同，
// 必然不同。故比对时**剔除该行**，其余逐字节比对。
func TestOracleFileInfo(t *testing.T) {
	o := loadProjmapOracle(t)
	if len(o.FileInfo) == 0 {
		t.Fatal("oracle 无 fileInfo 用例")
	}
	dir := makeFileInfoFixture(t)

	for _, c := range o.FileInfo {
		res, err := FileInfo().Execute(context.Background(), &CallParams{
			Cwd: dir, Input: map[string]any{"path": c.Path},
		})
		if err != nil {
			t.Fatalf("[%s] Execute 报错：%v", c.Name, err)
		}
		got := stripModifiedLine(res.Content)
		want := stripModifiedLine(c.Content)
		if got != want {
			t.Errorf("[%s] content 不一致（已剔 Modified 行）\n--- TS ---\n%s\n--- Go ---\n%s", c.Name, want, got)
		}
		if res.IsError != c.IsError {
			t.Errorf("[%s] isError TS=%v Go=%v", c.Name, c.IsError, res.IsError)
		}
		// uiContent：oracle 为 null 时 Go 应为空串。
		if c.UIContent == "" && res.UIContent != "" {
			t.Errorf("[%s] uiContent 应空，实得 %q", c.Name, res.UIContent)
		}
	}
	t.Logf("fileInfo 对账 %d 例", len(o.FileInfo))
}

// stripModifiedLine 剔除 `Modified:` 行（mtime 不可比对）。
func stripModifiedLine(s string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "Modified:") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// TestOracleFileInfoNoPath —— 缺 path 参数的错误文案。
func TestOracleFileInfoNoPath(t *testing.T) {
	o := loadProjmapOracle(t)
	res, err := FileInfo().Execute(context.Background(), &CallParams{
		Cwd: t.TempDir(), Input: map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != o.FileInfoNoPath.Content {
		t.Errorf("文案不符\nTS: %q\nGo: %q", o.FileInfoNoPath.Content, res.Content)
	}
}

// TestOracleFormatPermissions —— 权限格式化的**平台分叉**对账。
//
// 覆盖 Windows（read-write/read-only）与非 Windows（八进制）两分支。
func TestOracleFormatPermissions(t *testing.T) {
	o := loadProjmapOracle(t)
	if len(o.FormatPermissions) == 0 {
		t.Fatal("oracle 无 formatPermissions 用例")
	}
	for _, c := range o.FormatPermissions {
		got := FormatPermissions(os.FileMode(c.Mode), c.Platform)
		if got != c.Out {
			t.Errorf("FormatPermissions(%o, %s) TS=%q Go=%q", c.Mode, c.Platform, c.Out, got)
		}
	}
	t.Logf("formatPermissions 对账 %d 例", len(o.FormatPermissions))
}
