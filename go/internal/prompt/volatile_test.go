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
