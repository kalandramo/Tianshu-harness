package prompt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// verifyOracle 是 TS 侧真实 renderDeclaredVerify 的产出（经真实读取路径）。
// 生成命令：npx tsx go/testdata/verifycmds/gen-oracle.ts
type verifyOracle struct {
	Cases map[string]struct {
		Verify json.RawMessage `json:"verify"`
		Out    *string         `json:"out"`
		Note   string          `json:"note"`
	} `json:"cases"`
}

func loadVerifyOracle(t *testing.T) verifyOracle {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "verifycmds", "oracle.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败（%s）：%v\n生成命令：npx tsx go/testdata/verifycmds/gen-oracle.ts", path, err)
	}
	var o verifyOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return o
}

// TestRenderDeclaredVerifyParity —— verify-commands 渲染与 TS 逐字节相同。
func TestRenderDeclaredVerifyParity(t *testing.T) {
	o := loadVerifyOracle(t)
	if len(o.Cases) == 0 {
		t.Fatal("oracle 无用例")
	}
	for name, c := range o.Cases {
		t.Run(name, func(t *testing.T) {
			var cfg VerifyConfig
			if err := json.Unmarshal(c.Verify, &cfg); err != nil {
				t.Fatalf("解析 verify config 失败：%v", err)
			}
			got := RenderDeclaredVerify(cfg)
			want := ""
			if c.Out != nil {
				want = *c.Out
			}
			if got != want {
				t.Errorf("不等价\n  Go =%q\n  TS =%q", got, want)
			}
		})
	}
}

// TestRenderDeclaredVerifyKindOrder —— 四种 kind 的输出顺序固定，
// 与 config 里的书写顺序无关。
func TestRenderDeclaredVerifyKindOrder(t *testing.T) {
	// 故意逆序书写
	cfg := VerifyConfig{Lint: "L", Typecheck: "T", Build: "B", Test: "X"}
	got := RenderDeclaredVerify(cfg)
	idxTest := indexOf(got, "test: X")
	idxBuild := indexOf(got, "build: B")
	idxType := indexOf(got, "typecheck: T")
	idxLint := indexOf(got, "lint: L")
	if !(idxTest < idxBuild && idxBuild < idxType && idxType < idxLint) {
		t.Errorf("顺序应为 test→build→typecheck→lint，实际位置 %d/%d/%d/%d\n%s",
			idxTest, idxBuild, idxType, idxLint, got)
	}
}

// TestRenderDeclaredVerifyFiltersBlank —— trim 后为空的项应被过滤。
func TestRenderDeclaredVerifyFiltersBlank(t *testing.T) {
	cfg := VerifyConfig{Test: "   ", Build: "\t\n", Typecheck: "tsc"}
	got := RenderDeclaredVerify(cfg)
	if contains(got, "test:") {
		t.Errorf("空白 test 应被过滤，实际 %q", got)
	}
	if contains(got, "build:") {
		t.Errorf("空白 build 应被过滤，实际 %q", got)
	}
	if !contains(got, "typecheck: tsc") {
		t.Errorf("应保留 typecheck，实际 %q", got)
	}
}

// TestRenderDeclaredVerifyEmpty —— 无声明时返回空串。
func TestRenderDeclaredVerifyEmpty(t *testing.T) {
	if got := RenderDeclaredVerify(VerifyConfig{}); got != "" {
		t.Errorf("空 config 应返回空串，实际 %q", got)
	}
	if got := RenderDeclaredVerify(VerifyConfig{Routes: []VerifyRoute{}}); got != "" {
		t.Errorf("空 routes 应返回空串，实际 %q", got)
	}
}

// TestRenderDeclaredVerifyEscaping —— 内容必须经 escapeXml。
func TestRenderDeclaredVerifyEscaping(t *testing.T) {
	cfg := VerifyConfig{Test: `echo "<x>" && true`}
	got := RenderDeclaredVerify(cfg)
	if contains(got, "<x>") {
		t.Error("尖括号应被转义")
	}
	if !contains(got, "&lt;x&gt;") {
		t.Error("应含转义后的 &lt;x&gt;")
	}
	if contains(got, " && ") {
		t.Error("裸 & 应被转义为 &amp;")
	}
}

// TestRenderDeclaredVerifyRoutes —— routes 追加在四种 kind 之后。
func TestRenderDeclaredVerifyRoutes(t *testing.T) {
	cfg := VerifyConfig{
		Test:   "npm test",
		Routes: []VerifyRoute{{Match: "src/**", Run: "eslint", Kind: "lint"}},
	}
	got := RenderDeclaredVerify(cfg)
	idxTest := indexOf(got, "test: npm test")
	idxRoute := indexOf(got, "lint [src/**]: eslint")
	if !(idxTest >= 0 && idxRoute > idxTest) {
		t.Errorf("routes 应在四种 kind 之后，实际 %q", got)
	}
}

// TestLoadDeclaredVerify —— 从 .rivet-config.json 读取（含向上查找）。
func TestLoadDeclaredVerify(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, projectConfigFile)
	content := `{"verify":{"test":"npm test","routes":[{"match":"a/**","run":"x","kind":"lint"}]}}`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	got := LoadDeclaredVerify(dir)
	if got.Test != "npm test" {
		t.Errorf("test 应为 npm test，实际 %q", got.Test)
	}
	if len(got.Routes) != 1 || got.Routes[0].Match != "a/**" {
		t.Errorf("routes 解析错误：%+v", got.Routes)
	}
}

// TestLoadDeclaredVerifyUpwardSearch —— 从子目录应能找到祖先的 config。
func TestLoadDeclaredVerifyUpwardSearch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, projectConfigFile),
		[]byte(`{"verify":{"build":"make"}}`), 0644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "a", "b", "c")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}

	got := LoadDeclaredVerify(sub)
	if got.Build != "make" {
		t.Errorf("应从祖先目录找到 config，实际 %+v", got)
	}
}

// TestLoadDeclaredVerifyMissingAndMalformed —— 缺失/非法 JSON 均返回空 config。
func TestLoadDeclaredVerifyMissingAndMalformed(t *testing.T) {
	dir := t.TempDir()
	if got := LoadDeclaredVerify(dir); got.Test != "" || got.Build != "" {
		t.Errorf("无 config 应返回空，实际 %+v", got)
	}

	if err := os.WriteFile(filepath.Join(dir, projectConfigFile), []byte("{ not json"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := LoadDeclaredVerify(dir); got.Test != "" {
		t.Errorf("非法 JSON 应返回空，实际 %+v", got)
	}

	// 无 verify 节
	if err := os.WriteFile(filepath.Join(dir, projectConfigFile), []byte(`{"other":1}`), 0644); err != nil {
		t.Fatal(err)
	}
	if got := LoadDeclaredVerify(dir); got.Test != "" {
		t.Errorf("无 verify 节应返回空，实际 %+v", got)
	}
}

// TestDetectDeclaredVerifyBlock —— 读取+渲染的生产入口。
func TestDetectDeclaredVerifyBlock(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, projectConfigFile),
		[]byte(`{"verify":{"test":"go test ./..."}}`), 0644); err != nil {
		t.Fatal(err)
	}
	got := DetectDeclaredVerifyBlock(dir)
	if !contains(got, "test: go test ./...") {
		t.Errorf("应渲染出 test 声明，实际 %q", got)
	}
	if !contains(got, `<verify-commands source=".rivet-config.json">`) {
		t.Errorf("应含包裹标签，实际 %q", got)
	}
}

// TestLoadDeclaredVerifySearchDepthLimit —— 向上查找的**层数上限**（20）。
//
// 这是边界测试：M4 变异（20→1）首轮红 0 处的原因是用例只建了 3 层，
// 改上界后仍能查到。需构造**恰好跨越上界**的场景。
//
// 语义（对账 TS findProjectConfig）：从 startDir 起最多检查 20 个目录
// （含自身），即 config 在 20 层内可找到、第 21 层起找不到。
func TestLoadDeclaredVerifySearchDepthLimit(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, projectConfigFile),
		[]byte(`{"verify":{"test":"from-root"}}`), 0644); err != nil {
		t.Fatal(err)
	}

	// 构造 19 层子目录（root + 19 = 第 20 层是 startDir 本身 → 可达）
	deep19 := root
	for i := 0; i < 19; i++ {
		deep19 = filepath.Join(deep19, "d")
	}
	if err := os.MkdirAll(deep19, 0755); err != nil {
		t.Fatal(err)
	}
	if got := LoadDeclaredVerify(deep19); got.Test != "from-root" {
		t.Errorf("19 层子目录应能查到 root 的 config（第 20 个候选），实际 %+v", got)
	}

	// 再深一层（20 层子目录 → startDir 是第 21 个候选 → 超出上限）
	tooDeep := filepath.Join(deep19, "d")
	if err := os.MkdirAll(tooDeep, 0755); err != nil {
		t.Fatal(err)
	}
	if got := LoadDeclaredVerify(tooDeep); got.Test != "" {
		t.Errorf("20 层子目录应超出查找上限（返回空），实际 %+v", got)
	}
}
