package tools

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// winStreamDecoder 是 Windows 控制台输出的流式解码器，防中文乱码。
//
// 对账 src/platform.ts 的 `WinStreamDecoder`（首块探测 UTF-8/GBK + 流式解码）。
//
// # 为什么需要它
//
// Windows 中文环境的控制台代码页是 **936（GBK）**。实测（本机 Windows）：
//
//	cmd /c "echo 中文测试"   → d6 d0 ce c4 b2 e2 ca d4   ← GBK
//	bash -c "echo 中文测试"  → e4 b8 ad e6 96 87 ...     ← UTF-8
//
// 同一个二进制可能收到两种编码，取决于命令跑在哪个 shell 里——而 shell 由探测
// 决定（internal/platform），**未装 Git Bash 时回落 cmd.exe**，那时中文必乱码。
//
// TS 侧首块探测的处置被沿用，但 Go 的实现有两处必须比 TS 更小心（探针实测得出）：
//
//  1. **不能每次调用都重置转换器**。`transform.Bytes` 每次新建状态——逐块调用
//     会让每个多字节字符的首字节单独解码成替换字符（实测全部产出 `ef bf bd`）。
//     必须持有 `transform.Transformer` 并反复调 `Transform`（状态跨调用保留）。
//
//  2. **单块 `utf8.Valid` 不是可靠的编码判据**。一个块可能恰好以多字节字符的
//     首字节结尾，此时 `utf8.Valid` 为 false——但那是「不完整」而非「非法」。
//     实测：单字节 `d6` 与 `e4` 的 `utf8.Valid` **都是 false**，无法区分 GBK 与
//     UTF-8。故改为**累积探测**：攒到「确定非法」或「合法且完整」再判定。
type winStreamDecoder struct {
	// enabled 为假时退化为直通（非 Windows 宿主）。
	enabled bool

	// decided 标记编码已判定。
	decided bool
	// isUTF8 是判定结果（decided 为真时有效）。
	isUTF8 bool

	// pending 是尚未判定期间累积的字节。
	pending []byte

	// transformer 是 GBK 解码器（判定为 GBK 时构造，之后持续复用保状态）。
	transformer transform.Transformer
	// residue 是转换器未消费的残留字节（半个多字节字符的尾部），下次拼接重试。
	residue []byte
}

// newWinStreamDecoder 构造解码器。
//
// isWindows 为假时完全不介入（Unix 输出恒为 UTF-8）——对账 TS 的
// `if (!isWin) return chunk.toString('utf8')`。
func newWinStreamDecoder(isWindows bool) *winStreamDecoder {
	return &winStreamDecoder{enabled: isWindows}
}

// write 解码一块输出，返回可追加到结果的字符串。
//
// 状态机：
//
//	未判定 → 累积到 pending，尝试判定；判定成立则把 pending 全部解出
//	已判定 → 直接按确定编码解当前块（GBK 走持久转换器 / UTF-8 直通）
func (d *winStreamDecoder) write(chunk []byte) string {
	if !d.enabled {
		return string(chunk)
	}
	if len(chunk) == 0 {
		return ""
	}

	if d.decided {
		// 已判定：直接解当前块（pending 已在判定时清空）。
		return d.decode(chunk)
	}

	// 未判定：累积后尝试判定。
	d.pending = append(d.pending, chunk...)
	status := utf8PrefixStatus(d.pending)
	switch {
	case status.definiteInvalid:
		d.decided, d.isUTF8 = true, false
		d.transformer = simplifiedchinese.GBK.NewDecoder()
	case status.ok && !status.incomplete:
		d.decided, d.isUTF8 = true, true
	default:
		// 合法但尾部残缺——不足以判定，等更多字节。
		return ""
	}
	// 判定成立：把已累积的字节一次解出。
	buf := d.pending
	d.pending = nil
	return d.decode(buf)
}

// decode 按已判定的编码解一块字节。
//
// **前提**：d.decided 为真。调用方负责在判定时清空 pending。
func (d *winStreamDecoder) decode(chunk []byte) string {
	if d.isUTF8 {
		return string(chunk)
	}
	// GBK：拼接上次未消费的残留（可能是半个字符的首字节）。
	src := chunk
	if len(d.residue) > 0 {
		src = append(append([]byte{}, d.residue...), chunk...)
		d.residue = nil
	}
	out, rest := transformChunk(d.transformer, src)
	d.residue = rest
	return out
}

// end 冲刷残留（对账 TS 的 `end()`）。
//
// 未判定就结束（流只含一个残缺前缀，或全程无输出）→ 按 UTF-8 直通，不丢数据。
func (d *winStreamDecoder) end() string {
	if !d.enabled {
		return ""
	}
	if !d.decided {
		if len(d.pending) == 0 {
			return ""
		}
		// 从未判定：按 UTF-8 处理（对账 TS 的 `chunk.toString('utf8')` 兜底）。
		s := string(d.pending)
		d.pending = nil
		return s
	}
	if d.isUTF8 {
		return ""
	}
	// GBK：冲刷转换器（残留的半个字符在此产出替换字符）。
	out, _ := transformChunk(d.transformer, d.residue)
	d.residue = nil
	return out
}

// utf8PrefixStatus 判定一块字节的 UTF-8 前缀状态。
//
// 返回：
//   - ok            —— 目前是合法 UTF-8 前缀（可能尾部残缺）
//   - incomplete    —— 尾部是一个未完成的多字节序列（需更多字节才能判定）
//   - definiteInvalid —— 含确定非法的 UTF-8 字节序列
//
// **为什么不能直接用 utf8.Valid**：它以「完整性」为准，尾部残缺也返回 false。
// 而探测需要的恰是「残缺（等更多）」与「非法（判 GBK）」的区分——实测单字节
// `d6`（GBK 首字节）与 `e4`（UTF-8 首字节）的 utf8.Valid 都是 false，无法区分。
type utf8PrefixStatusResult struct {
	ok              bool
	incomplete      bool
	definiteInvalid bool
}

func utf8PrefixStatus(b []byte) utf8PrefixStatusResult {
	for i := 0; i < len(b); {
		c := b[i]
		if c < 0x80 {
			i++
			continue
		}
		var n int
		switch {
		case c&0xE0 == 0xC0:
			n = 2
		case c&0xF0 == 0xE0:
			n = 3
		case c&0xF8 == 0xF0:
			n = 4
		default:
			// 非法首字节（0x80-0xBF 的孤立续字节，或 0xF8+）。
			return utf8PrefixStatusResult{definiteInvalid: true}
		}
		// 检查续字节。
		for j := i + 1; j < i+n && j < len(b); j++ {
			if b[j]&0xC0 != 0x80 {
				// 该位置应是续字节但不是——确定非法。
				return utf8PrefixStatusResult{definiteInvalid: true}
			}
		}
		if i+n > len(b) {
			// 尾部残缺：已有的续字节都合法，等更多字节。
			return utf8PrefixStatusResult{ok: true, incomplete: true}
		}
		i += n
	}
	return utf8PrefixStatusResult{ok: true}
}

// transformChunk 用持久转换器解一块字节，返回（解出的文本, 未消费的残留字节）。
//
// **为什么返回残留**：GBK 转换器对不完整的多字节序列返回 `ErrShortSrc` 且
// **nSrc=0**（一个字节都不消费）——它要求调用方累积更多字节后重试。探针实测：
//
//	喂 d6          → nDst=0 nSrc=0 err=short source buffer
//	喂 d6 d0       → nDst=3 nSrc=2 out="中" err=nil
//	喂 d6 d0 ce    → nDst=3 nSrc=2 out="中" err=short source buffer（ce 未消费）
//
// 故未消费的尾部必须**留存到下次**（而非丢弃）——否则那半个字符永久丢失。
//
// **不用 transform.Bytes**：它每次新建状态，多字节字符被块边界切开时产出替换
// 字符（探针实测：逐块喂 GBK 字节全部得到 `ef bf bd`）。
func transformChunk(t transform.Transformer, src []byte) (string, []byte) {
	if t == nil {
		return string(src), nil
	}
	var dst []byte
	buf := make([]byte, 256)
	remaining := src
	for {
		nDst, nSrc, err := t.Transform(buf, remaining, false)
		dst = append(dst, buf[:nDst]...)
		remaining = remaining[nSrc:]
		if err != nil {
			if err == transform.ErrShortDst {
				continue // 输出缓冲不够，继续
			}
			// ErrShortSrc（输入不完整）或其他：停止，把未消费的留给下次。
			break
		}
		if len(remaining) == 0 {
			break
		}
	}
	return string(dst), remaining
}

// decodingWriter 是无上限的「解码后写入」包装器。
//
// 与 limitedWriter 的区别：后者带字节上限（bash 的 8MB 单流限制），本类型
// 只做解码——供 run_tests 这类「输出全量读入再解析」的场景用。
type decodingWriter struct {
	buf *bytes.Buffer
	dec *winStreamDecoder
}

func (w *decodingWriter) Write(p []byte) (int, error) {
	if w.dec != nil {
		w.buf.WriteString(w.dec.write(p))
	} else {
		w.buf.Write(p)
	}
	return len(p), nil
}

// decodeWindowsOutput 是一次性解码（非流式），供不需跨块状态的场景用。
//
// 语义等价于「newWinStreamDecoder + 单次 write + end」。
func decodeWindowsOutput(raw []byte, isWindows bool) string {
	if !isWindows {
		return string(raw)
	}
	d := newWinStreamDecoder(true)
	s := d.write(raw)
	if rest := d.end(); rest != "" {
		s += rest
	}
	return strings.TrimSuffix(s, "")
}

// 保留 utf8 引用（utf8PrefixStatus 用位运算实现，此 import 供未来扩展）。
var _ = utf8.RuneStart
