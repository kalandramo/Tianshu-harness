package prompt

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// HashLine 复刻 src/tools/hash-edit.ts:22 的 hashLine —— 行哈希。
//
// **关键语义**：先剥掉行尾的 `\r` 再哈希（CRLF 归一化）。故 "abc" 与 "abc\r"
// 哈希相同。这是为了让同一文件在 LF/CRLF 两种检出的锚点一致——否则跨平台
// 编辑时锚点会失配。
//
// 输出是 sha256 十六进制的前 8 位。
func HashLine(line string) string {
	clean := line
	if strings.HasSuffix(clean, "\r") {
		clean = clean[:len(clean)-1]
	}
	sum := sha256.Sum256([]byte(clean))
	return hex.EncodeToString(sum[:])[:8]
}

// BuildFreshAnchors 复刻 src/tools/hash-edit.ts:37 的 buildFreshAnchors ——
// 编辑后为刚写入的区域生成「链式安全」的新鲜锚点，让模型无需 read_file
// 即可再次编辑同一文件。
//
// 最多 4 个锚点（对账 TS 的四个条件）：
//  1. 编辑点**前一行**（editStart0 > 0 时）
//  2. 新内容**首行**（newLineCount > 0 时）
//  3. 新内容**末行**（newLineCount > 1 时——只有一行时不重复）
//  4. 编辑点**后一行**（editStart0+newLineCount < len 时）
//
// 每个锚点的内容部分：`replace(/\s+$/, ”)` 去行尾空白后截断 80 字符（超出加
// `…`）。**哈希用原始行**（未 trim）——故含 `\r` 的行哈希与去 `\r` 后相同，
// 但展示内容不含 `\r`（它被 \s+$ 吃掉）。
//
// 无锚点时返回 ""（对账 TS 的 parts.length > 0 分支）。
func BuildFreshAnchors(newFileLines []string, editStart0, newLineCount int) string {
	lineWithContent := func(idx int) string {
		raw := ""
		if idx >= 0 && idx < len(newFileLines) {
			raw = newFileLines[idx]
		}
		// 内容：去行尾空白（JS 的 \s 覆盖 unicode 空白）
		content := trimEndUnicode(raw)
		snippet := content
		if UTF16Len(content) > 80 {
			snippet = sliceByUTF16(content, 80) + "…"
		}
		return "L" + itoa(idx+1) + ":" + HashLine(raw) + " → " + snippet
	}

	parts := []string{}
	if editStart0 > 0 {
		parts = append(parts, lineWithContent(editStart0-1))
	}
	if newLineCount > 0 {
		parts = append(parts, lineWithContent(editStart0))
	}
	if newLineCount > 1 {
		parts = append(parts, lineWithContent(editStart0+newLineCount-1))
	}
	if editStart0+newLineCount < len(newFileLines) {
		parts = append(parts, lineWithContent(editStart0+newLineCount))
	}
	if len(parts) == 0 {
		return ""
	}
	return "\n新鲜锚点（链式安全）：\n" + strings.Join(parts, "\n")
}
