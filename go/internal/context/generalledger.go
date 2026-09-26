package context

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// generalledger.go —— 将星账本（`record_general_finding` / `recall_general` 的存储层）。
//
// 对账 TS `src/agent/general-ledger.ts`。
//
// # 什么是「将星账本」
//
// 每个星域（或「星域之外的将星」，如贪狼）有一份 markdown 账本
// （`.rivet/generals/<slug>.md`），按**族**（family）记录反复出现的失误模式。
// 同族复发时 `recurrenceCount` 递增——这是「这个坑踩过几次」的跨会话记忆。
//
// # 范围
//
// 本文件移植**纯逻辑 + 文件 I/O**（TS 的 12 个导出）。**未移植**：
// `setGeneralLedgerTelemetrySink` 的遥测（TS 用它喂 efficacy 统计；
// Go 侧遥测面不存在——见 `emitLedgerTelemetry` 的说明）。

// domainSlugByName 是「域中文名 → slug」的映射。
//
// **来源**：从 TS `src/agent/star-domain-data.ts` 逐条提取（16 个域）。
// **为什么不移植整表**：TS 那份 600+ 行含每个域的 prompt 正文、纪律、工具白名单——
// `starToGeneralSlug` **只需要 id 与 name**。移植全表会把无关的认知资产拖进来。
// **漂移风险**：TS 侧新增域时本表不会自动同步（已记于 HANDOFF）。
var domainSlugByName = map[string]string{
	"天枢": "tianshu", "破军": "pojun", "天府": "tianfu", "天梁": "tianliang",
	"天权": "tianquan", "天机": "tianji", "天璇": "tianxuan", "辅": "fu",
	"文曲": "wenqu", "开阳": "kaiyang", "瑶光": "yaoguang", "华盖": "huagai",
	"启明": "qiming", "长庚": "changgeng", "七杀": "qisha", "太一": "taiyi",
}

// domainSlugByID 是「域 id → 自身」的集合（用于校验小写输入是否为合法 id）。
var domainSlugByID = func() map[string]bool {
	m := make(map[string]bool, len(domainSlugByName))
	for _, slug := range domainSlugByName {
		m[slug] = true
	}
	return m
}()

// extraGeneralSlugs 是「星域之外的将星」——有胶囊/账本但无 star-domain id。
//
// 对账 TS 的同名常量（`general-ledger.ts:34`）。**当前只有贪狼**。
var extraGeneralSlugs = map[string]string{
	"贪狼": "tanlang",
}

// StarToGeneralSlug 把星名（中文或 slug）转成账本 slug；未知返回 ("", false)。
//
// 对账 TS `starToGeneralSlug`：
//
//	① trim；空 → null
//	② 小写后匹配域 id（`id === lower`）
//	③ 匹配域中文名（`domain.name === q`，**用 trim 后的原文**，非小写）
//	④ EXTRA_GENERAL_SLUGS 的中文键（`EXTRA_GENERAL_SLUGS[q]`）
//	⑤ EXTRA_GENERAL_SLUGS 的值（`Object.values(...).includes(lower)`）
//	⑥ 都不中 → null
//
// **大小写语义**（oracle 实测）：`TIANQUAN`/`TianQuan` → `tianquan`（走②，
// 因为 lower 后等于 id）；而中文名走③用原文——故 ` 天权 ` 也命中（trim 后）。
func StarToGeneralSlug(star string) (string, bool) {
	q := strings.TrimSpace(star)
	if q == "" {
		return "", false
	}
	lower := strings.ToLower(q)

	// ② 域 id
	if domainSlugByID[lower] {
		return lower, true
	}
	// ③ 域中文名（用 trim 后原文，非小写——中文无大小写，但混排时语义不同）
	if slug, ok := domainSlugByName[q]; ok {
		return slug, true
	}
	// ④ EXTRA 的中文键
	if slug, ok := extraGeneralSlugs[q]; ok {
		return slug, true
	}
	// ⑤ EXTRA 的值
	for _, slug := range extraGeneralSlugs {
		if slug == lower {
			return lower, true
		}
	}
	// ⑥ 未知
	return "", false
}

// GeneralLedgerPath 返回某 slug 的账本文件路径。
//
// 对账 TS `generalLedgerPath`：`join(cwd, '.rivet', 'generals', `${slug}.md`)`。
func GeneralLedgerPath(cwd, slug string) string {
	return filepath.Join(cwd, ".rivet", "generals", slug+".md")
}

// ReadGeneralLedger 读某星名的账本；未知星域或无文件时返回 ("", "", false)。
//
// 对账 TS `readGeneralLedger`：先解析 slug（未知 → null），再读文件
// （读失败 → null）。
func ReadGeneralLedger(cwd, star string) (slug, content string, ok bool) {
	slug, ok = StarToGeneralSlug(star)
	if !ok {
		return "", "", false
	}
	b, err := os.ReadFile(GeneralLedgerPath(cwd, slug))
	if err != nil {
		return "", "", false
	}
	return slug, string(b), true
}

// ListGenerals 列出 `.rivet/generals/` 下的全部 slug（按文件名排序，无 `.md` 后缀）。
//
// 对账 TS `listGenerals`：目录不存在或读失败 → 空切片。
func ListGenerals(cwd string) []string {
	dir := filepath.Join(cwd, ".rivet", "generals")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		out = append(out, strings.TrimSuffix(name, ".md"))
	}
	// `os.ReadDir` 已按文件名排序——与 TS `readdirSync` 的排序一致。
	return out
}

// LedgerFamily 是账本里的一个「族」（对账 TS `LedgerFamily`）。
type LedgerFamily struct {
	Family          string `json:"family"`
	RecurrenceCount int    `json:"recurrenceCount"`
	LastSeen        string `json:"lastSeen"`
	Signature       string `json:"signature"`
}

// familyHeadingRe 匹配族标题行。
//
// 对账 TS：`/^### (\S+) \| recurrenceCount: (\d+) \| lastSeen: (\S+)\s*$/`
//
// **注意 `\S+` 对族名**：中文族名（无空白）可匹配；含空格的族名**不匹配**
// （oracle 的 `sample_4` 钉住了「缺字段的行被忽略」）。
var familyHeadingRe = regexp.MustCompile(`^### (\S+) \| recurrenceCount: (\d+) \| lastSeen: (\S+)\s*$`)

// signatureRe 匹配 `**signature**: xxx` 或 `**signature**：xxx`（中英文冒号）。
//
// 对账 TS：`/^\*\*signature\*\*[：:]\s*(.+)$/`
var signatureRe = regexp.MustCompile(`^\*\*signature\*\*[：:]\s*(.+)$`)

// ParseLedgerFamilies 解析账本内容里的全部族。
//
// 对账 TS `parseLedgerFamilies`：
//
//	逐行扫；命中族标题 → 记 family/count/lastSeen；
//	然后**向下找** signature（遇到下一个 `### ` 或文件末尾为止）——
//	找到即停（只取第一个）。
//
// **`lastSeen` 不做格式校验**（TS 用 `\S+` 捕获，原样存）。
func ParseLedgerFamilies(content string) []LedgerFamily {
	lines := strings.Split(content, "\n")
	var families []LedgerFamily
	for i := 0; i < len(lines); i++ {
		m := familyHeadingRe.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		count, _ := strconv.Atoi(m[2])
		signature := ""
		for j := i + 1; j < len(lines) && !strings.HasPrefix(lines[j], "### "); j++ {
			if sig := signatureRe.FindStringSubmatch(lines[j]); sig != nil {
				signature = strings.TrimSpace(sig[1])
				break
			}
		}
		families = append(families, LedgerFamily{
			Family:          m[1],
			RecurrenceCount: count,
			LastSeen:        m[3],
			Signature:       signature,
		})
	}
	return families
}

// TopGeneralFamilies 返回某星名账本里 recurrenceCount 最高的前 n 个族。
//
// 对账 TS `topGeneralFamilies`：读账本（未知星域 → 空切片）→ 解析 →
// 按 count 降序 → 取前 n。
func TopGeneralFamilies(cwd, star string, n int) []LedgerFamily {
	_, content, ok := ReadGeneralLedger(cwd, star)
	if !ok {
		return nil
	}
	families := ParseLedgerFamilies(content)
	// 稳定排序（对账 TS 的 `.sort((a,b) => b.recurrenceCount - a.recurrenceCount)`）
	for i := 1; i < len(families); i++ {
		for j := i; j > 0 && families[j].RecurrenceCount > families[j-1].RecurrenceCount; j-- {
			families[j], families[j-1] = families[j-1], families[j]
		}
	}
	if n >= 0 && len(families) > n {
		families = families[:n]
	}
	return families
}

// GeneralFindingInput 是 `AppendGeneralFinding` 的输入（对账 TS `GeneralFindingInput`）。
type GeneralFindingInput struct {
	Star   string
	Family string
	Note   string
}

// GeneralFindingResult 是 `AppendGeneralFinding` 的结果（对账 TS `GeneralFindingResult`）。
type GeneralFindingResult struct {
	Slug            string
	Created         bool
	RecurrenceCount int
}

// ledgerMu 串行化账本写入（对账 TS 的同进程串行语义）。
//
// **为什么需要**：`AppendGeneralFinding` 是「读 → 改 → 写」的非原子序列。
// 并发写同一账本会丢更新。TS 侧靠 Node 单线程事件循环天然串行；Go 侧需显式锁。
var ledgerMu sync.Mutex

// AppendGeneralFinding 把一条发现记进账本。
//
// 对账 TS `appendGeneralFinding`：
//
//	① 解析 slug（未知星域 → nil，调用方报错）
//	② 文件不存在 → 新建（含标题 + `### <family> | recurrenceCount: 1 | lastSeen: <date>`）
//	③ 同族已存在 → 计数++、更新 lastSeen、**段尾插入实例行**
//	④ 族不存在 → 追加新族段
//
// **`lastSeen` 的日期格式**：TS 用 `new Date().toISOString().slice(0, 10)`（`YYYY-MM-DD`）。
func AppendGeneralFinding(cwd string, f GeneralFindingInput) (GeneralFindingResult, bool) {
	slug, ok := StarToGeneralSlug(f.Star)
	if !ok {
		return GeneralFindingResult{}, false
	}

	ledgerMu.Lock()
	defer ledgerMu.Unlock()

	path := GeneralLedgerPath(cwd, slug)
	date := time.Now().UTC().Format("2006-01-02")

	existing, err := os.ReadFile(path)
	if err != nil {
		// 新建：目录 + 文件
		if mkErr := os.MkdirAll(filepath.Dir(path), 0o755); mkErr != nil {
			return GeneralFindingResult{}, false
		}
		body := ledgerHeader(f.Star, slug) +
			"### " + f.Family + " | recurrenceCount: 1 | lastSeen: " + date + "\n\n" +
			"- " + date + " " + f.Note + "\n"
		if wErr := os.WriteFile(path, []byte(body), 0o644); wErr != nil {
			return GeneralFindingResult{}, false
		}
		return GeneralFindingResult{Slug: slug, Created: true, RecurrenceCount: 1}, true
	}

	lines := strings.Split(string(existing), "\n")

	// 找同族标题
	headingIdx := -1
	for i, ln := range lines {
		m := familyHeadingRe.FindStringSubmatch(ln)
		if m != nil && m[1] == f.Family {
			headingIdx = i
			break
		}
	}

	if headingIdx >= 0 {
		// 同族复发：计数++、更新 lastSeen、段尾插入实例行
		m := familyHeadingRe.FindStringSubmatch(lines[headingIdx])
		count, _ := strconv.Atoi(m[2])
		count++
		lines[headingIdx] = "### " + f.Family + " | recurrenceCount: " + strconv.Itoa(count) + " | lastSeen: " + date

		// 段尾 = 下一个 `### ` 或 `---` 或 EOF
		end := len(lines)
		for i := headingIdx + 1; i < len(lines); i++ {
			if strings.HasPrefix(lines[i], "### ") || strings.HasPrefix(lines[i], "---") {
				end = i
				break
			}
		}
		// 回退越过段尾空行，让实例行紧贴段落内容
		insertAt := end
		for insertAt > headingIdx+1 && strings.TrimSpace(lines[insertAt-1]) == "" {
			insertAt--
		}
		instanceLine := "- " + date + " " + f.Note
		lines = append(lines[:insertAt], append([]string{instanceLine}, lines[insertAt:]...)...)

		if wErr := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); wErr != nil {
			return GeneralFindingResult{}, false
		}
		return GeneralFindingResult{Slug: slug, Created: false, RecurrenceCount: count}, true
	}

	// 族不存在：追加新族段
	body := string(existing)
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	body += "\n### " + f.Family + " | recurrenceCount: 1 | lastSeen: " + date + "\n\n" +
		"- " + date + " " + f.Note + "\n"
	if wErr := os.WriteFile(path, []byte(body), 0o644); wErr != nil {
		return GeneralFindingResult{}, false
	}
	return GeneralFindingResult{Slug: slug, Created: false, RecurrenceCount: 1}, true
}

// ledgerHeader 生成新建账本的头部。
//
// 对账 TS `appendGeneralFinding` 的 `!exists` 分支。
func ledgerHeader(star, slug string) string {
	return "# " + star + " 将星账本\n\n" +
		"slug: " + slug + "\n\n" +
		"本文件由 `record_general_finding` 维护——记录反复出现的失误模式（族）。\n" +
		"同族复发时 `recurrenceCount` 递增。\n\n"
}
