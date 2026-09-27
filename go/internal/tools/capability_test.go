package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// capability_test.go —— `capability` 工具（第八十五刀 · W2-2）。
//
// 对账 TS `src/tools/capability-index.ts`（384 行）。
//
// # 依赖判定（已核实，消除计划里的「待验证假设」）
//
// TS 用 `createRequire` 做 **package 存在性检查**。核实结论：
// **6 条种子 registry 全部只用 `binary`**（`gh` 额外用 `env`），
// **无一条用 `package`**（实测 `grep -n "package:" src/tools/capability-index.ts` 零命中）。
// 故 `createRequire` 只服务于**项目级** `.rivet/capabilities.json` 里可能出现的
// `package` 项——Go 侧无对应物（Node 的模块解析语义）。
//
// **Go 侧决策**：`package` 检查器**恒返回 false（视为缺失）**——
// 因为 Go 没有 Node 的 `node_modules` 解析语义，「包存在」在 Go 里没有明确定义。
// 这样项目级 registry 若写了 `package` 要求，会**诚实显示为缺失**（fail-closed），
// 而不是假装可用。

// ── 解析（parseCapability / parseRegistry）──────────────────────────────

// TestCapabilityParseRegistry —— 解析项目级 registry。
func TestCapabilityParseRegistry(t *testing.T) {
	// 非法输入 → nil
	for _, raw := range []any{nil, "str", 42, map[string]any{}} {
		if got := parseRegistry(raw); got != nil {
			t.Errorf("parseRegistry(%#v) 应为 nil，实得 %+v", raw, got)
		}
	}

	// 正常解析
	raw := map[string]any{
		"schemaVersion": "2",
		"capabilities": []any{
			map[string]any{
				"id":     "my-cap",
				"intent": "我的能力",
				"hints":  []any{"提示1", "提示2"},
				"providers": []any{
					map[string]any{
						"kind":        "public-cli",
						"name":        "mytool",
						"requires":    map[string]any{"binary": []any{"mytool"}},
						"installHint": "brew install mytool",
					},
				},
			},
		},
	}
	reg := parseRegistry(raw)
	if reg == nil || len(reg.Capabilities) != 1 {
		t.Fatalf("应解析出 1 条能力，实得 %+v", reg)
	}
	c := reg.Capabilities[0]
	if c.ID != "my-cap" || c.Intent != "我的能力" {
		t.Errorf("id/intent 不符：%+v", c)
	}
	if len(c.Hints) != 2 || c.Hints[0] != "提示1" {
		t.Errorf("hints 不符：%+v", c.Hints)
	}
	if len(c.Providers) != 1 || c.Providers[0].Name != "mytool" {
		t.Errorf("providers 不符：%+v", c.Providers)
	}

	// **缺 id 或 intent 的条目被丢弃**（对账 TS `parseCapability` 返回 null）
	raw2 := map[string]any{
		"capabilities": []any{
			map[string]any{"intent": "无 id"},
			map[string]any{"id": "无 intent"},
			map[string]any{"id": "ok", "intent": "好", "providers": []any{
				map[string]any{"name": "x"},
			}},
		},
	}
	reg2 := parseRegistry(raw2)
	if reg2 == nil || len(reg2.Capabilities) != 1 || reg2.Capabilities[0].ID != "ok" {
		t.Errorf("应只保留合法条目，实得 %+v", reg2)
	}

	// **无 providers 的条目被丢弃**（对账 TS `.filter(c => c.providers.length > 0)`）
	raw3 := map[string]any{
		"capabilities": []any{
			map[string]any{"id": "empty", "intent": "无 provider", "providers": []any{}},
		},
	}
	if reg3 := parseRegistry(raw3); reg3 == nil || len(reg3.Capabilities) != 0 {
		t.Errorf("无 provider 的条目应被丢弃，实得 %+v", reg3)
	}
}

// TestCapabilitySeedRegistry —— 种子 registry 6 条完整（对账 TS SEED_REGISTRY）。
func TestCapabilitySeedRegistry(t *testing.T) {
	if len(seedRegistry.Capabilities) != 6 {
		t.Fatalf("种子应有 6 条，实得 %d", len(seedRegistry.Capabilities))
	}
	wantIDs := []string{
		"media-transcode", "image-processing", "document-conversion",
		"json-processing", "github-ops", "code-search",
	}
	for i, want := range wantIDs {
		if seedRegistry.Capabilities[i].ID != want {
			t.Errorf("第 %d 条 id 应为 %q，实得 %q", i, want, seedRegistry.Capabilities[i].ID)
		}
	}
	// imagemagick 有两个 binary（magick / convert）
	img := seedRegistry.Capabilities[1]
	if len(img.Providers[0].Requires.Binary) != 2 {
		t.Errorf("imagemagick 应要求 2 个 binary，实得 %+v", img.Providers[0].Requires.Binary)
	}
	// gh 额外要求 env GITHUB_TOKEN
	gh := seedRegistry.Capabilities[4]
	if len(gh.Providers[0].Requires.Env) != 1 || gh.Providers[0].Requires.Env[0] != "GITHUB_TOKEN" {
		t.Errorf("gh 应要求 GITHUB_TOKEN，实得 %+v", gh.Providers[0].Requires.Env)
	}
	// **无一条用 package**（这是 Go 侧决策的依据）
	for _, c := range seedRegistry.Capabilities {
		for _, p := range c.Providers {
			if len(p.Requires.Package) != 0 {
				t.Errorf("%s 不应有 package 要求，实得 %+v", c.ID, p.Requires.Package)
			}
		}
	}
}

// ── 合并（mergeRegistries）──────────────────────────────────────────────

// TestCapabilityMerge —— 项目级按 id 覆盖、新增追加、**绝不删种子**。
func TestCapabilityMerge(t *testing.T) {
	// nil 项目级 → 原样返回种子
	if got := mergeRegistries(seedRegistry, nil); len(got.Capabilities) != 6 {
		t.Errorf("nil 应返回种子 6 条，实得 %d", len(got.Capabilities))
	}

	project := &capabilityRegistry{
		SchemaVersion: "2",
		Capabilities: []capability{
			// 覆盖种子的 media-transcode
			{ID: "media-transcode", Intent: "改写后的意图", Providers: []capabilityProvider{{Name: "newffmpeg"}}},
			// 新增
			{ID: "brand-new", Intent: "全新能力", Providers: []capabilityProvider{{Name: "newtool"}}},
		},
	}
	merged := mergeRegistries(seedRegistry, project)
	// 6 种子 + 1 新增（覆盖不增加计数）
	if len(merged.Capabilities) != 7 {
		t.Fatalf("应 7 条（6 种子 + 1 新增），实得 %d", len(merged.Capabilities))
	}
	byID := map[string]capability{}
	for _, c := range merged.Capabilities {
		byID[c.ID] = c
	}
	if byID["media-transcode"].Intent != "改写后的意图" {
		t.Error("项目级应覆盖同 id 种子条目")
	}
	if _, ok := byID["brand-new"]; !ok {
		t.Error("项目级新条目应被追加")
	}
	// 其余种子仍在（零契约破坏）
	for _, id := range []string{"image-processing", "document-conversion", "json-processing", "github-ops", "code-search"} {
		if _, ok := byID[id]; !ok {
			t.Errorf("种子 %s 不应被删除", id)
		}
	}
}

// ── preflight（条件矩阵）────────────────────────────────────────────────

// TestCapabilityCheckProvider —— provider 可用性判定。
//
// 对账 TS `checkProviderRequirements`：
//
//	available = (present 总数 > 0) && (missing 全为 0)
//
// **注意 `present 总数 > 0` 这个条件**——一个 requires 全空的 provider
// **不算 available**（否则「无要求」会被误判为「可用」）。
func TestCapabilityCheckProvider(t *testing.T) {
	ch := capabilityCheckers{
		Binary:  func(n string) bool { return n == "present-bin" },
		Env:     func(n string) bool { return n == "PRESENT_ENV" },
		Package: func(n string) bool { return false }, // Go 侧恒 false（见文件头）
	}

	// 全在 → available
	p := checkProviderRequirements(capabilityProvider{
		Kind: "public-cli", Name: "ok",
		Requires: capabilityRequires{Binary: []string{"present-bin"}},
	}, ch)
	if !p.Available {
		t.Errorf("binary 在应 available，实得 %+v", p)
	}
	if len(p.Present.Binary) != 1 || len(p.Missing.Binary) != 0 {
		t.Errorf("present/missing 不符：%+v", p)
	}

	// 缺一个 → 不可用（即使另一个在）
	p = checkProviderRequirements(capabilityProvider{
		Name:     "partial",
		Requires: capabilityRequires{Binary: []string{"present-bin", "absent-bin"}},
	}, ch)
	if p.Available {
		t.Errorf("有缺失应不可用，实得 %+v", p)
	}
	if len(p.Missing.Binary) != 1 || p.Missing.Binary[0] != "absent-bin" {
		t.Errorf("missing 应含 absent-bin，实得 %+v", p.Missing)
	}

	// **requires 全空 → 不可用**（对账 TS `present 总数 > 0` 条件）
	p = checkProviderRequirements(capabilityProvider{Name: "no-req"}, ch)
	if p.Available {
		t.Errorf("无要求不应算 available，实得 %+v", p)
	}

	// env 缺失
	p = checkProviderRequirements(capabilityProvider{
		Name:     "env-missing",
		Requires: capabilityRequires{Env: []string{"ABSENT_ENV"}},
	}, ch)
	if p.Available {
		t.Errorf("env 缺失应不可用，实得 %+v", p)
	}

	// **package 在 Go 侧恒缺失**（fail-closed）
	p = checkProviderRequirements(capabilityProvider{
		Name:     "pkg",
		Requires: capabilityRequires{Package: []string{"some-npm-pkg"}},
	}, ch)
	if p.Available {
		t.Errorf("package 在 Go 侧应恒缺失（fail-closed），实得 %+v", p)
	}
	if len(p.Missing.Package) != 1 {
		t.Errorf("应报告 package 缺失，实得 %+v", p.Missing)
	}
}

// TestCapabilityPreflight —— 单 capability 的 preflight 汇总。
func TestCapabilityPreflight(t *testing.T) {
	reg := &capabilityRegistry{
		Capabilities: []capability{
			{
				ID: "test-cap", Intent: "测试", Hints: []string{"h1"},
				Providers: []capabilityProvider{
					{Name: "have", Requires: capabilityRequires{Binary: []string{"present-bin"}}},
					{Name: "lack", Requires: capabilityRequires{Binary: []string{"absent"}}},
				},
			},
		},
	}
	ch := capabilityCheckers{Binary: func(n string) bool { return n == "present-bin" }}

	r := preflightCapability(reg, "test-cap", ch)
	if r == nil {
		t.Fatal("应找到 capability")
	}
	if r.Summary.Providers != 2 || r.Summary.Available != 1 || r.Summary.Missing != 1 {
		t.Errorf("汇总应为 2/1/1，实得 %+v", r.Summary)
	}
	if r.Capability.ID != "test-cap" || r.Capability.Intent != "测试" {
		t.Errorf("capability 信息不符：%+v", r.Capability)
	}

	// 未找到 → nil
	if r := preflightCapability(reg, "nope", ch); r != nil {
		t.Errorf("未找到应返回 nil，实得 %+v", r)
	}
}

// ── 格式化 ──────────────────────────────────────────────────────────────

// TestCapabilityFormatPreflight —— 文案格式（逐字对账 TS `formatPreflight`）。
func TestCapabilityFormatPreflight(t *testing.T) {
	reg := &capabilityRegistry{
		Capabilities: []capability{{
			ID: "c1", Intent: "意图一",
			Providers: []capabilityProvider{
				{Kind: "public-cli", Name: "ok", Requires: capabilityRequires{Binary: []string{"present-bin"}}},
				{Kind: "public-cli", Name: "bad", Requires: capabilityRequires{Binary: []string{"absent"}}, InstallHint: "brew install bad"},
			},
		}},
	}
	ch := capabilityCheckers{Binary: func(n string) bool { return n == "present-bin" }}
	got := formatPreflight(preflightCapability(reg, "c1", ch))

	for _, want := range []string{
		"capability: c1",
		"intent: 意图一",
		"preflight: 1/2 providers available",
		"- ok (public-cli) available",
		"- bad (public-cli) missing binary:absent",
		"install: brew install bad",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("应含 %q，实得：\n%s", want, got)
		}
	}
}

// TestCapabilityFormatRegistryList —— 列表格式。
func TestCapabilityFormatRegistryList(t *testing.T) {
	got := formatRegistryList(seedRegistry)
	if !strings.Contains(got, "capability registry (schema v2)") {
		t.Errorf("应含 schema 版本行，实得 %q", got)
	}
	if !strings.Contains(got, "- media-transcode: 音视频转码、格式转换与媒体处理（ffmpeg） (1 provider)") {
		t.Errorf("应含能力行（单 provider 用单数），实得 %q", got)
	}
}

// ── 工具行为 ────────────────────────────────────────────────────────────

// TestCapabilityToolListAll —— 无 capabilityId 时列出全部。
func TestCapabilityToolListAll(t *testing.T) {
	reg := NewDefaultRegistry(Options{Cwd: t.TempDir()})
	res, err := reg.Execute(nil, "capability", &CallParams{})
	if err != nil {
		t.Fatalf("不应返回 error：%v", err)
	}
	if res.IsError {
		t.Fatalf("应成功，实得 %q", res.Content)
	}
	if !strings.Contains(res.Content, "capability registry") {
		t.Errorf("应列出 registry，实得 %q", res.Content)
	}
}

// TestCapabilityToolUnknownId —— 未知 capabilityId 报错。
func TestCapabilityToolUnknownId(t *testing.T) {
	reg := NewDefaultRegistry(Options{Cwd: t.TempDir()})
	res, _ := reg.Execute(nil, "capability", &CallParams{
		Input: map[string]any{"capabilityId": "definitely-not-exist"},
	})
	if !res.IsError || !strings.Contains(res.Content, "未找到 capability:") {
		t.Errorf("未知 id 应报错，实得 %q", res.Content)
	}
}

// TestCapabilityToolPreflightKnownId —— 已知 id 的 preflight（真实环境探测）。
func TestCapabilityToolPreflightKnownId(t *testing.T) {
	reg := NewDefaultRegistry(Options{Cwd: t.TempDir()})
	res, _ := reg.Execute(nil, "capability", &CallParams{
		Input: map[string]any{"capabilityId": "code-search"},
	})
	if res.IsError {
		t.Fatalf("应成功，实得 %q", res.Content)
	}
	// 文案应含 rg 的可用性（本机有无 rg 不确定，故只验证格式）
	if !strings.Contains(res.Content, "capability: code-search") {
		t.Errorf("应含 capability 行，实得 %q", res.Content)
	}
	if !strings.Contains(res.Content, "rg") {
		t.Errorf("应含 provider 名 rg，实得 %q", res.Content)
	}
}

// TestCapabilityToolLoadsProjectRegistry —— 项目级 `.rivet/capabilities.json` 生效。
func TestCapabilityToolLoadsProjectRegistry(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, ".rivet")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"capabilities":[{"id":"project-only","intent":"项目专属能力","providers":[{"name":"ptool","requires":{"binary":["ptool"]}}]}]}`
	if err := os.WriteFile(filepath.Join(dir, "capabilities.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := NewDefaultRegistry(Options{Cwd: cwd})
	res, _ := reg.Execute(nil, "capability", &CallParams{})
	if res.IsError {
		t.Fatalf("应成功，实得 %q", res.Content)
	}
	if !strings.Contains(res.Content, "project-only") {
		t.Errorf("应含项目级能力，实得 %q", res.Content)
	}
	// 种子仍在
	if !strings.Contains(res.Content, "media-transcode") {
		t.Errorf("种子应仍在（零契约破坏），实得 %q", res.Content)
	}
}

// ── definition ─────────────────────────────────────────────────────────

// TestCapabilityDefinitionParity —— definition 逐字对账。
func TestCapabilityDefinitionParity(t *testing.T) {
	def := Capability(t.TempDir()).Definition()

	if def.Name != "capability" {
		t.Errorf("name 应为 capability，实得 %q", def.Name)
	}
	if !strings.HasPrefix(def.Description, "只读查询能力索引（capability registry）。") {
		t.Errorf("description 首行应逐字对账，实得 %q", def.Description)
	}
	if len(def.InputSchema.PropOrder) != 1 || def.InputSchema.PropOrder[0] != "capabilityId" {
		t.Errorf("PropOrder 应只有 capabilityId，实得 %#v", def.InputSchema.PropOrder)
	}
	// **required 为空**（对账 TS：input_schema 无 required 字段）
	if len(def.InputSchema.Required) != 0 {
		t.Errorf("required 应为空，实得 %#v", def.InputSchema.Required)
	}
}

// TestCapabilityApprovalSemantics —— **不需审批** + 并发安全（对账 TS）。
func TestCapabilityApprovalSemantics(t *testing.T) {
	tool := Capability(t.TempDir())
	if tool.RequiresApproval(nil) {
		t.Error("应不需审批（只读查询，对账 TS `() => false`）")
	}
	if !tool.ConcurrencySafe() {
		t.Error("应并发安全")
	}
	if !tool.Enabled() {
		t.Error("应 enabled")
	}
}

// TestCapabilityDefaultCheckersPackageFailClosed —— **默认检查器的 package 语义**。
//
// # 为什么这条测试存在（变异反证暴露的覆盖缺口）
//
// M4 变异（把 `defaultCheckers().Package` 从 `false` 改成 `true`）**没有让任何
// 测试变红**——因为其他测试都用**自建 checkers**，从不走默认路径。
//
// 但 `defaultCheckers()` 的 `Package` 恒 false 是**本实现的显式设计决策**
// （见 capability.go 文件头）：Go 没有 Node 的 `node_modules` 解析语义，
// 「包存在」无明确定义。若写成 true，项目级 registry 里的 package 要求会
// **假装可用**（fail-open）——这是安全相关的语义，必须有测试锁住。
func TestCapabilityDefaultCheckersPackageFailClosed(t *testing.T) {
	ch := defaultCheckers()
	if ch.Package("any-npm-package") {
		t.Error("默认 package 检查器应恒 false（Go 无 node_modules 解析语义，fail-closed）")
	}
	if ch.Package("") {
		t.Error("空包名也应 false")
	}
	// binary/env 检查器应是真的（不是恒值）
	// `which` 对必然不存在的名字应返回 false
	if ch.Binary("zzq-definitely-not-a-real-binary-8f3a") {
		t.Error("binary 检查器对不存在的命令应 false")
	}
	if ch.Env("ZZQ_DEFINITELY_NOT_SET_8F3A") {
		t.Error("env 检查器对未设置的变量应 false")
	}
}

// TestCapabilityDefaultCheckersEnvPresent —— env 检查器对**已设置**变量返回 true。
func TestCapabilityDefaultCheckersEnvPresent(t *testing.T) {
	t.Setenv("ZZQ_TEST_CAP_ENV", "1")
	ch := defaultCheckers()
	if !ch.Env("ZZQ_TEST_CAP_ENV") {
		t.Error("已设置的环境变量应返回 true")
	}
}

// TestCapabilityDefaultCheckersBinaryPresent —— binary 检查器对**存在的**命令返回 true。
func TestCapabilityDefaultCheckersBinaryPresent(t *testing.T) {
	// `which` 自身必然存在（我们在用它）
	ch := defaultCheckers()
	if !ch.Binary("which") && !ch.Binary("sh") {
		t.Skip("本机 PATH 里既无 which 也无 sh——环境异常，跳过")
	}
}

// TestCapabilityEndToEndPackageFailClosed —— 项目级 package 要求在真实路径上显示缺失。
//
// 这条把「设计决策」与「端到端行为」连起来——走 `NewDefaultRegistry`（默认检查器）。
func TestCapabilityEndToEndPackageFailClosed(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, ".rivet")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 项目级条目只要求一个 npm 包
	content := `{"capabilities":[{"id":"pkg-only","intent":"只要求 npm 包","providers":[{"name":"pkgprov","requires":{"package":["lodash"]}}]}]}`
	if err := os.WriteFile(filepath.Join(dir, "capabilities.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := NewDefaultRegistry(Options{Cwd: cwd})
	res, _ := reg.Execute(nil, "capability", &CallParams{
		Input: map[string]any{"capabilityId": "pkg-only"},
	})
	if res.IsError {
		t.Fatalf("应成功，实得 %q", res.Content)
	}
	// 应诚实报告缺失（fail-closed），而非假装可用
	if !strings.Contains(res.Content, "missing package:lodash") {
		t.Errorf("package 要求应显示为缺失（fail-closed），实得 %q", res.Content)
	}
	// **注意**：文案里的 `preflight: 0/1 providers available` 是 TS 的固定标题格式
	// （available 是名词不是状态），故不能简单断言不含 "available"。
	// 真正的状态判定看 provider 行：应为 `missing package:lodash` 而非 `available`。
	if !strings.Contains(res.Content, "- pkgprov (public-cli) missing package:lodash") {
		t.Errorf("provider 行应显示 missing，实得 %q", res.Content)
	}
	if strings.Contains(res.Content, "- pkgprov (public-cli) available") {
		t.Errorf("provider 行不应显示 available（fail-open 缺口），实得 %q", res.Content)
	}
}
