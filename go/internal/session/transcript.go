package session

import (
	"bytes"
	"fmt"

	"github.com/klauspost/compress/zstd"

	"github.com/kalandramo/tianshu/go/internal/prompt"
)

// Transcript 是会话 transcript 的编解码器。
//
// 对账 src/agent/session-transcript-codec.ts（130 行）。格式：
//   - 会话文件要么是**传统纯文本 JSONL**（每行一个带校验和的 JSON 对象），
//   - 要么是**独立可解压的 zstd 帧拼接**（每帧含一批 JSONL 文本，行格式同上）。
//
// 帧自描述（靠 zstd 帧魔数），无需容器头。末帧可能被崩溃截断（torn tail）；
// 读取方**丢弃它**——等价于纯文本行的有损窗口 + 孤儿修复语义。
//
// ## 跨版本兼容（已验证）
//
// Node 的 `zstdCompressSync` 与 klauspost/compress 的 zstd 实现**双向可解**：
// 探针实测 Node 帧→Go 解压 ✅、Go 帧→Node 解压 ✅。
// 帧字节不完全相同（descriptor 的 singleSegment 位有差异：Node 用单段、
// Go 默认非单段），但互相可解——这是跨版本兼容的充分条件。
//
// 注意：**不要把 Go 的帧字节与 Node 的帧字节做逐字节对账**——它们本来就
// 允许不同（压缩器实现差异）。兼容性的判据是「互解」，不是「同字节」。
type Transcript struct {
	enc *zstd.Encoder
	dec *zstd.Decoder
}

// NewTranscript 构造编解码器。
//
// 编码器开 **CRC 校验**（对账 TS 的 ZSTD_c_checksumFlag: 1）——帧尾 4 字节
// 校验和，损坏能被检出。
func NewTranscript() (*Transcript, error) {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderCRC(true))
	if err != nil {
		return nil, fmt.Errorf("创建 zstd 编码器失败：%w", err)
	}
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return nil, fmt.Errorf("创建 zstd 解码器失败：%w", err)
	}
	return &Transcript{enc: enc, dec: dec}, nil
}

// Close 释放编解码器资源。
func (t *Transcript) Close() {
	if t.enc != nil {
		t.enc.Close()
	}
	if t.dec != nil {
		t.dec.Close()
	}
}

// EncodeBatch 把一批 JSONL 文本压成**一个**带校验和的独立帧。
//
// 对账 TS 的 encodeBatch：空文本产出空 buffer（调用方跳过写入）。
func (t *Transcript) EncodeBatch(text string) []byte {
	if text == "" {
		return nil
	}
	return t.enc.EncodeAll([]byte(text), nil)
}

// DecodeTranscriptText 把 transcript 文件的字节解回 JSONL 文本。
//
// 对账 TS 的 decodeTranscriptText：
//   - 空 buffer → ""
//   - 非 zstd 帧流 → 按 UTF-8 原样返回（传统纯文本直通）
//   - zstd 帧流 → 逐帧解压拼接；**torn tail 被丢弃**
//
// 与 TS 的差异：TS 用 `scanZstdFrames` 先定位帧再逐帧 `zstdDecompressSync`；
// Go 侧同样先扫描（复用 prompt.ScanZstdFrames），但**只解压完整帧**——
// torn tail 天然被排除在 frames 之外，无需特殊处理。
func (t *Transcript) DecodeTranscriptText(buf []byte) (string, error) {
	if len(buf) == 0 {
		return "", nil
	}
	if !prompt.IsZstdFrameStream(buf) {
		return string(buf), nil
	}
	scan, err := prompt.ScanZstdFrames(buf)
	if err != nil {
		return "", err
	}
	if len(scan.Frames) == 0 {
		return "", nil
	}
	var out bytes.Buffer
	for _, f := range scan.Frames {
		plain, err := t.dec.DecodeAll(buf[f.Start:f.End], nil)
		if err != nil {
			return "", fmt.Errorf("解压帧 [%d,%d) 失败：%w", f.Start, f.End, err)
		}
		out.Write(plain)
	}
	return out.String(), nil
}

// TornTailStart 返回不完整末帧的起点（无 torn tail 时为 -1）。
//
// 调用方可用它实现「丢弃 torn tail 并截断文件」的恢复策略。
func TornTailStart(buf []byte) (int, error) {
	if len(buf) == 0 || !prompt.IsZstdFrameStream(buf) {
		return -1, nil
	}
	scan, err := prompt.ScanZstdFrames(buf)
	if err != nil {
		return -1, err
	}
	return scan.TornStart, nil
}
