package prompt

import (
	"encoding/binary"
	"fmt"
)

// ZstdMagicLE 是 zstd 帧魔数（按小端读为 uint32）。
//
// 字节序：文件里的前 4 字节是 0x28 0xB5 0x2F 0xFD（大端序的魔数），
// 按 UInt32LE 读得到 0xfd2fb528。对账 src/agent/session-transcript-codec.ts:24。
const ZstdMagicLE = 0xfd2fb528

// ZstdFrameRange 是一个完整帧的字节区间（start 含、end 不含）。
type ZstdFrameRange struct {
	Start int
	End   int
}

// ZstdFrameScan 是帧扫描结果。
type ZstdFrameScan struct {
	Frames []ZstdFrameRange
	// TornStart 是**不完整末帧**的起点（EOF 打断了它）。
	// -1 表示没有 torn tail（对应 TS 的 undefined）。
	TornStart int
}

// IsZstdFrameStream 复刻 session-transcript-codec.ts 的 isZstdFrameStream ——
// 按前 4 字节魔数判定是否为 zstd 帧流。
func IsZstdFrameStream(buf []byte) bool {
	return len(buf) >= 4 && binary.LittleEndian.Uint32(buf[0:4]) == ZstdMagicLE
}

// ScanZstdFrames 复刻 session-transcript-codec.ts 的 scanZstdFrames ——
// 走查 zstd 帧结构（RFC 8878 帧头），**不解压块**。
//
// 语义（对账 TS 逐行）：
//   - 逐个帧：校验魔数 → 读 descriptor → 算帧头剩余长度 → 走块头直到 last block
//     → 若有校验位则跳 4 字节
//   - **torn tail**：任何一步发现字节不足 → 返回已扫出的帧 + 该帧起点作 TornStart
//   - **损坏**：魔数错 / 保留位非 0 / 保留块类型 → 返回 error
//
// torn tail 是崩溃恢复的核心：崩溃截断的末帧应被丢弃而非报错，否则整条
// 会话读不出来。
func ScanZstdFrames(buf []byte) (ZstdFrameScan, error) {
	frames := []ZstdFrameRange{}
	offset := 0

	for offset < len(buf) {
		start := offset
		if len(buf)-offset < 4 {
			return ZstdFrameScan{Frames: frames, TornStart: start}, nil
		}
		if binary.LittleEndian.Uint32(buf[offset:offset+4]) != ZstdMagicLE {
			return ZstdFrameScan{}, fmt.Errorf(
				"corrupt session transcript: invalid frame magic at byte %d", offset)
		}
		offset += 4

		if offset == len(buf) {
			return ZstdFrameScan{Frames: frames, TornStart: start}, nil
		}
		descriptor := buf[offset]
		offset++
		if descriptor&0x18 != 0 {
			return ZstdFrameScan{}, fmt.Errorf(
				"corrupt session transcript: reserved frame-header bit at byte %d", offset-1)
		}

		contentSizeFlag := descriptor >> 6
		singleSegment := descriptor&0x20 != 0
		checksum := descriptor&0x04 != 0
		dictionaryFlag := descriptor & 0x03
		dictionaryBytes := int(dictionaryFlag)
		if dictionaryFlag == 3 {
			dictionaryBytes = 4
		}
		remainingHeaderBytes := remainingHeaderBytesFor(contentSizeFlag, singleSegment, dictionaryBytes)
		if len(buf)-offset < remainingHeaderBytes {
			return ZstdFrameScan{Frames: frames, TornStart: start}, nil
		}
		offset += remainingHeaderBytes

		for {
			if len(buf)-offset < 3 {
				return ZstdFrameScan{Frames: frames, TornStart: start}, nil
			}
			blockHeader := readUintLE(buf[offset : offset+3])
			offset += 3
			lastBlock := blockHeader&1 != 0
			blockType := (blockHeader >> 1) & 0x03
			blockSize := int(blockHeader >> 3)

			if blockType == 0x03 {
				return ZstdFrameScan{}, fmt.Errorf(
					"corrupt session transcript: reserved block type at byte %d", offset-3)
			}
			payloadBytes := blockSize
			if blockType == 0x01 {
				payloadBytes = 1
			}
			if len(buf)-offset < payloadBytes {
				return ZstdFrameScan{Frames: frames, TornStart: start}, nil
			}
			offset += payloadBytes
			if lastBlock {
				break
			}
		}

		if checksum {
			if len(buf)-offset < 4 {
				return ZstdFrameScan{Frames: frames, TornStart: start}, nil
			}
			offset += 4
		}
		frames = append(frames, ZstdFrameRange{Start: start, End: offset})
	}

	return ZstdFrameScan{Frames: frames, TornStart: -1}, nil
}

// contentSizeBytesFor 复刻 TS 的 contentSize 字段长度计算。
//
// **易错点**：contentSizeFlag=0 且**非单段**时该字段占 0 字节（不是 1）。
// 对应 TS：
//
//	const contentSizeBytes = contentSizeFlag === 0
//	  ? (singleSegment ? 1 : 0)
//	  : 1 << contentSizeFlag
func contentSizeBytesFor(contentSizeFlag byte, singleSegment bool) int {
	if contentSizeFlag == 0 {
		if singleSegment {
			return 1
		}
		return 0
	}
	return 1 << contentSizeFlag
}

// remainingHeaderBytesFor 复刻 TS 的帧头剩余字节数：
// `(singleSegment ? 0 : 1) + dictionaryBytes + contentSizeBytes`。
func remainingHeaderBytesFor(contentSizeFlag byte, singleSegment bool, dictionaryBytes int) int {
	n := dictionaryBytes + contentSizeBytesFor(contentSizeFlag, singleSegment)
	if !singleSegment {
		n++
	}
	return n
}

// readUintLE 复刻 Buffer.readUIntLE(offset, 3) —— 3 字节小端无符号整数。
func readUintLE(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16
}
