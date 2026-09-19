package prompt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// volatileOracle 是 TS 侧真实 buildStableVolatileBlock 的产出。
// 生成命令：npx tsx go/testdata/volatile/gen-oracle.ts
type volatileOracle struct {
	FixtureCwd string `json:"fixtureCwd"`
	Host       struct {
		Platform  string `json:"platform"`
		OSType    string `json:"osType"`
		OSRelease string `json:"osRelease"`
	} `json:"host"`
	Cases map[string]struct {
		Ctx  json.RawMessage `json:"ctx"`
		Out  string          `json:"out"`
		Note string          `json:"note"`
	} `json:"cases"`
}

func loadVolatileOracle(t *testing.T) volatileOracle {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "volatile", "oracle.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败（%s）：%v\n生成命令：npx tsx go/testdata/volatile/gen-oracle.ts", path, err)
	}
	var o volatileOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return o
}

// TestBuildStableVolatileBlockParity —— frozen 稳定块与 TS 逐字节相同。
//
// 这是缓存命中的核心：该块紧跟 system prompt，它的字节稳定性决定整条
// 前缀能否复用。
//
// 宿主相关部分（<environment> 的 platform/os）由 oracle 记录的 host 值
// 注入，使对账可跨机器复现。
func TestBuildStableVolatileBlockParity(t *testing.T) {
	o := loadVolatileOracle(t)
	if len(o.Cases) == 0 {
		t.Fatal("oracle 无用例")
	}

	host := HostEnv{
		Platform:  o.Host.Platform,
		OSType:    o.Host.OSType,
		OSRelease: o.Host.OSRelease,
	}

	for name, c := range o.Cases {
		t.Run(name, func(t *testing.T) {
			ctx := parseVolatileCtx(t, c.Ctx)
			got := BuildStableVolatileBlock(ctx, host)
			if got != c.Out {
				t.Errorf("不等价\n  Go 长度=%d\n  TS 长度=%d\n  Go =%q\n  TS =%q",
					len(got), len(c.Out), trunc2(got, 300), trunc2(c.Out, 300))
			}
		})
	}
}

// parseVolatileCtx 把 oracle 的 ctx JSON 解成 Go 结构。
// 只解对账需要的字段（宿主/IO 相关字段不参与）。
// parseVolatileCtx 把 oracle 的 ctx JSON 解成 Go 结构。
// cwd 从 JSON 里取（oracle 每个用例都显式带 cwd），保证与 TS 侧同源。
func parseVolatileCtx(t *testing.T, raw json.RawMessage) VolatileContext {
	t.Helper()
	var wire struct {
		Cwd                    string         `json:"cwd"`
		RivetMd                string         `json:"rivetMd"`
		ProjectMemoryBlock     string         `json:"projectMemoryBlock"`
		KnowledgeManifestBlock string         `json:"knowledgeManifestBlock"`
		SeedCapsuleBlock       string         `json:"seedCapsuleBlock"`
		ProjectIndexBlock      string         `json:"projectIndexBlock"`
		WorkingSet             []string       `json:"workingSet"`
		SessionMemoryBlock     string         `json:"sessionMemoryBlock"`
		CwdRelation            string         `json:"cwdRelation"`
		BlockCaps              map[string]int `json:"blockCaps"`
		ActiveDomain           *struct {
			Name           string `json:"name"`
			Motto          string `json:"motto"`
			VolatileBlock  string `json:"volatileBlock"`
			KnowledgeBlock string `json:"knowledgeBlock"`
		} `json:"activeDomain"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("解析 ctx 失败：%v", err)
	}

	ctx := VolatileContext{
		Cwd:                    wire.Cwd,
		RivetMd:                wire.RivetMd,
		ProjectMemoryBlock:     wire.ProjectMemoryBlock,
		KnowledgeManifestBlock: wire.KnowledgeManifestBlock,
		SeedCapsuleBlock:       wire.SeedCapsuleBlock,
		ProjectIndexBlock:      wire.ProjectIndexBlock,
		WorkingSet:             wire.WorkingSet,
		SessionMemoryBlock:     wire.SessionMemoryBlock,
		CwdRelation:            wire.CwdRelation,
		BlockCaps:              wire.BlockCaps,
	}
	if wire.ActiveDomain != nil {
		ctx.ActiveDomain = &ActiveDomain{
			Name:           wire.ActiveDomain.Name,
			Motto:          wire.ActiveDomain.Motto,
			VolatileBlock:  wire.ActiveDomain.VolatileBlock,
			KnowledgeBlock: wire.ActiveDomain.KnowledgeBlock,
		}
	}
	return ctx
}

// TestRuntimeEnvBlockInjectionPosition —— runtime-env 块的注入位置。
//
// **这条是 Go-only 测试**（oracle 覆盖不了它，原因见 gen-oracle.ts 的注释）：
// TS 侧 runtime-env 由 detectRuntimeEnvBlock(ctx.cwd) 内部探测产生，不是 ctx
// 字段；Go 侧为保持纯函数做成了注入字段 ctx.RuntimeEnv。两者架构分歧是有意的。
//
// 断言：块应出现在 environment 之后、sober 之前（对账 volatile.ts:1090 顺序）。
func TestRuntimeEnvBlockInjectionPosition(t *testing.T) {
	host := HostEnv{Platform: "darwin", OSType: "Darwin", OSRelease: "25.6.0"}
	ctx := VolatileContext{
		Cwd:        "/fixture",
		RuntimeEnv: "<runtime-env>\ngo: declared 1.22.0 via go.mod\n</runtime-env>",
	}
	got := BuildStableVolatileBlock(ctx, host)

	idxEnv := indexOf(got, "<environment")
	idxRuntime := indexOf(got, "<runtime-env>")
	idxSober := indexOf(got, "<sober>")

	if idxRuntime < 0 {
		t.Fatalf("runtime-env 块应出现，实际 %q", got)
	}
	if !(idxEnv < idxRuntime && idxRuntime < idxSober) {
		t.Errorf("顺序应为 environment → runtime-env → sober，实际位置 %d/%d/%d",
			idxEnv, idxRuntime, idxSober)
	}
}

// TestRuntimeEnvBlockAbsent —— RuntimeEnv 为空时不产生该块。
func TestRuntimeEnvBlockAbsent(t *testing.T) {
	host := HostEnv{Platform: "darwin", OSType: "Darwin", OSRelease: "25.6.0"}
	got := BuildStableVolatileBlock(VolatileContext{Cwd: "/fixture"}, host)
	if contains(got, "<runtime-env>") {
		t.Errorf("RuntimeEnv 为空时不应有该块，实际 %q", got)
	}
}

// TestStripTableWiring —— projectIndexBlock 存在时剥离 rivetMd 的表格。
//
// 这条锁定 volatile.go 里 StripFirstMarkdownTable 的**调用点**
// （函数早已移植，但此前未接线）。
func TestStripTableWiring(t *testing.T) {
	host := HostEnv{Platform: "darwin", OSType: "Darwin", OSRelease: "25.6.0"}
	md := "## 标题\n正文\n\n> 索引\n| a | b |\n| - | - |\n| 1 | 2 |\n\n后续"

	// 有 projectIndexBlock → 表格被剥离
	withIdx := BuildStableVolatileBlock(VolatileContext{
		Cwd:               "/fixture",
		RivetMd:           md,
		ProjectIndexBlock: "<codebase-index>\n模块\n</codebase-index>",
	}, host)
	if contains(withIdx, "| a | b |") {
		t.Error("projectIndexBlock 存在时表格应被剥离")
	}

	// 无 projectIndexBlock → 表格保留
	withoutIdx := BuildStableVolatileBlock(VolatileContext{
		Cwd:     "/fixture",
		RivetMd: md,
	}, host)
	if !contains(withoutIdx, "| a | b |") {
		t.Error("无 projectIndexBlock 时表格应保留")
	}
}
