package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/contract"
)

// capability.go —— `capability` 工具（第八十五刀 · W2-2）。
//
// 对账 TS `src/tools/capability-index.ts`（384 行）。
//
// # 依赖判定（已核实，消除计划里的「待验证假设」）
//
// TS 用 `createRequire` 做 **package 存在性检查**。核实结论：
// **6 条种子 registry 全部只用 `binary`**（`gh` 额外用 `env`），
// **无一条用 `package`**（实测 `grep -n "package:" src/tools/capability-index.ts` 零命中）。
// 故 `createRequire` 只服务于**项目级** `.rivet/capabilities.json` 里可能出现的
// `package` 项。
//
// **Go 侧决策**：`package` 检查器**恒返回 false（视为缺失）**——
// Go 没有 Node 的 `node_modules` 解析语义，「包存在」在 Go 里没有明确定义。
// 这样项目级 registry 若写了 `package` 要求，会**诚实显示为缺失**（fail-closed），
// 而不是假装可用。
//
// # 与 TS 的接线形态一致
//
// TS 的 `createCapabilityTool(loadRegistry)` 接受**可注入的加载器**——
// 本实现保留这一形态（`capabilityRegistryLoader`），便于测试注入。

// 能力索引 schema 版本与项目级 registry 路径（对账 TS 常量）。
const (
	capabilitySchemaVersion = "2"
	projectRegistryPath     = ".rivet/capabilities.json"
)

// Capability 创建 `capability` 工具。
func Capability(cwd string) Tool {
	return &capabilityTool{cwd: cwd, load: loadMergedRegistry}
}

type capabilityTool struct {
	cwd  string
	load func(string) *capabilityRegistry
}

// ── 数据模型（对账 TS 的接口）────────────────────────────────────────────

// capabilityRequires 对账 TS `CapabilityRequires`。
type capabilityRequires struct {
	Binary  []string `json:"binary"`
	Env     []string `json:"env"`
	Package []string `json:"package"`
}

// capabilityProvider 对账 TS `CapabilityProvider`。
type capabilityProvider struct {
	Kind        string             `json:"kind"`
	Name        string             `json:"name"`
	Requires    capabilityRequires `json:"requires"`
	InstallHint string             `json:"installHint"`
}

// capability 对账 TS `Capability`。
type capability struct {
	ID        string               `json:"id"`
	Intent    string               `json:"intent"`
	Hints     []string             `json:"hints"`
	Providers []capabilityProvider `json:"providers"`
}

// capabilityRegistry 对账 TS `CapabilityRegistry`。
type capabilityRegistry struct {
	SchemaVersion string       `json:"schemaVersion"`
	Capabilities  []capability `json:"capabilities"`
}

// providerPreflight 对账 TS `ProviderPreflight`。
type providerPreflight struct {
	Kind        string
	Name        string
	Available   bool
	Present     capabilityRequires
	Missing     capabilityRequires
	InstallHint string
}

// preflightResult 对账 TS `PreflightResult`。
type preflightResult struct {
	Capability struct {
		ID     string
		Intent string
		Hints  []string
	}
	Summary struct {
		Providers int
		Available int
		Missing   int
	}
	Providers []providerPreflight
}

// ── 种子 registry（对账 TS SEED_REGISTRY，逐条对账）────────────────────

var seedRegistry = &capabilityRegistry{
	SchemaVersion: capabilitySchemaVersion,
	Capabilities: []capability{
		{
			ID:     "media-transcode",
			Intent: "音视频转码、格式转换与媒体处理（ffmpeg）",
			Hints:  []string{"转码", "视频处理", "音频提取", "ffmpeg"},
			Providers: []capabilityProvider{{
				Kind: "public-cli", Name: "ffmpeg",
				Requires:    capabilityRequires{Binary: []string{"ffmpeg"}},
				InstallHint: "brew install ffmpeg",
			}},
		},
		{
			ID:     "image-processing",
			Intent: "图像处理与格式转换（ImageMagick）",
			Hints:  []string{"图片", "缩略图", "convert", "imagemagick"},
			Providers: []capabilityProvider{{
				Kind: "public-cli", Name: "imagemagick",
				Requires:    capabilityRequires{Binary: []string{"magick", "convert"}},
				InstallHint: "brew install imagemagick",
			}},
		},
		{
			ID:     "document-conversion",
			Intent: "文档格式转换（pandoc）",
			Hints:  []string{"markdown", "pdf", "docx", "pandoc"},
			Providers: []capabilityProvider{{
				Kind: "public-cli", Name: "pandoc",
				Requires:    capabilityRequires{Binary: []string{"pandoc"}},
				InstallHint: "brew install pandoc",
			}},
		},
		{
			ID:     "json-processing",
			Intent: "JSON 处理与查询（jq）",
			Hints:  []string{"jq", "json", "查询"},
			Providers: []capabilityProvider{{
				Kind: "public-cli", Name: "jq",
				Requires:    capabilityRequires{Binary: []string{"jq"}},
				InstallHint: "brew install jq",
			}},
		},
		{
			ID:     "github-ops",
			Intent: "GitHub 仓库操作（gh CLI）",
			Hints:  []string{"github", "pr", "issue", "gh"},
			Providers: []capabilityProvider{{
				Kind: "public-cli", Name: "gh",
				Requires:    capabilityRequires{Binary: []string{"gh"}, Env: []string{"GITHUB_TOKEN"}},
				InstallHint: "brew install gh",
			}},
		},
		{
			ID:     "code-search",
			Intent: "高性能代码搜索（ripgrep）",
			Hints:  []string{"搜索", "rg", "ripgrep"},
			Providers: []capabilityProvider{{
				Kind: "public-cli", Name: "rg",
				Requires:    capabilityRequires{Binary: []string{"rg"}},
				InstallHint: "brew install ripgrep",
			}},
		},
	},
}

// ── 工具定义 ────────────────────────────────────────────────────────────

func (t *capabilityTool) Definition() contract.Definition {
	return contract.Definition{
		Name: "capability",
		Description: "只读查询能力索引（capability registry）。列出全部能力或对指定 capabilityId 做 preflight——" +
			"逐 provider 报告本机 binary/env/package 可用性与安装提示。不修改任何状态。",
		InputSchema: objSchemaOrdered(
			[]string{"capabilityId"},
			map[string]any{
				"capabilityId": strProp("要 preflight 的能力 id；缺省时列出全部能力概览。"),
			},
			// **required 为空**（对账 TS：input_schema 无 required 字段）
		),
	}
}

func (t *capabilityTool) Execute(_ context.Context, p *CallParams) (contract.Result, error) {
	registry := t.load(t.cwd)
	if registry == nil {
		return contract.Result{Content: "错误：capability registry 加载失败", IsError: true}, nil
	}

	id, _ := p.Input["capabilityId"].(string)
	if id != "" {
		result := preflightCapability(registry, id, defaultCheckers())
		if result == nil {
			return contract.Result{Content: "未找到 capability: " + id, IsError: true}, nil
		}
		return contract.Result{Content: formatPreflight(result)}, nil
	}
	return contract.Result{Content: formatRegistryList(registry)}, nil
}

// RequiresApproval 恒 **false**（对账 TS `() => false`）——只读查询。
func (t *capabilityTool) RequiresApproval(_ *CallParams) bool { return false }

// ConcurrencySafe 恒 true（对账 TS `() => true`）。
func (t *capabilityTool) ConcurrencySafe() bool { return true }

// Enabled 恒 true（对账 TS `() => true`）。
func (t *capabilityTool) Enabled() bool { return true }

// Timeout 用默认（0）——本机探测（which/env）很快。
func (t *capabilityTool) Timeout(_ *CallParams) time.Duration { return 0 }

// ── 检查器（对账 TS Checkers）──────────────────────────────────────────

// capabilityCheckers 对账 TS `Checkers`——可注入，便于测试。
type capabilityCheckers struct {
	Binary  func(string) bool
	Env     func(string) bool
	Package func(string) bool
}

// defaultCheckers 返回默认检查器（对账 TS 的三个 default*Checker）。
//
// **`Package` 恒 false**——见文件头的 Go 侧决策说明（fail-closed）。
func defaultCheckers() capabilityCheckers {
	return capabilityCheckers{
		Binary:  defaultBinaryChecker,
		Env:     defaultEnvChecker,
		Package: func(string) bool { return false },
	}
}

// defaultBinaryChecker 对账 TS `defaultBinaryChecker`——`which <name>` 退出码 0。
func defaultBinaryChecker(name string) bool {
	cmd := exec.Command("which", name)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// defaultEnvChecker 对账 TS `defaultEnvChecker`——环境变量存在（**非空即算**）。
func defaultEnvChecker(name string) bool {
	_, ok := os.LookupEnv(name)
	return ok
}

// ── 解析（对账 TS parseCapability / parseRegistry）──────────────────────

// asList 对账 TS `asList`——nil → 空；数组 → 过滤非字符串；其余 → 单元素。
func asList(v any) []string {
	if v == nil {
		return nil
	}
	if arr, ok := v.([]any); ok {
		out := make([]string, 0, len(arr))
		for _, item := range arr {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return []string{fmt.Sprintf("%v", v)}
}

// parseCapability 对账 TS `parseCapability`——缺 id/intent 返回 nil。
func parseCapability(raw any) *capability {
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	id, _ := obj["id"].(string)
	intent, _ := obj["intent"].(string)
	if id == "" || intent == "" {
		return nil
	}

	var providers []capabilityProvider
	if arr, ok := obj["providers"].([]any); ok {
		for _, p := range arr {
			po, ok := p.(map[string]any)
			if !ok {
				continue
			}
			name, _ := po["name"].(string)
			if name == "" {
				continue
			}
			req, _ := po["requires"].(map[string]any)
			kind, _ := po["kind"].(string)
			if kind == "" {
				kind = "public-cli"
			}
			hint, _ := po["installHint"].(string)
			providers = append(providers, capabilityProvider{
				Kind: kind,
				Name: name,
				Requires: capabilityRequires{
					Binary:  asList(req["binary"]),
					Env:     asList(req["env"]),
					Package: asList(req["package"]),
				},
				InstallHint: hint,
			})
		}
	}

	var hints []string
	if arr, ok := obj["hints"].([]any); ok {
		for _, h := range arr {
			if s, ok := h.(string); ok {
				hints = append(hints, s)
			}
		}
	}

	return &capability{ID: id, Intent: intent, Hints: hints, Providers: providers}
}

// parseRegistry 对账 TS `parseRegistry`。
//
// **丢弃无 providers 的条目**（对账 TS `.filter(c => c.providers.length > 0)`）。
func parseRegistry(raw any) *capabilityRegistry {
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	arr, ok := obj["capabilities"].([]any)
	if !ok {
		return nil
	}
	var caps []capability
	for _, item := range arr {
		c := parseCapability(item)
		if c != nil && len(c.Providers) > 0 {
			caps = append(caps, *c)
		}
	}
	ver, _ := obj["schemaVersion"].(string)
	if ver == "" {
		ver = capabilitySchemaVersion
	}
	return &capabilityRegistry{SchemaVersion: ver, Capabilities: caps}
}

// loadProjectRegistry 对账 TS `loadProjectRegistry`——文件缺失或解析失败返回 nil。
func loadProjectRegistry(cwd string) *capabilityRegistry {
	path := filepath.Join(cwd, projectRegistryPath)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	return parseRegistry(raw)
}

// mergeRegistries 对账 TS `mergeRegistries`——项目级按 id 覆盖、新增追加、
// **绝不删种子**（零契约破坏）。
func mergeRegistries(seed *capabilityRegistry, project *capabilityRegistry) *capabilityRegistry {
	if project == nil {
		return seed
	}
	order := make([]string, 0, len(seed.Capabilities)+len(project.Capabilities))
	byID := make(map[string]capability, len(seed.Capabilities)+len(project.Capabilities))
	for _, c := range seed.Capabilities {
		if _, seen := byID[c.ID]; !seen {
			order = append(order, c.ID)
		}
		byID[c.ID] = c
	}
	for _, c := range project.Capabilities {
		if _, seen := byID[c.ID]; !seen {
			order = append(order, c.ID)
		}
		byID[c.ID] = c
	}
	out := make([]capability, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	ver := project.SchemaVersion
	if ver == "" {
		ver = seed.SchemaVersion
	}
	return &capabilityRegistry{SchemaVersion: ver, Capabilities: out}
}

// loadMergedRegistry 对账 TS `loadMergedRegistry`——种子 + 项目级覆盖。
func loadMergedRegistry(cwd string) *capabilityRegistry {
	return mergeRegistries(seedRegistry, loadProjectRegistry(cwd))
}

// ── preflight（对账 TS）─────────────────────────────────────────────────

// checkRequires 对账 TS `checkRequires`。
func checkRequires(req capabilityRequires, ch capabilityCheckers) (present, missing capabilityRequires) {
	for _, name := range req.Binary {
		if ch.Binary(name) {
			present.Binary = append(present.Binary, name)
		} else {
			missing.Binary = append(missing.Binary, name)
		}
	}
	for _, name := range req.Env {
		if ch.Env(name) {
			present.Env = append(present.Env, name)
		} else {
			missing.Env = append(missing.Env, name)
		}
	}
	for _, name := range req.Package {
		if ch.Package(name) {
			present.Package = append(present.Package, name)
		} else {
			missing.Package = append(missing.Package, name)
		}
	}
	return present, missing
}

// checkProviderRequirements 对账 TS `checkProviderRequirements`。
//
// **available 的判定含 `present 总数 > 0`**——一个 requires 全空的 provider
// **不算 available**（否则「无要求」会被误判为「可用」）。
func checkProviderRequirements(provider capabilityProvider, ch capabilityCheckers) providerPreflight {
	present, missing := checkRequires(provider.Requires, ch)
	totalPresent := len(present.Binary) + len(present.Env) + len(present.Package)
	available := totalPresent > 0 &&
		len(missing.Binary) == 0 && len(missing.Env) == 0 && len(missing.Package) == 0
	return providerPreflight{
		Kind:        provider.Kind,
		Name:        provider.Name,
		Available:   available,
		Present:     present,
		Missing:     missing,
		InstallHint: provider.InstallHint,
	}
}

// preflightCapability 对账 TS `preflightCapability`——未找到返回 nil。
func preflightCapability(registry *capabilityRegistry, capabilityID string, ch capabilityCheckers) *preflightResult {
	var found *capability
	for i := range registry.Capabilities {
		if registry.Capabilities[i].ID == capabilityID {
			found = &registry.Capabilities[i]
			break
		}
	}
	if found == nil {
		return nil
	}

	r := &preflightResult{}
	r.Capability.ID = found.ID
	r.Capability.Intent = found.Intent
	r.Capability.Hints = found.Hints
	if r.Capability.Hints == nil {
		r.Capability.Hints = []string{}
	}
	for _, p := range found.Providers {
		pf := checkProviderRequirements(p, ch)
		r.Providers = append(r.Providers, pf)
		if pf.Available {
			r.Summary.Available++
		} else {
			r.Summary.Missing++
		}
	}
	r.Summary.Providers = len(r.Providers)
	return r
}

// ── 格式化（对账 TS，逐字）──────────────────────────────────────────────

// formatPreflight 对账 TS `formatPreflight`。
func formatPreflight(result *preflightResult) string {
	lines := []string{
		"capability: " + result.Capability.ID,
		"intent: " + result.Capability.Intent,
		fmt.Sprintf("preflight: %d/%d providers available", result.Summary.Available, result.Summary.Providers),
	}
	for _, p := range result.Providers {
		var bits []string
		for _, b := range p.Missing.Binary {
			bits = append(bits, "binary:"+b)
		}
		for _, e := range p.Missing.Env {
			bits = append(bits, "env:"+e)
		}
		for _, n := range p.Missing.Package {
			bits = append(bits, "package:"+n)
		}
		status := "available"
		if !p.Available {
			status = "missing " + strings.Join(bits, ", ")
		}
		hint := ""
		if p.InstallHint != "" {
			hint = "  install: " + p.InstallHint
		}
		lines = append(lines, fmt.Sprintf("  - %s (%s) %s%s", p.Name, p.Kind, status, hint))
	}
	return strings.Join(lines, "\n")
}

// formatRegistryList 对账 TS `formatRegistryList`。
func formatRegistryList(registry *capabilityRegistry) string {
	lines := []string{fmt.Sprintf("capability registry (schema v%s)", registry.SchemaVersion)}
	for _, c := range registry.Capabilities {
		n := len(c.Providers)
		suffix := "s"
		if n == 1 {
			suffix = ""
		}
		lines = append(lines, fmt.Sprintf("  - %s: %s (%d provider%s)", c.ID, c.Intent, n, suffix))
	}
	return strings.Join(lines, "\n")
}
