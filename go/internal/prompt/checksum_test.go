package prompt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// checksumOracle 是 TS 侧真实 checksum 函数的产出。
// 生成命令：npx tsx go/testdata/checksum/gen-oracle.ts
type checksumOracle struct {
	Checksums map[string]string `json:"checksums"`
	Appended  map[string]string `json:"appended"`
	Verify    map[string]struct {
		Line   string `json:"line"`
		Result struct {
			Valid    bool   `json:"valid"`
			JSON     string `json:"json"`
			IsLegacy bool   `json:"isLegacy"`
			Error    string `json:"error"`
		} `json:"result"`
	} `json:"verifyResults"`
	VerifyLines map[string]struct {
		Lines  []string `json:"lines"`
		Result struct {
			ValidLines   []string `json:"validLines"`
			InvalidCount int      `json:"invalidCount"`
			LegacyCount  int      `json:"legacyCount"`
		} `json:"result"`
	} `json:"verifyLinesResults"`
}

func loadChecksumOracle(t *testing.T) checksumOracle {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "checksum", "oracle.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败（%s）：%v\n生成命令：npx tsx go/testdata/checksum/gen-oracle.ts", path, err)
	}
	var o checksumOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return o
}

// TestComputeLineChecksumParity —— 行校验和与 TS 逐字节相同。
func TestComputeLineChecksumParity(t *testing.T) {
	o := loadChecksumOracle(t)
	if len(o.Checksums) == 0 {
		t.Fatal("oracle 无 checksums")
	}
	for line, want := range o.Checksums {
		t.Run(line, func(t *testing.T) {
			if got := ComputeLineChecksum(line); got != want {
				t.Errorf("ComputeLineChecksum(%q)：Go=%s TS=%s", line, got, want)
			}
		})
	}
}

// TestAppendChecksumParity —— 追加校验和后的行与 TS 逐字节相同。
func TestAppendChecksumParity(t *testing.T) {
	o := loadChecksumOracle(t)
	for line, want := range o.Appended {
		t.Run(line, func(t *testing.T) {
			if got := AppendChecksum(line); got != want {
				t.Errorf("AppendChecksum(%q)：\n  Go=%q\n  TS=%q", line, got, want)
			}
		})
	}
}

// TestVerifyAndExtractParity —— 验证与提取与 TS 逐字节相同。
//
// 覆盖新格式（合法/不匹配）、legacy 三条判定、空行、边界。
func TestVerifyAndExtractParity(t *testing.T) {
	o := loadChecksumOracle(t)
	if len(o.Verify) == 0 {
		t.Fatal("oracle 无 verify 用例")
	}
	for name, c := range o.Verify {
		t.Run(name, func(t *testing.T) {
			got := VerifyAndExtract(c.Line)
			w := c.Result
			if got.Valid != w.Valid {
				t.Errorf("Valid：Go=%v TS=%v（line=%q）", got.Valid, w.Valid, c.Line)
			}
			if got.JSON != w.JSON {
				t.Errorf("JSON：Go=%q TS=%q", got.JSON, w.JSON)
			}
			if got.IsLegacy != w.IsLegacy {
				t.Errorf("IsLegacy：Go=%v TS=%v（line=%q）", got.IsLegacy, w.IsLegacy, c.Line)
			}
			if got.Error != w.Error {
				t.Errorf("Error：\n  Go=%q\n  TS=%q", got.Error, w.Error)
			}
		})
	}
}

// TestVerifyLinesParity —— 批量验证与 TS 一致。
func TestVerifyLinesParity(t *testing.T) {
	o := loadChecksumOracle(t)
	if len(o.VerifyLines) == 0 {
		t.Fatal("oracle 无 verifyLines 用例")
	}
	for name, c := range o.VerifyLines {
		t.Run(name, func(t *testing.T) {
			got := VerifyLines(c.Lines)
			w := c.Result
			if got.InvalidCount != w.InvalidCount {
				t.Errorf("InvalidCount：Go=%d TS=%d", got.InvalidCount, w.InvalidCount)
			}
			if got.LegacyCount != w.LegacyCount {
				t.Errorf("LegacyCount：Go=%d TS=%d", got.LegacyCount, w.LegacyCount)
			}
			if len(got.ValidLines) != len(w.ValidLines) {
				t.Fatalf("ValidLines 数：Go=%d TS=%d", len(got.ValidLines), len(w.ValidLines))
			}
			for i := range got.ValidLines {
				if got.ValidLines[i] != w.ValidLines[i] {
					t.Errorf("ValidLines[%d]：Go=%q TS=%q", i, got.ValidLines[i], w.ValidLines[i])
				}
			}
		})
	}
}

// TestVerifyAndExtractLegacyRules —— legacy 三条判定（核心兼容逻辑）。
func TestVerifyAndExtractLegacyRules(t *testing.T) {
	validJSON := `{"role":"user"}`
	cs := ComputeLineChecksum(validJSON)

	// 规则 1：无 `|` → legacy
	r := VerifyAndExtract(validJSON)
	if !r.Valid || !r.IsLegacy || r.JSON != validJSON {
		t.Errorf("无分隔符应判 legacy，实际 %+v", r)
	}

	// 规则 2：`|` 后非 16 位小写 hex → legacy
	r2 := VerifyAndExtract(validJSON + "|abc")
	if !r2.Valid || !r2.IsLegacy {
		t.Errorf("校验和格式错应判 legacy，实际 %+v", r2)
	}
	// 大写 hex 也不匹配（正则只认小写）
	r2b := VerifyAndExtract(validJSON + "|" + "ABCDEF0123456789")
	if !r2b.Valid || !r2b.IsLegacy {
		t.Errorf("大写校验和应判 legacy，实际 %+v", r2b)
	}

	// 规则 3：`|` 前非 JSON → legacy
	r3 := VerifyAndExtract("not json | " + cs)
	if !r3.Valid || !r3.IsLegacy {
		t.Errorf("jsonPart 非 JSON 应判 legacy，实际 %+v", r3)
	}

	// 正常新格式：非 legacy
	r4 := VerifyAndExtract(validJSON + "|" + cs)
	if !r4.Valid || r4.IsLegacy || r4.JSON != validJSON {
		t.Errorf("合法新格式不应判 legacy，实际 %+v", r4)
	}
}

// TestVerifyAndExtractLastPipe —— 用 lastIndexOf 取最后一个 `|`。
//
// 故含多个 `|` 的行会以最后一个为分隔（前面的 `|` 留在 jsonPart 里）。
func TestVerifyAndExtractLastPipe(t *testing.T) {
	// `{"a":"x|y"}|{valid checksum of {"a":"x|y"}}` —— 正常
	jsonPart := `{"a":"x|y"}`
	line := jsonPart + "|" + ComputeLineChecksum(jsonPart)
	r := VerifyAndExtract(line)
	if !r.Valid || r.IsLegacy || r.JSON != jsonPart {
		t.Errorf("含 | 的合法 JSON 应正确提取，实际 %+v", r)
	}

	// 额外 `|` 会被并入 jsonPart，导致非 JSON → legacy
	line2 := `{"a":"x"}|extra|` + ComputeLineChecksum(`{"a":"x"}`)
	r2 := VerifyAndExtract(line2)
	if !r2.IsLegacy {
		t.Errorf("多余 | 应导致 jsonPart 非 JSON 而判 legacy，实际 %+v", r2)
	}
}

// TestVerifyAndExtractEmptyAndWhitespace —— 空行与纯空白均 invalid。
func TestVerifyAndExtractEmptyAndWhitespace(t *testing.T) {
	for _, line := range []string{"", "   ", "\t", "\n", "  \r\n  "} {
		r := VerifyAndExtract(line)
		if r.Valid {
			t.Errorf("空/空白行应 invalid，line=%q 实际 %+v", line, r)
		}
		if r.Error != "Empty line" {
			t.Errorf("错误消息应为 \"Empty line\"，实际 %q", r.Error)
		}
	}
}
