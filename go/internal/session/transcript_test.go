package session

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/prompt"
)

// TestDecodeNodeFrame —— 解码 Node 生成的 zstd 帧（跨版本读取）。
//
// 帧来自真实 Node `zstdCompressSync(..., {checksumFlag: 1})`，见
// testdata/zstd/frames.json（gen-frames.ts 生成）。
func TestDecodeNodeFrame(t *testing.T) {
	o := loadZstdOracle(t)
	tr, err := NewTranscript()
	if err != nil {
		t.Fatalf("构造 codec 失败：%v", err)
	}
	defer tr.Close()

	checked := 0
	for name, c := range o.Frames {
		checked++
		t.Run(name, func(t *testing.T) {
			raw, err := hex.DecodeString(c.Hex)
			if err != nil {
				t.Fatalf("hex 解码失败：%v", err)
			}
			got, err := tr.DecodeTranscriptText(raw)
			if err != nil {
				t.Fatalf("解码失败：%v", err)
			}
			if got != c.Text {
				t.Errorf("解出的文本不符\n  Go =%q\n  Node =%q", got, c.Text)
			}
		})
	}
	if checked == 0 {
		t.Fatal("oracle 无帧样本")
	}
	t.Logf("跨版本解码了 %d 个 Node 帧", checked)
}

// TestDecodeConcatFrames —— 多帧拼接（Node 生成）。
func TestDecodeConcatFrames(t *testing.T) {
	o := loadZstdOracle(t)
	if o.ConcatHex == "" {
		t.Skip("oracle 无拼接样本")
	}
	raw, err := hex.DecodeString(o.ConcatHex)
	if err != nil {
		t.Fatalf("hex 解码失败：%v", err)
	}
	tr, _ := NewTranscript()
	defer tr.Close()
	got, err := tr.DecodeTranscriptText(raw)
	if err != nil {
		t.Fatalf("解码失败：%v", err)
	}
	if got != o.ConcatText {
		t.Errorf("拼接解码不符\n  Go =%q\n  Node =%q", got, o.ConcatText)
	}
}

// TestDecodeTornTail —— 崩溃截断的末帧被丢弃（不报错）。
func TestDecodeTornTail(t *testing.T) {
	o := loadZstdOracle(t)
	if o.ConcatHex == "" {
		t.Skip("oracle 无拼接样本")
	}
	full, _ := hex.DecodeString(o.ConcatHex)
	// 砍掉最后 5 字节 → 末帧不完整
	torn := full[:len(full)-5]

	tr, _ := NewTranscript()
	defer tr.Close()
	got, err := tr.DecodeTranscriptText(torn)
	if err != nil {
		t.Fatalf("torn tail 不应报错，得到：%v", err)
	}
	// 应只解出第一帧的内容（末帧被丢弃）
	if got != o.FirstFrameText {
		t.Errorf("torn tail 应只解出完整帧\n  Go =%q\n  期望 =%q", got, o.FirstFrameText)
	}

	// TornTailStart 应指向末帧起点
	ts, err := TornTailStart(torn)
	if err != nil {
		t.Fatalf("TornTailStart 报错：%v", err)
	}
	if ts < 0 {
		t.Error("应有 torn tail")
	}
}

// TestDecodeLegacyPlainText —— 传统纯文本直通。
func TestDecodeLegacyPlainText(t *testing.T) {
	tr, _ := NewTranscript()
	defer tr.Close()
	plain := `{"role":"user"}|abc123` + "\n"
	got, err := tr.DecodeTranscriptText([]byte(plain))
	if err != nil {
		t.Fatalf("纯文本直通不应报错：%v", err)
	}
	if got != plain {
		t.Errorf("纯文本应原样返回，得到 %q", got)
	}
}

// TestDecodeEmpty —— 空 buffer 返回空串。
func TestDecodeEmpty(t *testing.T) {
	tr, _ := NewTranscript()
	defer tr.Close()
	got, err := tr.DecodeTranscriptText(nil)
	if err != nil || got != "" {
		t.Errorf("空 buffer 应返回空串，得到 %q err=%v", got, err)
	}
}

// TestDecodeCorruptMagic —— 损坏的魔数报错。
func TestDecodeCorruptMagic(t *testing.T) {
	tr, _ := NewTranscript()
	defer tr.Close()
	// 前 4 字节是 zstd 魔数，但后续结构损坏
	buf := []byte{0x28, 0xb5, 0x2f, 0xfd, 0xff, 0xff, 0xff, 0xff}
	// 保留位非 0（0xff & 0x18 != 0）→ 应报错
	if _, err := tr.DecodeTranscriptText(buf); err == nil {
		t.Error("保留位非 0 应报错")
	}
}

// TestEncodeBatchEmpty —— 空文本产出空 buffer。
func TestEncodeBatchEmpty(t *testing.T) {
	tr, _ := NewTranscript()
	defer tr.Close()
	if got := tr.EncodeBatch(""); got != nil {
		t.Errorf("空文本应产出 nil，得到 %d 字节", len(got))
	}
}

// TestEncodeDecodeRoundTrip —— 自产自解往返。
func TestEncodeDecodeRoundTrip(t *testing.T) {
	tr, _ := NewTranscript()
	defer tr.Close()
	text := "第一行\n第二行\n"
	frame := tr.EncodeBatch(text)
	if len(frame) == 0 {
		t.Fatal("应产出非空帧")
	}
	if !prompt.IsZstdFrameStream(frame) {
		t.Error("产出的应是 zstd 帧流")
	}
	got, err := tr.DecodeTranscriptText(frame)
	if err != nil {
		t.Fatalf("自解失败：%v", err)
	}
	if got != text {
		t.Errorf("往返不符：%q", got)
	}
}

// TestGoFrameIsDecodable —— Go 帧结构可被自己的扫描器识别（含校验位）。
func TestGoFrameIsDecodable(t *testing.T) {
	tr, _ := NewTranscript()
	defer tr.Close()
	frame := tr.EncodeBatch("x")
	scan, err := prompt.ScanZstdFrames(frame)
	if err != nil {
		t.Fatalf("扫描 Go 帧失败：%v", err)
	}
	if len(scan.Frames) != 1 {
		t.Errorf("应有 1 个帧，得到 %d", len(scan.Frames))
	}
	if scan.TornStart != -1 {
		t.Errorf("完整帧不应有 torn tail，得到 %d", scan.TornStart)
	}
	// 校验位应被置上（对账 TS 的 ZSTD_c_checksumFlag: 1）
	if frame[4]&0x04 == 0 {
		t.Error("帧 descriptor 的校验位应被置上")
	}

	// **更精确的断言**：帧尾确有 4 字节校验和，且损坏能被检出。
	//
	// 为什么需要它：klauspost 的 `NewWriter(nil)` **默认就开 CRC**，
	// 故「descriptor 有校验位」无法区分我是否显式开了 CRC（实测两种写法
	// descriptor 都是 0x04）。真正有判别力的是「篡改内容后解压失败」。
	corrupted := append([]byte{}, frame...)
	corrupted[len(corrupted)-1] ^= 0xFF // 翻转校验和最后一字节
	if _, err := tr.DecodeTranscriptText(corrupted); err == nil {
		t.Error("校验和被篡改应解压失败（证明校验和真的生效）")
	}
}

// TestConcatOwnFrames —— 自产多帧拼接解码。
func TestConcatOwnFrames(t *testing.T) {
	tr, _ := NewTranscript()
	defer tr.Close()
	f1 := tr.EncodeBatch("A\n")
	f2 := tr.EncodeBatch("B\n")
	concat := append(append([]byte{}, f1...), f2...)
	got, err := tr.DecodeTranscriptText(concat)
	if err != nil {
		t.Fatalf("拼接自解失败：%v", err)
	}
	if got != "A\nB\n" {
		t.Errorf("拼接解码不符：%q", got)
	}
	// 砍掉末帧一部分 → 只解出 A
	torn := concat[:len(f1)+3]
	got2, err := tr.DecodeTranscriptText(torn)
	if err != nil {
		t.Fatalf("torn 不应报错：%v", err)
	}
	if got2 != "A\n" {
		t.Errorf("torn 应只解出 A，得到 %q", got2)
	}
}

// ── oracle 加载 ──

type zstdOracle struct {
	Frames map[string]struct {
		Text string `json:"text"`
		Hex  string `json:"hex"`
	} `json:"frames"`
	ConcatHex      string `json:"concatHex"`
	ConcatText     string `json:"concatText"`
	FirstFrameText string `json:"firstFrameText"`
}

func loadZstdOracle(t *testing.T) zstdOracle {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "zstd", "frames.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("缺 zstd oracle（%s）——生成命令：npx tsx go/testdata/zstd/gen-frames.ts", path)
	}
	var o zstdOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return o
}

// TestCrossVersionInteropNote —— 记录跨版本兼容的判据（不是同字节，是互解）。
//
// 本测试断言的是**事实**：Go 帧与 Node 帧字节不同但互解。
// 若未来有人试图把两者做逐字节对账，这个测试会提醒他判据错了。
func TestCrossVersionInteropNote(t *testing.T) {
	o := loadZstdOracle(t)
	if len(o.Frames) == 0 {
		t.Skip("无 oracle")
	}
	tr, _ := NewTranscript()
	defer tr.Close()

	// 取一个 Node 帧的明文，用 Go 重新压
	for _, c := range o.Frames {
		if c.Text == "" {
			continue
		}
		nodeRaw, _ := hex.DecodeString(c.Hex)
		goRaw := tr.EncodeBatch(c.Text)
		if bytes.Equal(nodeRaw, goRaw) {
			// 巧合相同也可以，但不作要求
			continue
		}
		// 字节不同 → 必须仍互解（Go 能解 Node 的，Node 能解 Go 的——后者
		// 已由 TS 侧探针验证，此处只验 Go 侧）
		got, err := tr.DecodeTranscriptText(nodeRaw)
		if err != nil || got != c.Text {
			t.Errorf("字节不同的帧仍应可解：err=%v got=%q", err, got)
		}
		return
	}
}
