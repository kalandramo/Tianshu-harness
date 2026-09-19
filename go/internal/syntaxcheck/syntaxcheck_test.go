package syntaxcheck

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// oracleCase 是 TS checkSyntax 的一条判定结果。
type oracleCase struct {
	File       string `json:"file"`
	Note       string `json:"note"`
	Content    string `json:"content"`
	HasFatal   bool   `json:"hasFatal"`
	HasWarning bool   `json:"hasWarning"`
}

func loadOracle(t *testing.T) map[string]oracleCase {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "syntaxcheck", "oracle.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败（%s）：%v\n生成：npx tsx go/testdata/syntaxcheck/gen-oracle.ts", path, err)
	}
	var o map[string]oracleCase
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return o
}

// TestCheckParityWithTS —— 逐用例对账**判定**（fatal 有/无）与 TS 一致。
//
// **不对账消息文本**：Go 与 JS 的解析器错误文本天然不同
// （`json.Unmarshal` vs `JSON.parse`）。判定才是调用方依赖的契约
// （`fatal != nil` → 回滚）。
func TestCheckParityWithTS(t *testing.T) {
	oracle := loadOracle(t)
	checked := 0
	mismatches := 0

	for key, want := range oracle {
		checked++
		t.Run(key, func(t *testing.T) {
			got := Check(want.File, want.Content)
			gotFatal := got.Fatal != ""
			if gotFatal != want.HasFatal {
				mismatches++
				t.Errorf("判定不符（%s）：Go fatal=%v TS fatal=%v\n  Go 消息=%q",
					want.Note, gotFatal, want.HasFatal, got.Fatal)
			}
			// warning 与 fatal 在 TS 侧总是同生同灭（同一个 msg）
			if want.HasWarning != want.HasFatal {
				t.Logf("注意：TS 侧 warning 与 fatal 不同步（%s）——请核对 oracle", want.Note)
			}
		})
	}
	if checked == 0 {
		t.Fatal("oracle 无用例")
	}
	if mismatches > 0 {
		t.Errorf("%d/%d 用例判定不符", mismatches, checked)
	}
	t.Logf("对账了 %d 个用例", checked)
}

// TestGoSyntaxNative —— `.go` 用原生解析器（生态重映射，无 oracle）。
func TestGoSyntaxNative(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool // 是否应 fatal
	}{
		{"合法", "package main\n\nfunc main() {}\n", false},
		{"缺右花括号", "package main\n\nfunc main() {\n", true},
		{"缺 package", "func main() {}\n", true},
		{"非法 token", "package main\n\nfunc @@@ {}\n", true},
		{"未闭合字符串", "package main\n\nvar s = \"unclosed\n", true},
		{"空文件", "", true},
		{"只有 package", "package main\n", false},
		{"多余右花括号", "package main\n\nfunc main() {}\n}\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Check("x.go", tc.content)
			if (got.Fatal != "") != tc.want {
				t.Errorf("fatal=%v，期望 %v\n  消息=%q", got.Fatal != "", tc.want, got.Fatal)
			}
		})
	}
}

// TestUnknownExtensionsOK —— 未覆盖的扩展名一律通过（对账 TS 的兜底）。
func TestUnknownExtensionsOK(t *testing.T) {
	for _, f := range []string{"a.ts", "a.tsx", "a.js", "a.jsx", "a.py", "a.md", "a.txt", "noext"} {
		got := Check(f, "this is {{{ not valid anything >>>")
		if got.Fatal != "" {
			t.Errorf("%s 应通过（未覆盖的扩展名），得到 fatal=%q", f, got.Fatal)
		}
	}
}

// TestSizeLimitSkips —— 超限文件跳过检查（对账 TS 的尺寸门）。
func TestSizeLimitSkips(t *testing.T) {
	// CSS 超 2MB → 跳过（即使内容明显不平衡）
	big := make([]byte, syncScanSizeLimit+10)
	for i := range big {
		big[i] = '{' // 全是不平衡的花括号
	}
	if got := Check("big.css", string(big)); got.Fatal != "" {
		t.Errorf("超限 CSS 应跳过检查，得到 fatal=%q", got.Fatal)
	}
	// HTML 同样
	if got := Check("big.html", string(big)); got.Fatal != "" {
		t.Errorf("超限 HTML 应跳过检查，得到 fatal=%q", got.Fatal)
	}
	// Go 超 8MB → 跳过
	huge := make([]byte, externalParseSizeLimit+10)
	for i := range huge {
		huge[i] = '@'
	}
	if got := Check("huge.go", string(huge)); got.Fatal != "" {
		t.Errorf("超限 Go 应跳过检查，得到 fatal=%q", got.Fatal)
	}
}

// TestCSSStateMachine —— CSS 状态机的关键边界（字符串/注释/转义）。
func TestCSSStateMachine(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"双引号里的花括号", `.a { content: "}"; }`, false},
		{"单引号里的花括号", `.a { content: '}'; }`, false},
		{"注释里的花括号", `.a { /* } */ }`, false},
		{"注释里未闭合的 {", `.a { /* { */ }`, false},
		{"转义引号后仍有花括号", `.a { content: "\"}"; }`, false},
		{"深度为负立即报错", `}}`, true},
		{"末尾深度为正", `{{`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Check("x.css", tc.content)
			if (got.Fatal != "") != tc.want {
				t.Errorf("fatal=%v，期望 %v\n  消息=%q", got.Fatal != "", tc.want, got.Fatal)
			}
		})
	}
}

// TestHTMLVoidsAndCase —— void 元素与大小写。
func TestHTMLVoidsAndCase(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"void 元素不入栈", `<br><img src="x"><hr>`, false},
		{"自闭合不入栈", `<div/>`, false},
		{"大小写不敏感", `<DIV></div>`, false},
		{"带属性", `<div class="a"><p>x</p></div>`, false},
		{"多余闭合", `</div>`, true},
		{"未闭合", `<div>`, true},
		{"交错不匹配", `<div><span></div></span>`, true},
		{"所有 void 元素", `<area><base><br><col><embed><hr><img><input><link><meta><param><source><track><wbr>`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Check("x.html", tc.content)
			if (got.Fatal != "") != tc.want {
				t.Errorf("fatal=%v，期望 %v\n  消息=%q", got.Fatal != "", tc.want, got.Fatal)
			}
		})
	}
}

// TestHTMLExtAlias —— .htm 与 .html 等价。
func TestHTMLExtAlias(t *testing.T) {
	if got := Check("x.htm", "<div>"); got.Fatal == "" {
		t.Error(".htm 应与 .html 同样检查")
	}
}

// TestCaseInsensitiveExt —— 扩展名大小写不敏感。
func TestCaseInsensitiveExt(t *testing.T) {
	if got := Check("x.CSS", "{{"); got.Fatal == "" {
		t.Error("大写扩展名也应检查")
	}
	if got := Check("x.JSON", "{"); got.Fatal == "" {
		t.Error("大写扩展名也应检查")
	}
	if got := Check("x.GO", "func main() {}"); got.Fatal == "" {
		t.Error("大写扩展名也应检查")
	}
}

// TestFatalAndWarningTogether —— 本实现的契约：fatal 与 warning 同生同灭
// （对账 TS：同一 msg 赋给两者）。调用方只看 fatal 决定回滚。
func TestFatalAndWarningTogether(t *testing.T) {
	got := Check("x.json", "{")
	if got.Fatal == "" {
		t.Fatal("应 fatal")
	}
	if got.Warning != got.Fatal {
		t.Errorf("warning 应与 fatal 相同（对账 TS）：warning=%q fatal=%q", got.Warning, got.Fatal)
	}
}
