package tools

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/compact"
)

// readCapOracleCase 对账 TS 的 cap → 截断链路。
//
// 生成：`node_modules/.bin/tsx go/testdata/readcap/gen_oracle.ts`
type readCapOracleCase struct {
	Label         string `json:"label"`
	ContextWindow int    `json:"contextWindow"`
	Profile       *struct {
		CacheType  string `json:"cacheType"`
		Persistent bool   `json:"persistent"`
	} `json:"profile"`
	Content     string `json:"content"`
	ExpectMax   int    `json:"expectMax"`
	ExpectHead  int    `json:"expectHead"`
	ExpectTail  int    `json:"expectTail"`
	ResultBytes string `json:"resultBytes"`
}

func loadReadCapOracle(t *testing.T) []readCapOracleCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "readcap", "oracle.json"))
	if err != nil {
		t.Fatalf("读 oracle 失败：%v", err)
	}
	var cases []readCapOracleCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	if len(cases) == 0 {
		t.Fatal("oracle 为空——生成脚本可能失败")
	}
	return cases
}

// TestReadCapOracle cap 计算 + 截断输出**双层对账**。
//
// 这是 `read_file` 截断的真实路径：`ComputeModelReadCap` → `TruncateContent`。
// 任一层偏差都会让 resultBytes 不符。
func TestReadCapOracle(t *testing.T) {
	cases := loadReadCapOracle(t)
	for i, c := range cases {
		var prof *compact.CompactRatioProfile
		if c.Profile != nil {
			prof = &compact.CompactRatioProfile{
				CacheType:  compact.CacheType(c.Profile.CacheType),
				Persistent: c.Profile.Persistent,
			}
		}
		cap := ComputeModelReadCap(ModelReadCapInput{
			ContextWindow:   c.ContextWindow,
			ProviderProfile: prof,
		})
		if cap.MaxChars != c.ExpectMax || cap.HeadChars != c.ExpectHead || cap.TailChars != c.ExpectTail {
			t.Errorf("用例 %d (%s) cap 不符：\n期望 max=%d head=%d tail=%d\n实得 max=%d head=%d tail=%d",
				i, c.Label, c.ExpectMax, c.ExpectHead, c.ExpectTail,
				cap.MaxChars, cap.HeadChars, cap.TailChars)
			continue
		}
		got := TruncateContent(c.Content, cap.MaxChars, cap.HeadChars, cap.TailChars)
		if gotHex := hex.EncodeToString([]byte(got)); gotHex != c.ResultBytes {
			t.Errorf("用例 %d (%s) **字节**不符：\n期望 %s\n实得 %s",
				i, c.Label, clipHex(c.ResultBytes, 80), clipHex(gotHex, 80))
		}
	}
}

// TestReadCapScalesWithWindow —— cap **随窗口缩放**（不是静态值）。
//
// 这是本刀修复的核心：旧实现硬编码 100_000。1M 窗口应给 120K（硬上限），
// 小窗口应给更小的值。
func TestReadCapScalesWithWindow(t *testing.T) {
	small := ComputeModelReadCap(ModelReadCapInput{ContextWindow: 128000})
	big := ComputeModelReadCap(ModelReadCapInput{ContextWindow: 1000000})
	if small.MaxChars >= big.MaxChars {
		t.Errorf("cap 应随窗口增长：128K→%d, 1M→%d", small.MaxChars, big.MaxChars)
	}
	if big.MaxChars != 120000 {
		t.Errorf("1M 窗口应封顶 120000，实得 %d", big.MaxChars)
	}
	// 未知窗口 → 地板 8000。
	if z := ComputeModelReadCap(ModelReadCapInput{ContextWindow: 0}); z.MaxChars != 8000 {
		t.Errorf("未知窗口应给地板 8000，实得 %d", z.MaxChars)
	}
}

// TestReadCapProviderStrategy —— 提供商策略影响系数。
//
// exact-prefix（cache-preserving）系数 1.3：128K 窗口给 13312 > nil 的 10240。
func TestReadCapProviderStrategy(t *testing.T) {
	base := ComputeModelReadCap(ModelReadCapInput{ContextWindow: 128000})
	cache := ComputeModelReadCap(ModelReadCapInput{
		ContextWindow:   128000,
		ProviderProfile: &compact.CompactRatioProfile{CacheType: compact.CacheExactPrefix, Persistent: true},
	})
	if cache.MaxChars <= base.MaxChars {
		t.Errorf("cache-preserving 应给更大 cap：base=%d, cache=%d", base.MaxChars, cache.MaxChars)
	}
}

// TestReadFileUsesScaledCapNotHardcoded —— **端到端**：read_file 走真实 cap。
//
// 防的是「接线漏了」——cap 算对了但 read_file 没用它。
func TestReadFileUsesScaledCapNotHardcoded(t *testing.T) {
	root := t.TempDir()
	// 造一个超 8000（地板）但远小于 120000 的文件。
	content := make([]byte, 50000)
	for i := range content {
		content[i] = 'x'
	}
	mustWriteFile(t, filepath.Join(root, "big.txt"), string(content))

	// 未知窗口 → cap 8000 → 应截断。
	tool := ReadFile(root, nil)
	r, _ := tool.Execute(t.Context(), call(root, map[string]any{"file_path": "big.txt"}))
	if r.Lossiness == nil {
		t.Fatal("未知窗口下 50000 文本应被截断（cap 8000）")
	}

	// 1M 窗口 → cap 120000 → 不应截断。
	p2 := call(root, map[string]any{"file_path": "big.txt"})
	p2.ContextWindow = 1000000
	r2, _ := tool.Execute(t.Context(), p2)
	if r2.Lossiness != nil {
		t.Errorf("1M 窗口下 50000 文本不应截断（cap 120000）")
	}
}

// TestReadFileTruncationKeepsTail —— **端到端**：read_file 截断保留**尾部**。
//
// 防的是「只保留头部」的实现——TS 的 `truncateContent` 是 head + tail
// （尾部常含总结/错误，丢掉会让模型误判文件结尾）。
//
// 这条补上 oracle 的盲区：oracle 直接测 `TruncateContent`，不经过 read_file，
// 故「read_file 接线时传错参数」不会被它抓到（变异 M31 实证）。
func TestReadFileTruncationKeepsTail(t *testing.T) {
	root := t.TempDir()
	// 造可辨识的头尾标记，长度超地板 cap（8000）。
	var b strings.Builder
	b.WriteString("HEADMARKER_START\n")
	for i := 0; i < 3000; i++ {
		b.WriteString("padding line to exceed the cap value here\n")
	}
	b.WriteString("TAILMARKER_END\n")
	mustWriteFile(t, filepath.Join(root, "big.txt"), b.String())

	tool := ReadFile(root, nil)
	// 未知窗口 → cap 8000 → 必截断。
	r, _ := tool.Execute(t.Context(), call(root, map[string]any{"file_path": "big.txt"}))
	if r.Lossiness == nil {
		t.Fatal("应被截断")
	}
	if !strings.Contains(r.Content, "HEADMARKER_START") {
		t.Error("截断后应保留头部")
	}
	if !strings.Contains(r.Content, "TAILMARKER_END") {
		t.Errorf("截断后应保留**尾部**（head+tail 语义），实得末尾 200 字符：%q",
			tailSnippet(r.Content, 200))
	}
}

func tailSnippet(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func clipHex(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
