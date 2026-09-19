package prompt

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// zstdOracle 是 TS 侧真实 scanZstdFrames / isZstdFrameStream 的产出。
// 生成命令：npx tsx go/testdata/zstdframe/gen-oracle.ts
type zstdOracle struct {
	MagicLE int `json:"magicLE"`
	Cases   map[string]struct {
		Note   string `json:"note"`
		Hex    string `json:"hex"`
		IsZstd bool   `json:"isZstd"`
		Frames []struct {
			Start int `json:"start"`
			End   int `json:"end"`
		} `json:"frames"`
		TornStart *int    `json:"tornStart"`
		Error     *string `json:"error"`
	} `json:"cases"`
}

func loadZstdOracle(t *testing.T) zstdOracle {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "zstdframe", "oracle.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败（%s）：%v\n生成命令：npx tsx go/testdata/zstdframe/gen-oracle.ts", path, err)
	}
	var o zstdOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return o
}

// TestZstdMagicConstant —— 魔数常量与 TS 一致。
func TestZstdMagicConstant(t *testing.T) {
	o := loadZstdOracle(t)
	if ZstdMagicLE != o.MagicLE {
		t.Errorf("魔数不符：Go=0x%x TS=0x%x", ZstdMagicLE, o.MagicLE)
	}
}

// TestScanZstdFramesParity —— 帧扫描与 TS 逐字节相同。
//
// 覆盖正常多帧、torn tail（崩溃截断）、损坏（魔数/保留位/保留块类型）。
func TestScanZstdFramesParity(t *testing.T) {
	o := loadZstdOracle(t)
	if len(o.Cases) == 0 {
		t.Fatal("oracle 无用例")
	}
	for name, c := range o.Cases {
		t.Run(name, func(t *testing.T) {
			buf, err := hex.DecodeString(c.Hex)
			if err != nil {
				t.Fatalf("解码 hex 失败：%v", err)
			}

			// isZstdFrameStream
			if got := IsZstdFrameStream(buf); got != c.IsZstd {
				t.Errorf("IsZstdFrameStream：Go=%v TS=%v", got, c.IsZstd)
			}

			scan, scanErr := ScanZstdFrames(buf)

			// 错误分支：两侧要么都错、要么都对（错误消息也逐字对账）
			if c.Error != nil {
				if scanErr == nil {
					t.Fatalf("应抛错（TS: %s），实际无错", *c.Error)
				}
				if scanErr.Error() != *c.Error {
					t.Errorf("错误消息不符\n  Go =%q\n  TS =%q", scanErr.Error(), *c.Error)
				}
				return
			}
			if scanErr != nil {
				t.Fatalf("TS 未抛错，Go 抛错：%v", scanErr)
			}

			// 帧区间
			if len(scan.Frames) != len(c.Frames) {
				t.Fatalf("帧数不符：Go=%d TS=%d（Go=%v）", len(scan.Frames), len(c.Frames), scan.Frames)
			}
			for i, want := range c.Frames {
				got := scan.Frames[i]
				if got.Start != want.Start || got.End != want.End {
					t.Errorf("第 %d 帧区间：Go=[%d,%d) TS=[%d,%d)",
						i, got.Start, got.End, want.Start, want.End)
				}
			}

			// tornStart
			wantTorn := -1
			if c.TornStart != nil {
				wantTorn = *c.TornStart
			}
			if scan.TornStart != wantTorn {
				t.Errorf("TornStart：Go=%d TS=%d", scan.TornStart, wantTorn)
			}
		})
	}
}

// TestScanZstdFramesTornTail —— torn tail 是崩溃恢复的核心：截断的末帧
// 应被丢弃（返回其起点）而非报错。
func TestScanZstdFramesTornTail(t *testing.T) {
	o := loadZstdOracle(t)
	c, ok := o.Cases["tornInMagic"]
	if !ok {
		t.Fatal("oracle 缺 tornInMagic 用例")
	}
	buf, _ := hex.DecodeString(c.Hex)
	scan, err := ScanZstdFrames(buf)
	if err != nil {
		t.Fatalf("torn tail 不应报错：%v", err)
	}
	// 首帧完整、次帧 torn
	if len(scan.Frames) != 1 {
		t.Errorf("应扫出 1 个完整帧，实际 %d", len(scan.Frames))
	}
	if scan.TornStart < 0 {
		t.Error("应有 tornStart")
	}
}

// TestScanZstdFramesCorruptThrows —— 损坏的帧结构应报错（与 torn 区分）。
func TestScanZstdFramesCorruptThrows(t *testing.T) {
	// 魔数错误
	if _, err := ScanZstdFrames([]byte{0xde, 0xad, 0xbe, 0xef, 0x00}); err == nil {
		t.Error("错误魔数应报错")
	}
	// 保留位非 0（descriptor 的 bit3 或 bit4）——构造合法魔数 + 保留位
	buf := []byte{0x28, 0xb5, 0x2f, 0xfd, 0x08}
	if _, err := ScanZstdFrames(buf); err == nil {
		t.Error("保留位非 0 应报错")
	}
}

// TestScanZstdFramesEmpty —— 空输入返回无帧。
func TestScanZstdFramesEmpty(t *testing.T) {
	scan, err := ScanZstdFrames(nil)
	if err != nil {
		t.Fatalf("空输入不应报错：%v", err)
	}
	if len(scan.Frames) != 0 {
		t.Errorf("空输入应无帧，实际 %d", len(scan.Frames))
	}
}

// TestContentSizeBytesRule —— 帧头 contentSize 字段长度的计算规则。
//
// **Go-only 测试**：这条规则无法用 oracle 覆盖——真实 zstd 只产出
// singleSegment=1 的帧，手工构造非单段帧又极易算错字节（我试了两版都
// 没构造出能区分两种逻辑的输入）。故直接断言规则本身。
//
// 对账 session-transcript-codec.ts:76-79：
//
//	const contentSizeBytes = contentSizeFlag === 0
//	  ? (singleSegment ? 1 : 0)
//	  : 1 << contentSizeFlag
//
// 关键：**contentSizeFlag=0 且非单段时，该字段占 0 字节**（不是 1）。
// 变异（统一用 1<<flag）会让帧头多算 1 字节 → 扫描偏移错位。
func TestContentSizeBytesRule(t *testing.T) {
	cases := []struct {
		flag          byte
		singleSegment bool
		want          int
	}{
		{0, true, 1},  // flag=0 + 单段 → 1 字节
		{0, false, 0}, // flag=0 + 非单段 → **0 字节**（易错点）
		{1, true, 2},  // flag=1 → 2 字节
		{1, false, 2},
		{2, true, 4},  // flag=2 → 4 字节
		{3, false, 8}, // flag=3 → 8 字节
	}
	for _, c := range cases {
		got := contentSizeBytesFor(c.flag, c.singleSegment)
		if got != c.want {
			t.Errorf("contentSizeFlag=%d singleSegment=%v：Go=%d 期望=%d",
				c.flag, c.singleSegment, got, c.want)
		}
	}
}

// TestRemainingHeaderBytesRule —— 帧头剩余字节数（window descriptor + dict + contentSize）。
func TestRemainingHeaderBytesRule(t *testing.T) {
	// 单段 + flag=0：无 windowDesc(0) + 无 dict(0) + contentSize(1) = 1
	if got := remainingHeaderBytesFor(0, true, 0); got != 1 {
		t.Errorf("单段 flag=0 dict=0：Go=%d 期望=1", got)
	}
	// 非单段 + flag=0：windowDesc(1) + 0 + contentSize(0) = 1
	if got := remainingHeaderBytesFor(0, false, 0); got != 1 {
		t.Errorf("非单段 flag=0 dict=0：Go=%d 期望=1", got)
	}
	// 非单段 + flag=1：windowDesc(1) + 0 + contentSize(2) = 3
	if got := remainingHeaderBytesFor(1, false, 0); got != 3 {
		t.Errorf("非单段 flag=1：Go=%d 期望=3", got)
	}
	// 单段 + flag=0 + dictionaryBytes=3：0 + 3 + 1 = 4
	// 注意：dictionaryBytes 是**已折算后**的值（dictFlag=3 → 4，见
	// dictionaryBytesFor），本函数收到的是折算结果。
	if got := remainingHeaderBytesFor(0, true, 3); got != 4 {
		t.Errorf("单段 dictBytes=3：Go=%d 期望=4", got)
	}
}
