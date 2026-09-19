package prompt

import "strings"

// ExtractPatchTargetPaths 复刻 src/tools/apply-patch.ts:34 的
// extractPatchTargetPaths —— 从 unified diff 提取目标文件路径。
//
// 语义（对账 TS 逐行）：
//   - 只看以 `+++ ` 开头的行
//   - 取 `+++ ` 之后的内容并 trim
//   - 含 tab 则截断到 tab 之前（git 的 `+++ b/file\ttimestamp` 形态）
//   - `/dev/null` 跳过（纯删除，无应用后内容可验）
//   - 去掉首尾成对引号（git 对含特殊字符的路径加引号）
//   - 去掉开头的 `a/` 或 `b/` 前缀
//   - 空串跳过；Set 去重；**保持首次出现顺序**
//
// 注意一个边界（oracle 锁定）：diff 正文里以 `+++ ` 开头的行**也会**被当作
// 路径行——TS 的判定只看行首前缀，不区分头部与正文。这是真实行为，需复刻。
func ExtractPatchTargetPaths(diff string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, line := range strings.Split(diff, "\n") {
		if !strings.HasPrefix(line, "+++ ") {
			continue
		}
		p := trimSpaceUnicode(line[4:])
		if idx := strings.Index(p, "\t"); idx != -1 {
			p = p[:idx]
		}
		if p == "/dev/null" {
			continue
		}
		// 去成对引号（TS 的正则 ^"(.*)"$ 是贪婪的——只去最外层）
		if len(p) >= 2 && strings.HasPrefix(p, `"`) && strings.HasSuffix(p, `"`) {
			p = p[1 : len(p)-1]
		}
		// 去开头的 a/ 或 b/ 前缀（TS 的 /^[ab]\// 只去一次）
		if len(p) >= 2 && (p[0] == 'a' || p[0] == 'b') && p[1] == '/' {
			p = p[2:]
		}
		if len(p) > 0 && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}
