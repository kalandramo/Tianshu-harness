package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/prompt"
)

// hasheditOracle 是 TS HASH_EDIT_TOOL.execute 的真实产出。
// 生成命令：npx tsx go/testdata/hashedit/gen-oracle.ts
type hasheditOracle struct {
	HashLineSamples map[string]string `json:"hashLineSamples"`
	Cases           map[string]struct {
		Note           string   `json:"note"`
		Initial        *string  `json:"initial"`
		ExpandedAnchor []string `json:"expandedAnchors"`
		Result         struct {
			Content string `json:"content"`
			IsError bool   `json:"isError"`
		} `json:"result"`
		FinalContent *string `json:"finalContent"`
	} `json:"cases"`
}

func loadHasheditOracle(t *testing.T) hasheditOracle {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "hashedit", "oracle.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败（%s）：%v\n生成命令：npx tsx go/testdata/hashedit/gen-oracle.ts", path, err)
	}
	var o hasheditOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return o
}

// TestHashLineParityWithOracle —— HashLine 与 TS hashLine 逐字节一致。
func TestHashLineParityWithOracle(t *testing.T) {
	o := loadHasheditOracle(t)
	for input, want := range o.HashLineSamples {
		if got := prompt.HashLine(input); got != want {
			t.Errorf("HashLine(%q) = %q，TS 为 %q", input, got, want)
		}
	}
}

// TestParseAnchorParity —— 锚点解析与 TS 一致。
//
// 注意：oracle 里有两个用例的锚点是**故意非法**的（badAnchorFormat 的 "XYZ"、
// anchorZero 的 "L0:aaaaaaaa"）——TS 侧同样解析失败并走错误分支。它们必须
// 解析为 nil，否则说明 Go 侧正则过宽。
func TestParseAnchorParity(t *testing.T) {
	o := loadHasheditOracle(t)
	// 这些用例的锚点按设计就是非法的
	illegalCases := map[string]bool{"badAnchorFormat": true, "anchorZero": true}

	seen := 0
	for name, c := range o.Cases {
		for _, raw := range c.ExpandedAnchor {
			seen++
			a := ParseAnchor(raw)
			if illegalCases[name] {
				if a != nil {
					t.Errorf("[%s] ParseAnchor(%q) 应返回 nil（该用例锚点故意非法），得到 %+v", name, raw, a)
				}
				continue
			}
			if a == nil {
				t.Errorf("[%s] ParseAnchor(%q) 返回 nil，但 TS 侧解析成功", name, raw)
				continue
			}
			if a.Line < 1 {
				t.Errorf("[%s] ParseAnchor(%q).Line = %d，应 >= 1", name, raw, a.Line)
			}
		}
	}
	if seen == 0 {
		t.Fatal("oracle 无锚点样本")
	}
	t.Logf("对账了 %d 个锚点样本", seen)
}

// TestParseAnchorFormats —— 各格式与边界。
func TestParseAnchorFormats(t *testing.T) {
	cases := []struct {
		raw      string
		wantNil  bool
		wantLine int
		wantHash string // 空 = nil
	}{
		{"L5:a1b2c3d4", false, 5, "a1b2c3d4"},
		{"L5", false, 5, ""},
		{"L1:00000000", false, 1, "00000000"},
		{"L2:deadbeef → some content", false, 2, "deadbeef"}, // 容忍内容后缀
		{"L2 ", false, 2, ""},                                // 位置 + 空格
		{"L0:aaaaaaaa", true, 0, ""},                         // 行号 0 非法
		{"L0", true, 0, ""},
		{"XYZ", true, 0, ""},
		{"l5:a1b2c3d4", true, 0, ""},  // 大写 L 非法
		{"L5:A1B2C3D4", true, 0, ""},  // 大写 hex 非法
		{"L5:a1b2c3d", true, 0, ""},   // 7 位 hex 非法
		{"L5:a1b2c3d4e", true, 0, ""}, // 9 位 hex → 完整格式失败，位置格式也失败（: 后非空白）
		{"", true, 0, ""},
	}
	for _, c := range cases {
		got := ParseAnchor(c.raw)
		if c.wantNil {
			if got != nil {
				t.Errorf("ParseAnchor(%q) 应为 nil，得到 %+v", c.raw, got)
			}
			continue
		}
		if got == nil {
			t.Errorf("ParseAnchor(%q) 不应为 nil", c.raw)
			continue
		}
		if got.Line != c.wantLine {
			t.Errorf("ParseAnchor(%q).Line = %d，期望 %d", c.raw, got.Line, c.wantLine)
		}
		if c.wantHash == "" {
			if got.Hash != nil {
				t.Errorf("ParseAnchor(%q).Hash 应为 nil，得到 %q", c.raw, *got.Hash)
			}
		} else if got.Hash == nil || *got.Hash != c.wantHash {
			t.Errorf("ParseAnchor(%q).Hash 不符", c.raw)
		}
	}
}

// TestRecoverStaleAnchorsShift —— 行号整体漂移时按漂移量恢复。
func TestRecoverStaleAnchorsShift(t *testing.T) {
	// 原文件 a/b/c；现在前面插入了 2 行 → b 从 L2 漂到 L4
	lines := strings.Split("new1\nnew2\na\nb\nc", "\n")
	h := prompt.HashLine("b")
	anchors := []Anchor{{Line: 2, Hash: &h}}

	got := RecoverStaleAnchors(anchors, lines)
	if got == nil {
		t.Fatal("应能恢复到 L4")
	}
	if got[0].Line != 4 {
		t.Errorf("应恢复到 L4，得到 L%d", got[0].Line)
	}
}

// TestRecoverStaleAnchorsNilWhenNotFound —— 内容不存在时返回 nil。
func TestRecoverStaleAnchorsNilWhenNotFound(t *testing.T) {
	lines := strings.Split("a\nb\nc", "\n")
	h := prompt.HashLine("不存在的行")
	anchors := []Anchor{{Line: 2, Hash: &h}}
	if got := RecoverStaleAnchors(anchors, lines); got != nil {
		t.Errorf("找不到内容应返回 nil，得到 %+v", got)
	}
}

// TestRecoverStaleAnchorsWindowBoundary —— 200 行窗口的边界（跨越上界）。
func TestRecoverStaleAnchorsWindowBoundary(t *testing.T) {
	// 目标行在 250 处，锚点声明在 50 处 → 距离 200，**恰好在窗口内**
	var sb strings.Builder
	for i := 1; i <= 300; i++ {
		if i == 250 {
			sb.WriteString("TARGET\n")
		} else {
			sb.WriteString("filler" + itoa(i) + "\n")
		}
	}
	lines := strings.Split(strings.TrimSuffix(sb.String(), "\n"), "\n")
	h := prompt.HashLine("TARGET")

	// 距离恰好 200 → 应找到
	if got := RecoverStaleAnchors([]Anchor{{Line: 50, Hash: &h}}, lines); got == nil {
		t.Error("距离 200（恰在窗口内）应能恢复")
	} else if got[0].Line != 250 {
		t.Errorf("应恢复到 L250，得到 L%d", got[0].Line)
	}

	// 距离 201 → 应找不到
	if got := RecoverStaleAnchors([]Anchor{{Line: 49, Hash: &h}}, lines); got != nil {
		t.Errorf("距离 201（超出窗口）不应恢复，得到 %+v", got)
	}
}

// TestRecoverStaleAnchorsOrder —— 恢复后必须保持严格升序。
func TestRecoverStaleAnchorsOrder(t *testing.T) {
	lines := strings.Split("a\nb\nc\nd", "\n")
	hb, hd := prompt.HashLine("b"), prompt.HashLine("d")
	// 声明顺序 L2(b) 与 L4(d)——恢复后仍是 2 < 4，应通过
	if got := RecoverStaleAnchors([]Anchor{{Line: 2, Hash: &hb}, {Line: 4, Hash: &hd}}, lines); got == nil {
		t.Error("升序锚点应能恢复")
	}
	// 声明顺序反了（L4 在前，L2 在后）→ 恢复后仍非升序 → nil
	if got := RecoverStaleAnchors([]Anchor{{Line: 4, Hash: &hd}, {Line: 2, Hash: &hb}}, lines); got != nil {
		t.Errorf("非升序锚点恢复后应返回 nil，得到 %+v", got)
	}
}

// TestFormatStaleDiagnosticParity —— 诊断文本与 TS 逐字节一致。
//
// 这是本模块最有价值的对账：诊断文本是模型唯一的重试线索，
// 少了「可重试锚点」模型就会反复用死锚点重试。
func TestFormatStaleDiagnosticParity(t *testing.T) {
	o := loadHasheditOracle(t)

	// 只对账走诊断路径的用例（content 以 "hash_edit 在" 开头）
	checked := 0
	for name, c := range o.Cases {
		if !strings.HasPrefix(c.Result.Content, "hash_edit 在 ") {
			continue
		}
		checked++
		// oracle 内容里的 <DIR> 是临时目录占位符；Go 侧用同一占位符重建
		want := c.Result.Content

		// 重建该用例的锚点与行内容
		if c.Initial == nil {
			t.Errorf("[%s] 诊断用例缺 initial", name)
			continue
		}
		lines := strings.Split(strings.TrimSuffix(*c.Initial, "\n"), "\n")
		anchors := make([]Anchor, 0, len(c.ExpandedAnchor))
		for _, raw := range c.ExpandedAnchor {
			a := ParseAnchor(raw)
			if a == nil {
				t.Fatalf("[%s] 锚点 %q 解析失败", name, raw)
			}
			anchors = append(anchors, *a)
		}

		// 构造 mismatches（与 TS 的校验逻辑一致）
		var mismatches []AnchorMismatch
		for _, a := range anchors {
			if a.Line > len(lines) {
				mismatches = append(mismatches, AnchorMismatch{
					Anchor: a, ActualHash: "<eof>", ActualLine: "<行号超出文件长度>",
				})
				continue
			}
			if a.Hash != nil {
				actual := prompt.HashLine(lines[a.Line-1])
				if actual != *a.Hash {
					mismatches = append(mismatches, AnchorMismatch{
						Anchor: a, ActualHash: actual, ActualLine: lines[a.Line-1],
					})
				}
			}
		}
		if len(mismatches) == 0 {
			t.Errorf("[%s] 诊断用例却无失配", name)
			continue
		}

		got := FormatStaleDiagnostic("<DIR>/target.txt", anchors, lines, mismatches)
		if got != want {
			t.Errorf("[%s] 诊断文本不等价\n  Go =%q\n  TS =%q", name, got, want)
		}
	}
	if checked == 0 {
		t.Fatal("oracle 无诊断路径用例")
	}
	t.Logf("对账了 %d 个诊断用例", checked)
}

// TestFormatStaleDiagnosticEOFNoRetry —— 越界失配不给可重试锚点。
func TestFormatStaleDiagnosticEOFNoRetry(t *testing.T) {
	lines := strings.Split("a\nb", "\n")
	a := Anchor{Line: 99}
	ms := []AnchorMismatch{{Anchor: a, ActualHash: "<eof>", ActualLine: "<行号超出文件长度>"}}
	got := FormatStaleDiagnostic("f.txt", []Anchor{a}, lines, ms)
	if strings.Contains(got, "请立即用以下锚点重试") {
		t.Errorf("eof 失配不应给重试锚点：%s", got)
	}
	if !strings.Contains(got, "锚点行号超出当前文件长度") {
		t.Errorf("应给越界提示：%s", got)
	}
}

// TestFormatStaleDiagnosticPositionOnlyNull —— 仅位置锚点渲染为字面量 null。
func TestFormatStaleDiagnosticPositionOnlyNull(t *testing.T) {
	lines := strings.Split("a\nb", "\n")
	a := Anchor{Line: 99} // Hash == nil
	ms := []AnchorMismatch{{Anchor: a, ActualHash: "<eof>", ActualLine: "<行号超出文件长度>"}}
	got := FormatStaleDiagnostic("f.txt", []Anchor{a}, lines, ms)
	if !strings.Contains(got, "L99:null") {
		t.Errorf("仅位置锚点应渲染为字面量 null：%s", got)
	}
	if !strings.Contains(got, "expected null") {
		t.Errorf("evidence 行应含 expected null：%s", got)
	}
}

// ── EOL 策略 ──

func TestDetectEOL(t *testing.T) {
	cases := []struct {
		text string
		want EOL
	}{
		{"a\nb\n", EOLLF},
		{"a\r\nb\r\n", EOLCRLF},
		{"a\r\nb\n", EOLLF},        // 平局 → lf（crlf=1, lf=1）
		{"a\r\nb\r\nc\n", EOLCRLF}, // crlf=2 > lf=1
		{"", ""},
		{"abc", ""}, // 无换行
	}
	for _, c := range cases {
		if got := detectEOL(c.text); got != c.want {
			t.Errorf("detectEOL(%q) = %q，期望 %q", c.text, got, c.want)
		}
	}
}

func TestToLFAndApplyEOL(t *testing.T) {
	// 顺序敏感：先 \r\n 再裸 \r
	if got := toLF("a\r\nb\rc\nd"); got != "a\nb\nc\nd" {
		t.Errorf("toLF = %q", got)
	}
	if got := applyEOL("a\nb", EOLCRLF); got != "a\r\nb" {
		t.Errorf("applyEOL crlf = %q", got)
	}
	if got := applyEOL("a\r\nb", EOLLF); got != "a\nb" {
		t.Errorf("applyEOL lf = %q", got)
	}
}

func TestChooseEOL(t *testing.T) {
	// 扩展名强制 > 既有 > 默认
	if got := chooseEOL("x.bat", EOLLF); got != EOLCRLF {
		t.Errorf(".bat 应强制 CRLF，得到 %q", got)
	}
	if got := chooseEOL("x.cmd", EOLLF); got != EOLCRLF {
		t.Errorf(".cmd 应强制 CRLF，得到 %q", got)
	}
	if got := chooseEOL("x.txt", EOLCRLF); got != EOLCRLF {
		t.Errorf("应保留既有 CRLF，得到 %q", got)
	}
	if got := chooseEOL("x.txt", ""); got != targetEOL() {
		// 默认 EOL 随目标平台（Windows=CRLF，其余=LF）——断言平台真实默认，
		// 不硬编码某一侧（那会让另一个平台上的**正确**行为被误判为缺陷）。
		t.Errorf("无既有信息时应回落到平台默认 %q，得到 %q", targetEOL(), got)
	}
	if got := chooseEOL("X.BAT", EOLLF); got != EOLCRLF {
		t.Errorf("扩展名应大小写不敏感，得到 %q", got)
	}
}
