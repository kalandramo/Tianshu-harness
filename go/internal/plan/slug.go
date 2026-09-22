// Package plan —— 计划文件的生命周期（slug 生成、状态标记、闭环勾选、
// 事实锚点校验）。
//
// 对账 TS `src/plan/` 下的 plan-store.ts / plan-close.ts / plan-fact-anchors.ts。
// 本包只做**纯逻辑与文件系统**，不含 TUI/审批面板（Go 侧无渲染层）。
package plan

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// slugMaxUTF16 是 slug 的截断长度（UTF-16 code unit）。
//
// 对账 TS `plan-store.ts:127` 的 `.slice(0, 80)`。
const slugMaxUTF16 = 80

// fallbackSlug 是 slug 为空时的兜底值。
//
// 对账 TS `plan-store.ts:127` 的 `|| 'plan'`。
const fallbackSlug = "plan"

// Slugify 从标题生成安全文件名（对账 TS `plan-store.ts:122-128`）。
//
//	text.toLowerCase().replace(/[^a-z0-9\u4e00-\u9fff]+/g, '-')
//	    .replace(/^-+|-+$/g, '').slice(0, 80) || 'plan'
//
// # 三个必须复刻的语义
//
//  1. **小写化**：JS 的 `toLowerCase()` 是 Unicode 感知的；Go 的
//     `strings.ToLower` 语义一致（都按 Unicode 简单/全大小写映射）。
//  2. **非法字符折叠**：`[^a-z0-9\u4e00-\u9fff]+` 是**连续**非法字符折叠为
//     单个 `-`（`+` 量词），不是逐个替换。CJK 基本区（U+4E00–U+9FFF）
//     被**保留**（不转写为拼音）。
//  3. **UTF-16 截断**：`.slice(0, 80)` 按 UTF-16 code unit 切——emoji（代理对）
//     计 2。Go 的 `len()` 是字节、`[]rune` 是码点，**两者都不对**。
//     切在代理对中间会产生孤立代理 → U+FFFD（与 JS 一致）。
func Slugify(text string) string {
	lowered := strings.ToLower(text)

	var b strings.Builder
	b.Grow(len(lowered))
	prevDash := false
	for _, r := range lowered {
		if isSlugSafe(r) {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		// 连续非法字符折叠为单个 '-'。
		if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}

	s := b.String()
	s = strings.Trim(s, "-")
	// 截断用 UTF-16 code unit（对账 JS `.slice(0, 80)`）。
	s = sliceByUTF16(s, slugMaxUTF16)
	if s == "" {
		return fallbackSlug
	}
	return s
}

// isSlugSafe 报告字符是否保留在 slug 中（对账 TS 的 `[a-z0-9\u4e00-\u9fff]`）。
//
// **注意**：TS 的正则跑在 `toLowerCase()` 之后，故只列小写字母。
func isSlugSafe(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z':
		return true
	case r >= '0' && r <= '9':
		return true
	case r >= 0x4E00 && r <= 0x9FFF: // CJK 统一表意文字（基本区）
		return true
	}
	return false
}

// sliceByUTF16 按 UTF-16 code unit 取前 n 个（对账 JS 的 `s.slice(0, n)`）。
//
// **为什么本地实现而非复用 `prompt` 包的**：`prompt.sliceByUTF16` 是**未导出**的，
// 为一个小需求把它提升为导出 API 会扩大 `prompt` 的公开面；且 `plan` 依赖
// `prompt` 属层次倒置（prompt 是渲染层，plan 是数据层）。此处 10 行等价实现，
// 语义与 `prompt/truncate.go:117` 逐字一致（该处已有差分对账）。
//
// 切在代理对中间 → 孤立代理 → U+FFFD（与 JS 编码行为一致）。
func sliceByUTF16(s string, n int) string {
	if n <= 0 {
		return ""
	}
	units := utf16.Encode([]rune(s))
	if n >= len(units) {
		return s
	}
	decoded := utf16.Decode(units[:n])
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range decoded {
		if r == utf8.RuneError {
			b.WriteRune('\uFFFD')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// utf16Len 返回 UTF-16 code unit 数（对账 JS 的 `String.prototype.length`）。
//
// 同 `sliceByUTF16` 的理由：本地实现，避免跨包依赖与公开面扩张。
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}
