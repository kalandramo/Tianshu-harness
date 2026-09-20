package tools

import (
	"bytes"
	"strings"
	"testing"
)

// TestWinStreamDecoderGBK —— GBK 字节解码为可读中文（**核心回归**）。
//
// 字节取自本机实测（`cmd /c "echo 中文测试"`，代码页 936）——不是构造的，
// 是真实命令的原始输出。修复前直读会得到 `\xd6\xd0\xceĲ\xe2\xca\xd4`（乱码）。
func TestWinStreamDecoderGBK(t *testing.T) {
	// `echo 中文测试\r\n` 的 GBK 字节
	raw := []byte{0xd6, 0xd0, 0xce, 0xc4, 0xb2, 0xe2, 0xca, 0xd4, 0x0d, 0x0a}
	got := decodeWindowsOutput(raw, true)
	want := "中文测试\r\n"
	if got != want {
		t.Errorf("GBK 解码失败\n  得到 %q\n  期望 %q", got, want)
	}
}

// TestWinStreamDecoderUTF8Passthrough —— UTF-8 输出不被误判为 GBK。
//
// 字节取自实测（`bash -c "echo 中文测试"` / `node -e "console.log(...)"`）。
// 若首块探测失效（一律当 GBK），这段会解成乱码——故本测试锁住「不误伤」。
func TestWinStreamDecoderUTF8Passthrough(t *testing.T) {
	raw := []byte{0xe4, 0xb8, 0xad, 0xe6, 0x96, 0x87, 0xe6, 0xb5, 0x8b, 0xe8, 0xaf, 0x95, 0x0a}
	got := decodeWindowsOutput(raw, true)
	want := "中文测试\n"
	if got != want {
		t.Errorf("UTF-8 被误判\n  得到 %q\n  期望 %q", got, want)
	}
}

// TestWinStreamDecoderChcp —— 真实场景：`chcp` 输出从乱码变可读。
//
// 修复前：`\xbb\uedaf\xb4\xfa\xc2\xebҳ: 936`（乱码）
// 修复后：`活动代码页: 936`
func TestWinStreamDecoderChcp(t *testing.T) {
	raw := []byte{
		0xbb, 0xee, 0xb6, 0xaf, 0xb4, 0xfa, 0xc2, 0xeb, 0xd2, 0xb3,
		0x3a, 0x20, 0x39, 0x33, 0x36, 0x0d, 0x0a,
	}
	got := decodeWindowsOutput(raw, true)
	if !strings.Contains(got, "活动代码页") || !strings.Contains(got, "936") {
		t.Errorf("chcp 输出应可读，得到 %q", got)
	}
}

// TestWinStreamDecoderNonWindowsPassthrough —— 非 Windows 完全直通。
//
// 对账 TS 的 `if (!isWin) return chunk.toString('utf8')`——不做探测、不改字节。
func TestWinStreamDecoderNonWindowsPassthrough(t *testing.T) {
	// GBK 字节在非 Windows 上应**原样**保留（不擅自当 GBK 解）。
	raw := []byte{0xd6, 0xd0, 0xce, 0xc4}
	got := decodeWindowsOutput(raw, false)
	if got != string(raw) {
		t.Errorf("非 Windows 应直通，得到 %q", got)
	}
}

// TestWinStreamDecoderStreamingAcrossChunks —— 多字节字符跨 chunk 边界不切断。
//
// 这是**流式**解码器的存在理由：GBK 的 2 字节 / UTF-8 的 3 字节若被 chunk
// 边界切开，逐块独立解码会产出乱码。此处把「中文测试」的 GBK 字节按奇偶
// 切成两块喂入，结果必须与整块解码一致。
func TestWinStreamDecoderStreamingAcrossChunks(t *testing.T) {
	raw := []byte{0xd6, 0xd0, 0xce, 0xc4, 0xb2, 0xe2, 0xca, 0xd4}
	d := newWinStreamDecoder(true)

	var sb strings.Builder
	// 逐字节喂——最极端的切分。
	for _, b := range raw {
		sb.WriteString(d.write([]byte{b}))
	}
	sb.WriteString(d.end())

	got := sb.String()
	want := "中文测试"
	if got != want {
		t.Errorf("逐字节流式解码失败\n  得到 %q\n  期望 %q", got, want)
	}
}

// TestWinStreamDecoderStreamingUTF8AcrossChunks —— UTF-8 跨块同理。
func TestWinStreamDecoderStreamingUTF8AcrossChunks(t *testing.T) {
	raw := []byte{0xe4, 0xb8, 0xad, 0xe6, 0x96, 0x87} // 中文
	d := newWinStreamDecoder(true)

	var sb strings.Builder
	for _, b := range raw {
		sb.WriteString(d.write([]byte{b}))
	}
	sb.WriteString(d.end())

	if got := sb.String(); got != "中文" {
		t.Errorf("UTF-8 逐字节流式解码失败，得到 %q", got)
	}
}

// TestLimitedWriterDecodes —— bash 的限流写入器同时做解码。
func TestLimitedWriterDecodes(t *testing.T) {
	var buf bytes.Buffer
	w := &limitedWriter{buf: &buf, limit: 1024, dec: newWinStreamDecoder(true)}
	// GBK「中文测试」+ 换行
	if _, err := w.Write([]byte{0xd6, 0xd0, 0xce, 0xc4, 0xb2, 0xe2, 0xca, 0xd4, 0x0d, 0x0a}); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "中文测试\r\n" {
		t.Errorf("limitedWriter 未解码，得到 %q", got)
	}
	if w.truncated {
		t.Error("未超限不应标记截断")
	}
}

// TestLimitedWriterTruncatesAtRuneBoundary —— 截断不切断 UTF-8 字符。
//
// 限流按字节计，但截断点必须落在字符边界——否则产出半个字符（乱码）。
func TestLimitedWriterTruncatesAtRuneBoundary(t *testing.T) {
	// 「中文」是 6 字节（每字 3 字节）。limit=4 时只能放下第一个字（3 字节），
	// 第二个字被切掉——不能留 1 个残字节。
	utf8Bytes := []byte{0xe4, 0xb8, 0xad, 0xe6, 0x96, 0x87}
	var buf bytes.Buffer
	w := &limitedWriter{buf: &buf, limit: 4, dec: newWinStreamDecoder(false)}
	if _, err := w.Write(utf8Bytes); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if got != "中" {
		t.Errorf("截断应落在字符边界，得到 %q（应为「中」）", got)
	}
	if !w.truncated {
		t.Error("超限应标记截断")
	}
}

// TestTruncateAtRuneBoundary —— 边界函数单测。
func TestTruncateAtRuneBoundary(t *testing.T) {
	s := "中文测试" // 12 字节
	cases := []struct {
		limit int
		want  string
	}{
		{0, ""},
		{1, ""}, // 不足一个字符
		{3, "中"},
		{4, "中"}, // 第 4 字节是下一个字的首字节——应回退
		{6, "中文"},
		{12, "中文测试"},
		{99, "中文测试"},
	}
	for _, c := range cases {
		if got := truncateAtRuneBoundary(s, c.limit); got != c.want {
			t.Errorf("truncateAtRuneBoundary(_, %d) = %q, want %q", c.limit, got, c.want)
		}
	}
}

// TestDecodingWriter —— run_tests 用的无上限解码写入器。
func TestDecodingWriter(t *testing.T) {
	var buf bytes.Buffer
	w := &decodingWriter{buf: &buf, dec: newWinStreamDecoder(true)}
	w.Write([]byte{0xd6, 0xd0})       // 「中」的前半（GBK 2 字节正好一个字）
	w.Write([]byte{0xce, 0xc4, 0x0a}) // 「文」+ 换行
	if got := buf.String(); got != "中文\n" {
		t.Errorf("decodingWriter 解码失败，得到 %q", got)
	}
}
