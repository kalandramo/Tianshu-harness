package session

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

// TestEmitGoFramesForNode —— 产出 Go 帧供 Node 侧验证（双向兼容的另一半）。
// 写文件后由 go/testdata/zstd/verify-go-frames.ts 读取校验。
func TestEmitGoFramesForNode(t *testing.T) {
	tr, err := NewTranscript()
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	defer tr.Close()

	cases := map[string]string{
		"simple":  "hello zstd\n",
		"chinese": "中文内容\n第二行\n",
		"emoji":   "😀 emoji 🎉\n",
		"jsonl":   "{\"role\":\"user\"}|a1b2c3d4e5f60718\n",
		"large":   rep("x", 5000) + "\n",
	}
	out := map[string]string{}
	for k, v := range cases {
		out[k] = hex.EncodeToString(tr.EncodeBatch(v))
	}
	// 拼接
	c1 := tr.EncodeBatch("第一帧\n")
	c2 := tr.EncodeBatch("第二帧\n")
	out["concat"] = hex.EncodeToString(append(append([]byte{}, c1...), c2...))

	raw, _ := json.MarshalIndent(map[string]any{"frames": out, "expect": cases,
		"concatText": "第一帧\n第二帧\n"}, "", "  ")
	p := "/tmp/go-frames.json"
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatalf("写失败：%v", err)
	}
	t.Logf("已写出 %s", p)
}
