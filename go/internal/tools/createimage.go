package tools

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/contract"
)

// createimage.go —— `create_image` 工具（第八十三刀 · W1-5）。
//
// 对账 TS `src/tools/create-image.ts`（约 100 行）。
//
// 纯字符串（SVG 包裹 + markdown fence 剥离）——零第三方库。
//
// # 两个不同文案（勿合并）
//
//   - `createImage` 校验：`svg 为必填项`
//   - `renderImage` 校验：`svg 内容为必填项`
//
// TS 侧确实是两处不同的 throw——复刻以免文案回归。

// CreateImage 创建 `create_image` 工具。
func CreateImage(cwd string) Tool { return &createImageTool{cwd: cwd} }

type createImageTool struct{ cwd string }

func (t *createImageTool) Definition() contract.Definition {
	return contract.Definition{
		Name: "create_image",
		Description: "从 SVG 标记创建 SVG 图片文件。\n\n" +
			"SVG 内容可以是完整 <svg>...</svg> 文档、原始 SVG 内部元素，或 markdown 代码块。" +
			"用于 logo、图表、示意图、插画等各种矢量图形。\n\n" +
			"用 open_path 在 OS 默认查看器中打开生成的图片。\n\n" +
			"示例：\n" +
			"Good: create_image(destination_path=\"~/Desktop/logo.svg\", svg=\"<svg xmlns=\\\"http://www.w3.org/2000/svg\\\" viewBox=\\\"0 0 100 100\\\"><circle cx=\\\"50\\\" cy=\\\"50\\\" r=\\\"40\\\" fill=\\\"#38bdf8\\\"/></svg>\")\n" +
			"Good: create_image(destination_path=\"~/Desktop/chart.svg\", svg=\"<rect x=\\\"10\\\" y=\\\"20\\\" width=\\\"30\\\" height=\\\"40\\\" fill=\\\"#818cf8\\\"/>\", width=200, height=120)",
		InputSchema: objSchemaOrdered(
			[]string{"destination_path", "svg", "width", "height"},
			map[string]any{
				"destination_path": strProp("目标文件路径。应以 .svg 结尾。可在项目之外。"),
				"svg":              strProp("SVG 标记——完整 <svg> 文档、内部 SVG 元素，或 markdown 代码块均可。"),
				"width":            numProp("可选的 SVG 元素 width 属性。"),
				"height":           numProp("可选的 SVG 元素 height 属性。"),
			}, "destination_path", "svg"),
	}
}

func (t *createImageTool) Execute(_ context.Context, p *CallParams) (contract.Result, error) {
	dest, _ := p.Input["destination_path"].(string)
	if strings.TrimSpace(dest) == "" {
		return contract.Result{Content: "错误：destination_path 为必填项", IsError: true}, nil
	}
	svg, _ := p.Input["svg"].(string)
	if strings.TrimSpace(svg) == "" {
		return contract.Result{Content: "错误：svg 为必填项", IsError: true}, nil
	}

	rendered, err := renderImage(svg, numPtr(p.Input["width"]), numPtr(p.Input["height"]))
	if err != nil {
		return contract.Result{Content: "错误：" + err.Error(), IsError: true}, nil
	}

	res, err := exportFileRun(map[string]any{
		"destination_path": dest,
		"content":          rendered,
	})
	if err != nil {
		return contract.Result{Content: "错误：" + err.Error(), IsError: true}, nil
	}
	return contract.Result{
		Content: fmt.Sprintf("已创建 svg 图片（%d 字节）：%s", res.bytes, res.path),
	}, nil
}

func (t *createImageTool) RequiresApproval(_ *CallParams) bool { return true }
func (t *createImageTool) ConcurrencySafe() bool               { return false }
func (t *createImageTool) Enabled() bool                       { return true }
func (t *createImageTool) Timeout(_ *CallParams) time.Duration { return 0 }

// numPtr 把 JSON 数字转成 *float64（缺失/非数字时返回 nil）。
func numPtr(v any) *float64 {
	switch x := v.(type) {
	case float64:
		return &x
	case int:
		f := float64(x)
		return &f
	case int64:
		f := float64(x)
		return &f
	}
	return nil
}

// svgFenceRe 匹配 markdown 代码围栏（对账 TS ` /```(?:svg)?\r?\n([\s\S]*?)\r?\n```/`）。
var svgFenceRe = regexp.MustCompile("(?s)```(?:svg)?\r?\n(.*?)\r?\n```")

// svgOpenRe 判断是否已是完整 svg 文档（对账 TS `/^<svg\b/i`）。
var svgOpenRe = regexp.MustCompile(`(?i)^<svg\b`)

// extractSvgInner 对账 TS `extractSvgInner`。
//
// ① 已是完整 `<svg>` → 原样；② markdown 围栏 → 剥离；③ 其余 → 原样（trim）。
func extractSvgInner(raw string) string {
	stripped := strings.TrimSpace(raw)
	if svgOpenRe.MatchString(stripped) {
		return stripped
	}
	if m := svgFenceRe.FindStringSubmatch(stripped); m != nil && len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	return stripped
}

// renderImage 对账 TS `renderImage`（含 `wrapSvg`）。
//
// 已是完整 svg → 补一个换行；否则用 `wrapSvg` 包裹。
// **注意**：校验文案是「svg 内容为必填项」（与 Execute 层的「svg 为必填项」不同）。
func renderImage(raw string, width, height *float64) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("svg 内容为必填项")
	}
	inner := extractSvgInner(raw)
	if svgOpenRe.MatchString(inner) {
		return inner + "\n", nil
	}
	return wrapSvg(inner, width, height), nil
}

// wrapSvg 对账 TS `wrapSvg`。
//
// `<svg ${attrs}>\n${inner}\n</svg>\n`；attrs 依次是 xmlns、width、height，
// 仅当宽高**都给**时才追加 viewBox。
func wrapSvg(inner string, width, height *float64) string {
	attrs := `xmlns="http://www.w3.org/2000/svg"`
	if width != nil {
		attrs += fmt.Sprintf(` width="%s"`, formatNum(*width))
	}
	if height != nil {
		attrs += fmt.Sprintf(` height="%s"`, formatNum(*height))
	}
	if width != nil && height != nil {
		attrs += fmt.Sprintf(` viewBox="0 0 %s %s"`, formatNum(*width), formatNum(*height))
	}
	return "<svg " + attrs + ">\n" + inner + "\n</svg>\n"
}

// formatNum 把 float64 格式化为 JS 风格的数字串（整数不带小数点）。
//
// 对账 JS 的模板串插值 `String(n)`——`200` 而非 `200.000000`。
func formatNum(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprintf("%d", int64(f))
	}
	return fmt.Sprintf("%g", f)
}
