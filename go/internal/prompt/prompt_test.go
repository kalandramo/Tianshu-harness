package prompt

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// oracle 是 TS 侧真实 buildSystemPrompt / detectModelFamily 的产出。
// 生成命令：npx tsx go/testdata/prompt/gen-oracle.ts
type oracleShape struct {
	Main         string            `json:"main"`
	Calibrations map[string]string `json:"calibrations"`
	Families     []string          `json:"families"`
	Detect       map[string]string `json:"detect"`
	SHA256       struct {
		Main      string `json:"main"`
		MainBytes string `json:"mainBytes"`
	} `json:"sha256"`
	MainEqualsBasePrompt bool `json:"mainEqualsBasePrompt"`
}

func loadOracle(t *testing.T) oracleShape {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "prompt", "oracle.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败（%s）：%v\n生成命令：npx tsx go/testdata/prompt/gen-oracle.ts", path, err)
	}
	var o oracleShape
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return o
}

// TestMainPromptByteParity —— 主控提示词与 TS 逐字节相同。
// 这是前缀缓存的根基：system prompt 差一个字节，整条前缀缓存失效。
func TestMainPromptByteParity(t *testing.T) {
	o := loadOracle(t)
	got := BuildSystemPrompt(Context{})

	if got != o.Main {
		t.Errorf("主控提示词与 TS 不等价\n  Go 长度=%d  TS 长度=%d\n  Go 首80=%q\n  TS 首80=%q",
			len(got), len(o.Main), trunc(got, 80), trunc(o.Main, 80))
	}
	if len(got) != len(o.Main) {
		t.Errorf("字节长度不符：Go=%d TS=%d", len(got), len(o.Main))
	}
}

// TestMainPromptSHA256 —— 用独立哈希通道二次确认字节等价。
// 长度相同不等于内容相同；哈希是独立的第二判据。
func TestMainPromptSHA256(t *testing.T) {
	o := loadOracle(t)
	got := BuildSystemPrompt(Context{})
	sum := sha256.Sum256([]byte(got))
	gotHex := hex.EncodeToString(sum[:])

	if gotHex != o.SHA256.Main {
		t.Errorf("sha256 不符\n  Go  =%s\n  TS  =%s", gotHex, o.SHA256.Main)
	}
}

// TestMainEqualsBasePrompt —— audience 未设时必须是 BASE_PROMPT 原样（TS 侧不变量）。
func TestMainEqualsBasePrompt(t *testing.T) {
	o := loadOracle(t)
	if !o.MainEqualsBasePrompt {
		t.Fatal("TS 侧不变量破坏：audience 未设时 buildSystemPrompt 不等于 MAIN_BASE_PROMPT")
	}
	if BuildSystemPrompt(Context{}) != o.Main {
		t.Error("Go 侧未复现该不变量")
	}
}

// TestCalibrationRendering —— 各家族渲染结果与 TS 逐字节相同。
func TestCalibrationRendering(t *testing.T) {
	o := loadOracle(t)

	for _, fam := range o.Families {
		t.Run(fam, func(t *testing.T) {
			want, ok := o.Calibrations[fam]
			if !ok {
				t.Fatalf("oracle 缺家族 %s", fam)
			}
			got := BuildSystemPrompt(Context{ModelFamily: fam})
			if got != want {
				t.Errorf("家族 %s 渲染不等价\n  Go 长度=%d TS 长度=%d\n  Go 尾80=%q\n  TS 尾80=%q",
					fam, len(got), len(want), tail(got, 80), tail(want, 80))
			}
		})
	}
}

// TestCalibrationSeparationInvariant —— 有 calibration 的家族 = base + "\n\n" + 片段。
// 这个不变量是 Go 拼接逻辑的假设；破坏它意味着两侧拼接方式分叉。
func TestCalibrationSeparationInvariant(t *testing.T) {
	o := loadOracle(t)
	base := o.Main

	for _, fam := range o.Families {
		rendered := o.Calibrations[fam]
		t.Run(fam, func(t *testing.T) {
			if rendered == base {
				// 该家族无 calibration
				if got := BuildSystemPrompt(Context{ModelFamily: fam}); got != base {
					t.Errorf("家族 %s 在 TS 侧无 calibration，Go 侧却改变了输出", fam)
				}
				return
			}
			sep := "\n\n"
			if len(rendered) <= len(base)+len(sep) {
				t.Fatalf("家族 %s 渲染过短，不满足分隔假设", fam)
			}
			if rendered[:len(base)] != base {
				t.Errorf("家族 %s 渲染结果不是以 base 开头", fam)
			}
			if rendered[len(base):len(base)+len(sep)] != sep {
				t.Errorf("家族 %s 的分隔符不是 %q，实际 %q",
					fam, sep, rendered[len(base):len(base)+len(sep)])
			}
		})
	}
}

// TestDetectModelFamily —— 模型名探测覆盖 oracle 的全部用例。
func TestDetectModelFamily(t *testing.T) {
	o := loadOracle(t)
	if len(o.Detect) == 0 {
		t.Fatal("oracle 未含探测用例")
	}
	for name, want := range o.Detect {
		t.Run(name, func(t *testing.T) {
			if got := DetectModelFamily(name); got != want {
				t.Errorf("DetectModelFamily(%q) = %q，TS 侧为 %q", name, got, want)
			}
		})
	}
}

// TestDetectPriority —— 优先级顺序不得颠倒（deepseek 优先于 openai 等）。
func TestDetectPriority(t *testing.T) {
	// 含 'gpt' 也含 'claude' 的名字：TS 顺序决定 gpt 先命中
	if got := DetectModelFamily("claude-gpt-hybrid"); got != "openai" {
		t.Errorf("优先级错误：claude-gpt-hybrid 应为 openai（gpt 检查先于 claude），实际 %s", got)
	}
}

// TestUnknownFamilyNoCalibration —— 未知家族不得附加任何 calibration。
func TestUnknownFamilyNoCalibration(t *testing.T) {
	o := loadOracle(t)
	base := o.Main
	for _, fam := range []string{"unknown", "openai", "anthropic", ""} {
		if got := BuildSystemPrompt(Context{ModelFamily: fam}); got != base {
			t.Errorf("家族 %q 不应有 calibration，但输出长度 %d != base %d", fam, len(got), len(base))
		}
	}
}

// TestEmbeddedDataIntegrity —— 嵌入数据自身的指纹与 oracle 一致。
// 防止 data/prompt.json 与 oracle 不同步（例如只重跑了部分生成步骤）。
func TestEmbeddedDataIntegrity(t *testing.T) {
	o := loadOracle(t)
	if baseBytes != len(o.Main) {
		t.Errorf("嵌入 base 字节数 %d != oracle %s", baseBytes, o.SHA256.MainBytes)
	}
	if baseSha256 != o.SHA256.Main {
		t.Errorf("嵌入 base sha256 %s != oracle %s", baseSha256, o.SHA256.Main)
	}
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
