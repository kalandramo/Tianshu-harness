package compact

import (
	"regexp"

	"github.com/kalandramo/tianshu/go/internal/session"
)

// filePathPattern 匹配「以 / 开头、含 1-6 位小写扩展名」的路径 token。
//
// 对账 TS `buildHandoffFromState` 里的
// `/(?:\/[^\s\n"'` + "`" + `{}()[\]]+\.[a-z]{1,6})\b/g`。
//
// TS 原模式含反引号（在 Go 的反引号字符串里无法直写），故用双引号字符串
// 构造。RE2 与 JS 正则在本模式上语义一致（无反向引用/前瞻）。
var filePathPattern = regexp.MustCompile(`/[^\s\n"'` + "`" + `{}()\[\]]+\.[a-z]{1,6}\b`)

// extractFilePaths 从 tool 消息中提取文件路径（去重保序）。
//
// 对账 TS 的 filesSeen 集合（用 Set 保插入序）。
func extractFilePaths(messages []session.OaiMessage) []string {
	seen := make(map[string]bool)
	var out []string
	for _, m := range messages {
		if m.Role != "tool" || m.Content == nil {
			continue
		}
		for _, match := range filePathPattern.FindAllString(*m.Content, -1) {
			if !seen[match] {
				seen[match] = true
				out = append(out, match)
			}
		}
	}
	return out
}

// formatPercent 把比例格式化为整数百分比（对账 TS 的
// `(ratio * 100).toFixed(0)`——四舍五入）。
func formatPercent(ratio float64) string {
	pct := int(ratio*100 + 0.5)
	if pct < 0 {
		pct = 0
	}
	return itoaCompact(pct) + "%"
}

// itoaCompact 是无依赖的整数转字符串。
func itoaCompact(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
