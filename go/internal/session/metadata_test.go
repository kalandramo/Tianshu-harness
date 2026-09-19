package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// metadataOracle 是 TS SessionMetadataStore 的真实产出。
// 生成命令：npx tsx go/testdata/metadata/gen-oracle.ts
type metadataOracle struct {
	Cases map[string]struct {
		Note         string           `json:"note"`
		Preexisting  *string          `json:"preexisting"`
		Observations []map[string]any `json:"observations"`
	} `json:"cases"`
}

func loadMetadataOracle(t *testing.T) metadataOracle {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "metadata", "oracle.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败（%s）：%v\n生成命令：npx tsx go/testdata/metadata/gen-oracle.ts", path, err)
	}
	var o metadataOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return o
}

// rawAfterReadRaw 取用例里最后一次 readRaw 的 raw 文本。
// rawAfterReadRaw 取用例里最后一次 readRaw 的 raw 文本；无 readRaw 时返回
// (nil, false) —— 调用方据此跳过（不是所有用例都有落盘步骤）。
func rawAfterReadRaw(obs []map[string]any) (*string, bool) {
	for i := len(obs) - 1; i >= 0; i-- {
		if obs[i]["after"] == "readRaw" {
			if r, ok := obs[i]["raw"].(string); ok {
				return &r, true
			}
			return nil, true // 有 readRaw 但文件不存在
		}
	}
	return nil, false
}

// TestIndentJSONMatchesStringify —— 缩进格式逐字节对账 JSON.stringify(x,null,2)。
//
// **这是本模块风险最高的部分**：手写的缩进器必须与 V8 的实现在所有形态上一致。
func TestIndentJSONMatchesStringify(t *testing.T) {
	cases := []struct{ compact, want string }{
		{`{"a":1}`, "{\n  \"a\": 1\n}"},
		{`{"a":1,"b":2}`, "{\n  \"a\": 1,\n  \"b\": 2\n}"},
		{`{"a":{"b":1}}`, "{\n  \"a\": {\n    \"b\": 1\n  }\n}"},
		{`{"a":[1,2]}`, "{\n  \"a\": [\n    1,\n    2\n  ]\n}"},
		{`{"a":[],"b":{}}`, "{\n  \"a\": [],\n  \"b\": {}\n}"},
		{`{"a":"x,y:z"}`, "{\n  \"a\": \"x,y:z\"\n}"},
		{`{}`, "{}"},
		{`[]`, "[]"},
		{`[1]`, "[\n  1\n]"},
		// 字符串里的转义引号不应干扰状态机
		{`{"a":"say \"hi\""}`, "{\n  \"a\": \"say \\\"hi\\\"\"\n}"},
		// 字符串里的反斜杠
		{`{"a":"a\\b"}`, "{\n  \"a\": \"a\\\\b\"\n}"},
		// 字符串里的花括号
		{`{"a":"{x}"}`, "{\n  \"a\": \"{x}\"\n}"},
	}
	for _, c := range cases {
		if got := indentJSON(c.compact, 2); got != c.want {
			t.Errorf("indentJSON(%s)\n  Go =%q\n  期望 =%q", c.compact, got, c.want)
		}
	}
}

// TestMetadataWriteFormatParity —— write 的落盘格式对账 oracle。
func TestMetadataWriteFormatParity(t *testing.T) {
	o := loadMetadataOracle(t)

	// writeFormat 用例
	t.Run("writeFormat", func(t *testing.T) {
		c, ok := o.Cases["writeFormat"]
		if !ok {
			t.Skip("oracle 缺该用例")
		}
		dir := t.TempDir()
		path := filepath.Join(dir, "s.meta.json")
		s := NewMetadataStore(path)
		if err := s.Write(&SessionMetadata{
			SessionID: "s1", CreatedAt: 1, UpdatedAt: 2, CompactEvents: []any{},
		}); err != nil {
			t.Fatalf("write 失败：%v", err)
		}
		want, _ := rawAfterReadRaw(c.Observations)
		got, _ := os.ReadFile(path)
		if want == nil {
			t.Fatal("oracle 无 raw")
		}
		if string(got) != *want {
			t.Errorf("格式不符\n  Go =%q\n  TS =%q", string(got), *want)
		}
	})

	t.Run("writeWithOptionals", func(t *testing.T) {
		c, ok := o.Cases["writeWithOptionals"]
		if !ok {
			t.Skip("oracle 缺该用例")
		}
		dir := t.TempDir()
		path := filepath.Join(dir, "s.meta.json")
		s := NewMetadataStore(path)
		if err := s.Write(&SessionMetadata{
			SessionID: "s1", CreatedAt: 1, UpdatedAt: 2, CompactEvents: []any{},
			Status: "active", TurnCount: 0, ToolCallCount: 0,
			TokenUsage: &TokenUsage{Prompt: 1, Completion: 2, Total: 3},
			Model:      "deepseek-v4", Provider: "deepseek", Title: "T",
			// 复刻生产路径 initMetadata 的键序（src/agent/session-persist.ts:519-529）
			PresentKeys: []string{
				"sessionId", "createdAt", "updatedAt", "compactEvents",
				"status", "turnCount", "toolCallCount", "tokenUsage",
				"model", "provider", "title",
			},
		}); err != nil {
			t.Fatalf("write 失败：%v", err)
		}
		want, _ := rawAfterReadRaw(c.Observations)
		got, _ := os.ReadFile(path)
		if want == nil {
			t.Fatal("oracle 无 raw")
		}
		if string(got) != *want {
			t.Errorf("格式不符\n  Go =%q\n  TS =%q", string(got), *want)
		}
	})
}

// TestMetadataLoadThreeStates —— load 的三态缓存。
func TestMetadataLoadThreeStates(t *testing.T) {
	// 磁盘无文件 → nil
	dir := t.TempDir()
	path := filepath.Join(dir, "s.meta.json")
	s := NewMetadataStore(path)
	if got := s.Load(); got != nil {
		t.Errorf("无文件应返回 nil，得到 %+v", got)
	}
	// 二次 load 仍 nil（缓存）
	if got := s.Load(); got != nil {
		t.Error("二次 load 应缓存 nil")
	}

	// 损坏 JSON → nil（不抛）
	dir2 := t.TempDir()
	path2 := filepath.Join(dir2, "s.meta.json")
	if err := os.WriteFile(path2, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	s2 := NewMetadataStore(path2)
	if got := s2.Load(); got != nil {
		t.Errorf("损坏 JSON 应返回 nil，得到 %+v", got)
	}

	// 正常文件 → 对象
	dir3 := t.TempDir()
	path3 := filepath.Join(dir3, "s.meta.json")
	body := `{"sessionId":"s1","createdAt":100,"updatedAt":200,"compactEvents":[]}`
	if err := os.WriteFile(path3, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	s3 := NewMetadataStore(path3)
	got := s3.Load()
	if got == nil || got.SessionID != "s1" || got.CreatedAt != 100 {
		t.Errorf("应读出元数据，得到 %+v", got)
	}
}

// TestMetadataUpdateSessionIDAuthoritative —— sessionId 由调用方权威。
func TestMetadataUpdateSessionIDAuthoritative(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.meta.json")
	body := `{"sessionId":"OLD","createdAt":100,"updatedAt":100,"compactEvents":[]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewMetadataStore(path)
	s.Update(&SessionMetadata{SessionID: "NEW"})
	if err := s.Flush(); err != nil {
		t.Fatalf("flush 失败：%v", err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"sessionId": "NEW"`) {
		t.Errorf("sessionId 应被覆盖为 NEW：%s", raw)
	}
	// createdAt 保留
	if !strings.Contains(string(raw), `"createdAt": 100`) {
		t.Errorf("createdAt 应保留 100：%s", raw)
	}
}

// TestMetadataTokenUsageNestedMerge —— tokenUsage 嵌套合并。
func TestMetadataTokenUsageNestedMerge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.meta.json")
	body := `{"sessionId":"s1","createdAt":100,"updatedAt":100,"compactEvents":[],"tokenUsage":{"prompt":10,"completion":20,"total":30}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewMetadataStore(path)
	// patch 只给 total → prompt/completion 应保留
	s.Update(&SessionMetadata{SessionID: "s1", TokenUsage: &TokenUsage{Total: 99}})
	if err := s.Flush(); err != nil {
		t.Fatalf("flush 失败：%v", err)
	}
	got := s.Load()
	if got == nil || got.TokenUsage == nil {
		t.Fatal("应有 tokenUsage")
	}
	if got.TokenUsage.Prompt != 10 {
		t.Errorf("prompt 应保留 10，得到 %d", got.TokenUsage.Prompt)
	}
	if got.TokenUsage.Completion != 20 {
		t.Errorf("completion 应保留 20，得到 %d", got.TokenUsage.Completion)
	}
	if got.TokenUsage.Total != 99 {
		t.Errorf("total 应被覆盖为 99，得到 %d", got.TokenUsage.Total)
	}
}

// TestMetadataFlushNotDirtyNoWrite —— 未 dirty 时 flush 不写盘。
func TestMetadataFlushNotDirtyNoWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.meta.json")
	s := NewMetadataStore(path)
	if err := s.Flush(); err != nil {
		t.Fatalf("flush 失败：%v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("未 dirty 不应建文件")
	}
}

// TestMetadataFlushFailureKeepsDirty —— flush 失败保持 dirty（下次重试）。
//
// **构造要点**：必须先有一次**成功**的 flush 把 dirty 清零，再让下一次失败
// ——否则 `Update` 设的 dirty 本来就在，变异「不重新设 dirty」测不出来。
func TestMetadataFlushFailureKeepsDirty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.meta.json")

	s := NewMetadataStore(path)
	s.Update(&SessionMetadata{SessionID: "s1"})
	if err := s.Flush(); err != nil {
		t.Fatalf("首次 flush 应成功：%v", err)
	}
	if s.dirty {
		t.Fatal("成功后 dirty 应清零")
	}

	// 让后续写入失败：把目标路径换成目录
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}

	s.Update(&SessionMetadata{SessionID: "s2"}) // 重新置 dirty
	if err := s.Flush(); err == nil {
		t.Fatal("写目录应失败")
	}
	if !s.dirty {
		t.Error("失败后应保持 dirty（否则这些更新静默丢失）")
	}
}

// TestMetadataUpdateThenFlushIdempotent —— 连续两次 flush 第二次无操作。
func TestMetadataUpdateThenFlushIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.meta.json")
	s := NewMetadataStore(path)
	s.Update(&SessionMetadata{SessionID: "s1", Model: "m"})
	if err := s.Flush(); err != nil {
		t.Fatalf("首次 flush 失败：%v", err)
	}
	first, _ := os.ReadFile(path)
	if err := s.Flush(); err != nil {
		t.Fatalf("二次 flush 失败：%v", err)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Error("二次 flush 不应改变内容")
	}
}

// TestMetadataOracleParity —— 逐用例对账落盘格式（跳过含时间戳的用例）。
func TestMetadataOracleParity(t *testing.T) {
	o := loadMetadataOracle(t)
	checked := 0
	for name, c := range o.Cases {
		if c.Preexisting == nil {
			continue // 只对账有预置文件的（可复现）
		}
		// 跳过含时间戳的（updatedAt 不可复现）
		hasTS := false
		for _, ob := range c.Observations {
			if r, ok := ob["raw"].(string); ok && strings.Contains(r, `"updatedAt": 178`) {
				hasTS = true
			}
		}
		if hasTS {
			continue
		}
		checked++
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "s.meta.json")
			if err := os.WriteFile(path, []byte(*c.Preexisting), 0o644); err != nil {
				t.Fatal(err)
			}
			s := NewMetadataStore(path)
			// 重放 ops（简化：只处理 load/flush）
			for _, ob := range c.Observations {
				switch ob["after"] {
				case "load":
					s.Load()
				case "flush":
					_ = s.Flush()
				}
			}
			want, hasRaw := rawAfterReadRaw(c.Observations)
			if !hasRaw || want == nil {
				return // 该用例无落盘步骤
			}
			got, _ := os.ReadFile(path)
			if string(got) != *want {
				t.Errorf("格式不符\n  Go =%q\n  TS =%q", string(got), *want)
			}
		})
	}
	t.Logf("对账了 %d 个可复现用例", checked)
}
