package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/api/wire"
	"github.com/kalandramo/tianshu/go/internal/contract"
	tnet "github.com/kalandramo/tianshu/go/internal/net"
)

// webfetch.go —— `web_fetch` 工具（第九十三刀 · W3-4d）。
//
// 对账 TS `src/tools/web-fetch/tool.ts`（230 行）。
//
// # 三个分支
//
//  1. **urls 批量**（上限 10）——逐页独立走共享内核，部分失败不整体失败
//  2. **actions 渲染**——Go 侧**未移植**（需 Playwright），诚实报错
//  3. **常规单页**——共享内核 + maxCharacters 截断
//
// # 有意不移植（诚实披露）
//
// `actions`（渲染后交互：点击/输入/滚动/执行 JS）依赖 Playwright——
// Go 侧无对应物（对账 TS `render-fetch.ts` 381 行 + `render-actions.ts` 184 行）。
// 故该分支**诚实报错**而非假装成功。
//
// # 依赖已就位
//
// 本工具是 `internal/net` 三层（SSRF / HTTP 抓取 / HTML→MD）+ 缓存 +
// fetch-core 的**消费者**——那些层此前「零生产消费者」，本刀让它们全部接线。

// maxFetchURLs 对账 TS `MAX_URLS`。
const maxFetchURLs = 10

// WebFetch 创建 `web_fetch` 工具。
func WebFetch(cwd string) Tool { return &webFetchTool{cwd: cwd} }

type webFetchTool struct{ cwd string }

func (t *webFetchTool) Definition() contract.Definition {
	// actions 的嵌套 schema：items 是 object，含 type 枚举与 7 个可选字段。
	actionsItem := objPropMapOrdered(
		[]string{"type", "ms", "selector", "all", "text", "key", "direction", "script"},
		map[string]any{
			"type":     enumPropOrdered("", []string{"wait", "click", "write", "press", "scroll", "execute_js"}),
			"ms":       numProp("wait：等待毫秒数"),
			"selector": strProp("目标元素 CSS 选择器（wait/click/write/press/scroll）"),
			"all":      boolProp("click：点击所有匹配元素（默认 false 只点第一个）"),
			"text":     strProp("write：要填入的文本"),
			"key":      strProp("press：按键名（如 Enter/Tab/Escape）"),
			"direction": enumPropOrdered(
				"scroll：滚动方向（默认 down）",
				[]string{"down", "up", "top", "bottom"},
			),
			"script": strProp("execute_js：在页面上下文执行的 JS 表达式，返回值随结果返回"),
		},
		"type",
	)

	actions := wireArrItemsFirst(
		"渲染后按序执行的交互动作（≤50 步，wait 总时长 ≤60s）。仅 Playwright 渲染可用时生效。",
		actionsItem,
	)
	urls := wireArrItemsFirst(
		"一次抓取多个 URL（与 url 二选一；批量时优先于 url）。逐页输出，部分失败不整体失败。",
		wire.NewOrderedMap().Set("type", "string"),
	)

	return contract.Definition{
		Name: "web_fetch",
		Description: "抓取 URL 内容并以文本返回。适合阅读文档、API 参考或 issue 页面。\n" +
			"\t\t返回转换为纯文本的页面内容（已剥离 HTML 标签）。内容截断至约 50K 字符。\n" +
			"\t\t本地提取质量差时自动用本地浏览器渲染（SPA 页面）或 Jina Reader 兜底；重复抓取走缓存。\n" +
			"\t\t可选 actions：在渲染页面中按序交互（点击/输入/滚动/等待/执行 JS），用于登录墙、无限滚动、Tab 内容——需启用 Playwright。\n" +
			"\t\t因发起网络请求，需要用户审批。",
		InputSchema: objSchemaOrdered(
			[]string{"url", "urls", "maxCharacters", "actions"},
			map[string]any{
				"url":           strProp("要抓取的 URL"),
				"urls":          urls,
				"maxCharacters": numProp("每页 markdown 按字符数截断（省略则不截断——保持现有行为）。"),
				"actions":       actions,
			},
			// **required 为空**（对账 TS：`required: []`）
		),
	}
}

func (t *webFetchTool) Execute(ctx context.Context, p *CallParams) (contract.Result, error) {
	rawURL, _ := p.Input["url"].(string)
	urlsRaw, hasURLs := p.Input["urls"]
	_, hasActions := p.Input["actions"]

	// urls 与 actions 互斥：批量抓取走共享降级链，不支持渲染动作序列。
	if hasURLs && hasActions {
		return contract.Result{
			Content: "urls 与 actions 不能同时使用：actions 仅单页渲染路径支持，批量抓取不渲染。",
			IsError: true,
		}, nil
	}

	// maxCharacters 校验：非有限数/负数 → 忽略（fallback 不截断，保持现有行为）。
	truncateTo := -1
	if v, ok := p.Input["maxCharacters"].(float64); ok && v >= 0 {
		truncateTo = int(v)
	}

	// ── 分支 1：批量 ──
	if hasURLs {
		urlsArr, ok := urlsRaw.([]any)
		if !ok {
			return contract.Result{Content: "urls 必须为 URL 字符串数组。", IsError: true}, nil
		}
		if len(urlsArr) > maxFetchURLs {
			return contract.Result{
				Content: fmt.Sprintf("一次最多抓取 %d 个 URL（收到 %d 个）", maxFetchURLs, len(urlsArr)),
				IsError: true,
			}, nil
		}
		return t.executeBatch(ctx, urlsArr, truncateTo), nil
	}

	// ── 分支 2：actions（Go 侧未移植 Playwright）──
	if hasActions {
		return contract.Result{
			Content: "actions 需要启用 Playwright 渲染（config fetch.enablePlaywright，或桌面端内置 chromium）。" +
				"Go 侧当前未移植 Playwright 渲染层——本能力不可用。",
			IsError: true,
		}, nil
	}

	// ── 分支 3：常规单页 ──
	if strings.TrimSpace(rawURL) == "" {
		return contract.Result{Content: "url 为必填项（或改用 urls 批量抓取）。", IsError: true}, nil
	}

	out := t.fetchOne(ctx, rawURL)
	if !out.OK {
		return contract.Result{Content: out.Error, IsError: true}, nil
	}

	markdown := out.Markdown
	note := ""
	if truncateTo >= 0 && len(markdown) > truncateTo {
		markdown = truncateToChars(markdown, truncateTo)
		note = fetchTruncationNote(truncateTo)
	}

	var header string
	if out.FromCache {
		header = fmt.Sprintf("URL：%s\n状态：%d（缓存，%s前抓取%s）\n内容长度：%d%s",
			rawURL, out.Status, tnet.FormatCacheAge(out.FetchedAt, nowMs()), out.Via, len(markdown), note)
	} else {
		header = fmt.Sprintf("URL：%s\n状态：%d\n内容长度：%d%s%s",
			rawURL, out.Status, out.RawBytes, out.Via, note)
	}
	return contract.Result{Content: header + "\n\n" + markdown}, nil
}

// executeBatch 对账 TS 的批量分支。
//
// **部分失败不整体失败**——成功的页照常输出，失败进尾部错误块。
func (t *webFetchTool) executeBatch(ctx context.Context, urls []any, truncateTo int) contract.Result {
	var sections []string
	var errs []string

	for i, raw := range urls {
		u, ok := raw.(string)
		if !ok {
			errs = append(errs, fmt.Sprintf("错误 %v：无效 URL", raw))
			continue
		}
		out := t.fetchOne(ctx, u)
		if !out.OK {
			errs = append(errs, fmt.Sprintf("错误 %s：%s", u, out.Error))
			continue
		}
		markdown := out.Markdown
		note := ""
		if truncateTo >= 0 && len(markdown) > truncateTo {
			markdown = truncateToChars(markdown, truncateTo)
			note = fetchTruncationNote(truncateTo)
		}
		var header string
		if out.FromCache {
			header = fmt.Sprintf("状态：%d（缓存，%s前抓取%s）\n内容长度：%d%s",
				out.Status, tnet.FormatCacheAge(out.FetchedAt, nowMs()), out.Via, len(markdown), note)
		} else {
			header = fmt.Sprintf("状态：%d\n内容长度：%d%s%s",
				out.Status, out.RawBytes, out.Via, note)
		}
		sections = append(sections, fmt.Sprintf("### %d. %s\n%s\n\n%s", i+1, u, header, markdown))
	}

	if len(sections) == 0 {
		return contract.Result{Content: strings.Join(errs, "\n"), IsError: true}
	}
	body := strings.Join(sections, "\n\n")
	if len(errs) > 0 {
		body += "\n\n" + strings.Join(errs, "\n")
	}
	return contract.Result{Content: body}
}

// fetchOne 调用共享内核（对账 TS 的 `fetchMarkdown`）。
func (t *webFetchTool) fetchOne(ctx context.Context, rawURL string) tnet.FetchMarkdownOutcome {
	return tnet.FetchMarkdown(ctx, rawURL, tnet.FetchCoreDeps{}, tnet.FetchMarkdownOptions{Cwd: t.cwd})
}

// RequiresApproval 恒 true（对账 TS `() => true`）——发起网络请求。
func (t *webFetchTool) RequiresApproval(_ *CallParams) bool { return true }

// ConcurrencySafe 恒 true（对账 TS `() => true`）——只读抓取。
func (t *webFetchTool) ConcurrencySafe() bool { return true }

// Enabled 恒 true（对账 TS `() => true`）。
func (t *webFetchTool) Enabled() bool { return true }

// Timeout 用默认（0）——内核自带 15s 超时。
func (t *webFetchTool) Timeout(_ *CallParams) time.Duration { return 0 }

// fetchTruncationNote 是 web_fetch 的截断提示（**与 truncation.go 的
// `truncationNote` 常量不同**——那是工具结果截断的通用提示，语义不同）。
func fetchTruncationNote(n int) string {
	return fmt.Sprintf("（已按 %d 字符截断）", n)
}

// truncateToChars 按 **UTF-16 code unit** 截断（对账 TS `markdown.slice(0, n)`）。
//
// **为什么不自己写 rune 切片**：JS 的 `String.slice` 按 UTF-16 code unit 计数，
// 而 `truncation.go` 已实现精确的 `UTF16Len` + `jsSliceHead`（处理代理对边界）。
// 复用它而非重复实现——语义等价性交给那层的测试保证。
func truncateToChars(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if UTF16Len(s) <= n {
		return s
	}
	return jsSliceHead(s, n)
}

// nowMs 返回当前毫秒时间戳。
func nowMs() int64 { return time.Now().UnixMilli() }
