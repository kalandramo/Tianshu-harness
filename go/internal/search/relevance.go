package search

import (
	"regexp"
	"strings"
)

// relevance.go —— 搜索结果的「跑题/降级」守卫（第九十七刀 · W3）。
//
// 对账 TS `src/tools/web-search/relevance.ts`（113 行）。
//
// # 背景（对账 TS 注释里的两轮实测复现）
//
//  1. **语言错位**——cn.bing.com 在请求携带英文语言标识时返回 HTTP 200、
//     结构完好、内容与查询**完全无关**的 SERP。零重叠判据兜住这一类。
//  2. **降级泛结果**——多词查询只匹配其中最泛的那个词
//     （`美国 AI 实验室 出逃 事件 7月 智能体` → 清一色「美国」百科/领事馆页面）。
//     这类结果**能命中一个泛词**，零重叠判据全数放行，于是劣质结果被当成答案
//     交给模型。实测 8 组真实 SERP 中 4 组降级。
//
// # 判据：按查询词覆盖计分
//
// 要求「覆盖 ≥2 个**不同查询词**」的结果过半数才判相关。
// **同一个查询词内的多枚 bigram 只计一词**——否则「量子计算」这一个词靠
// 量子/子计/计算 三枚 bigram 就能自重叠虚高，把只含该词的结果送过闸门。
//
// # 保守边界
//
// 查询提不出词元、或只有单个查询词（没有多词覆盖信号可比对）时，退回零重叠判据；
// 空结果集不判定（「没有结果」是 chain 的既有路径）。
// **宁可漏掉一次跑题，也不误杀一批正常结果**——误杀的代价是丢掉本来可用的答案。

// cjkChar 单个 CJK 字符——用来区分扫出来的片段属于中文还是拉丁。
var cjkChar = regexp.MustCompile(`[\x{4e00}-\x{9fff}]`)

// siteOperator 搜索操作符 `site:host`（web_map 的站点内搜索会拼出 `关键词 site:host`）。
var siteOperator = regexp.MustCompile(`(?i)\bsite:\S+`)

// chunkScan 扫描连续段（空格与标点都是分隔）。
//
// 对账 TS `const scan = /[a-z0-9\u4e00-\u9fff]+/gi`——**大小写不敏感**，
// 故拉丁词元会先被小写化（见 QueryTokenGroups）。
var chunkScan = regexp.MustCompile(`(?i)[a-z0-9\x{4e00}-\x{9fff}]+`)

// QueryTokenGroups 把查询切成「查询词 → 匹配词元」的分组。
//
// 切词：连续段（空格与标点都是分隔），长度 <2 的段丢弃。
// 词元：中文段按 bigram（`杭州西湖` → `杭州`/`州西`/`西湖`）；纯拉丁数字段整词小写。
// **数字与汉字同等参与 bigram**，所以 `7月` 这类混合词不会被整词漏掉。
//
// `site:host` 操作符**整段剔除**：域名只会出现在结果的 URL 里，而 URL 不参与匹配，
// 留着它会把「关键词 site:host」的站点内搜索一律判成跑题。
//
// **分组是判据的计分单位**：一个分组命中（组内任一词元出现在结果中）= 覆盖一个查询词。
// 词元顺序与查询中出现顺序一致——混合中英文时才能稳定断言。
func QueryTokenGroups(query string) [][]string {
	groups := [][]string{}
	// 单次扫描按位置切分，保证中英文混排时的产出顺序等于查询顺序。
	cleaned := siteOperator.ReplaceAllString(query, " ")
	for _, chunk := range chunkScan.FindAllString(cleaned, -1) {
		runes := []rune(chunk)
		if len(runes) < 2 {
			continue
		}
		if cjkChar.MatchString(chunk) {
			// 中文段按 bigram —— 按 **rune** 切，不是按字节
			// （TS 的 slice 按 UTF-16 code unit，BMP 内的 CJK 与 rune 等价）
			grams := make([]string, 0, len(runes)-1)
			for i := 0; i+2 <= len(runes); i++ {
				grams = append(grams, string(runes[i:i+2]))
			}
			groups = append(groups, grams)
		} else {
			groups = append(groups, []string{strings.ToLower(chunk)})
		}
	}
	return groups
}

// QueryTokens 是扁平词元列表（分组结构的展开）。
func QueryTokens(query string) []string {
	groups := QueryTokenGroups(query)
	tokens := []string{}
	for _, g := range groups {
		tokens = append(tokens, g...)
	}
	return tokens
}

// LooksOffTopic 判定一批结果是否整批跑题/降级。
//
// 判据：
//   - 空结果集 → false（「没有结果」是 chain 的既有路径，不归本守卫管）
//   - 查询提不出词元 → false（无判据可依时放行）
//   - 单个查询词 → 零重叠判据（任一命中即放行，全批零命中判跑题）
//   - 多个查询词 → 覆盖 ≥2 个不同查询词的结果过半数才放行；否则判跑题
//
// **URL 不参与匹配**：命中一个 slug 是噪声（`/hangzhou-xihu-menpiao` 可能挂在
// 完全无关的垃圾站上），只会让守卫失效。
func LooksOffTopic(query string, results []Result) bool {
	if len(results) == 0 {
		return false
	}
	groups := QueryTokenGroups(query)
	if len(groups) == 0 {
		return false
	}

	haystacks := make([]string, len(results))
	for i, r := range results {
		haystacks[i] = strings.ToLower(r.Title + " " + r.Snippet)
	}

	// 单查询词：没有「多词覆盖」信号可依，退回零重叠判据（保守）。
	if len(groups) == 1 {
		tokens := groups[0]
		for _, h := range haystacks {
			for _, tk := range tokens {
				if strings.Contains(h, tk) {
					return false
				}
			}
		}
		return true
	}

	// 多查询词：要求覆盖 ≥2 个查询词的结果过半数——单个泛词命中骗不过判据。
	multiCovered := 0
	for _, h := range haystacks {
		covered := 0
		for _, tokens := range groups {
			hit := false
			for _, tk := range tokens {
				if strings.Contains(h, tk) {
					hit = true
					break
				}
			}
			if hit {
				covered++
				if covered >= 2 {
					multiCovered++
					break
				}
			}
		}
	}
	return multiCovered*2 <= len(results)
}
