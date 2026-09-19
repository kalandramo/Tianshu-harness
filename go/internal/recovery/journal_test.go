package recovery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// recoveryOracle 是 TS recovery-journal 的真实产出（ts 已 scrub）。
// 生成：npx tsx go/testdata/recovery/gen-oracle.ts
type recoveryOracle struct {
	BasicWrite struct {
		Lines     []string   `json:"lines"`
		KeyOrders [][]string `json:"keyOrders"`
	} `json:"basicWrite"`
	TSFormat struct {
		Samples []string `json:"samples"`
		Regex   string   `json:"regex"`
	} `json:"tsFormat"`
	ReadScope struct {
		All      []string `json:"all"`
		OnlyMine []string `json:"onlyMine"`
		OnlyThem []string `json:"onlyThem"`
	} `json:"readScope"`
	AfterAckMine struct {
		All      []string   `json:"all"`
		OnlyMine []string   `json:"onlyMine"`
		RawKeys  [][]string `json:"rawKeys"`
		Raw      []string   `json:"raw"`
	} `json:"afterAckMine"`
	AfterHandoffThem struct {
		OnlyThem []string   `json:"onlyThem"`
		RawKeys  [][]string `json:"rawKeys"`
		Raw      []string   `json:"raw"`
	} `json:"afterHandoffThem"`
	EmptyJournal struct {
		Entries []any `json:"entries"`
	} `json:"emptyJournal"`
	CorruptTolerance struct {
		Files []string `json:"files"`
	} `json:"corruptTolerance"`
}

func loadRecoveryOracle(t *testing.T) recoveryOracle {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "recovery", "oracle.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败（%s）：%v\n生成：npx tsx go/testdata/recovery/gen-oracle.ts", path, err)
	}
	var o recoveryOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return o
}

// scrubTS 把实际输出的 ts 值替换为占位符（与 oracle 同款处理）。
func scrubTS(lines []string) []string {
	re := regexp.MustCompile(`"ts":"[^"]*"`)
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = re.ReplaceAllString(l, `"ts":"<TS>"`)
	}
	return out
}

func journalLines(t *testing.T, cwd string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(cwd, ".rivet", "recovery-journal.jsonl"))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
}

// TestJournalBasicWriteParity —— 逐字节对账 journal 行的**键序与字段**。
func TestJournalBasicWriteParity(t *testing.T) {
	o := loadRecoveryOracle(t)
	cwd := t.TempDir()

	if err := RecordRecovery(cwd, RecoveryEntry{File: "a.ts", Action: "edit", LinesLost: 5}, ""); err != nil {
		t.Fatalf("RecordRecovery 失败：%v", err)
	}
	if err := RecordRecovery(cwd, RecoveryEntry{File: "b.ts", Action: "git checkout HEAD", LinesLost: 22}, "sess-1"); err != nil {
		t.Fatalf("RecordRecovery 失败：%v", err)
	}

	got := scrubTS(journalLines(t, cwd))
	if len(got) != len(o.BasicWrite.Lines) {
		t.Fatalf("行数不符：Go=%d TS=%d\n  Go=%v", len(got), len(o.BasicWrite.Lines), got)
	}
	for i, want := range o.BasicWrite.Lines {
		if got[i] != want {
			t.Errorf("[%d] 行字节不符：\n  Go=%s\n  TS=%s", i, got[i], want)
		}
	}
}

// TestJournalKeyOrderParity —— 键序（含 sessionId 有无两种形态）。
func TestJournalKeyOrderParity(t *testing.T) {
	o := loadRecoveryOracle(t)
	cwd := t.TempDir()

	_ = RecordRecovery(cwd, RecoveryEntry{File: "a.ts", Action: "edit", LinesLost: 5}, "")
	_ = RecordRecovery(cwd, RecoveryEntry{File: "b.ts", Action: "git checkout HEAD", LinesLost: 22}, "sess-1")

	lines := journalLines(t, cwd)
	if len(lines) != len(o.BasicWrite.KeyOrders) {
		t.Fatalf("行数不符：Go=%d TS=%d", len(lines), len(o.BasicWrite.KeyOrders))
	}
	for i, wantKeys := range o.BasicWrite.KeyOrders {
		var m map[string]any
		if err := json.Unmarshal([]byte(lines[i]), &m); err != nil {
			t.Fatalf("[%d] 解析失败：%v", i, err)
		}
		// 从原始行提取键序（保序解析）
		gotKeys := extractKeyOrder(lines[i])
		if strings.Join(gotKeys, ",") != strings.Join(wantKeys, ",") {
			t.Errorf("[%d] 键序不符：Go=%v TS=%v", i, gotKeys, wantKeys)
		}
	}
}

// TestJournalTSFormat —— ts 必须是**恰好 3 位毫秒**的 ISO 8601。
//
// Go 的 RFC3339Nano 会省略尾随零（860ms → .86），与 JS toISOString() 不同。
func TestJournalTSFormat(t *testing.T) {
	o := loadRecoveryOracle(t)
	re := regexp.MustCompile(o.TSFormat.Regex)

	cwd := t.TempDir()
	for i := 0; i < 5; i++ {
		_ = RecordRecovery(cwd, RecoveryEntry{File: "f.ts", Action: "edit"}, "")
	}
	for i, line := range journalLines(t, cwd) {
		var m map[string]any
		_ = json.Unmarshal([]byte(line), &m)
		ts, _ := m["ts"].(string)
		if !re.MatchString(ts) {
			t.Errorf("[%d] ts 格式不符（应 %s）：%q", i, o.TSFormat.Regex, ts)
		}
	}
}

// TestJournalReadScope —— 会话范围过滤（legacy 无归属条目不匹配任何会话）。
func TestJournalReadScope(t *testing.T) {
	o := loadRecoveryOracle(t)
	cwd := t.TempDir()

	_ = RecordRecovery(cwd, RecoveryEntry{File: "legacy.ts", Action: "edit", LinesLost: 1}, "")
	_ = RecordRecovery(cwd, RecoveryEntry{File: "mine.ts", Action: "edit", LinesLost: 2}, "me")
	_ = RecordRecovery(cwd, RecoveryEntry{File: "other.ts", Action: "edit", LinesLost: 3}, "them")

	got := func(sid string) []string {
		var out []string
		for _, e := range ReadUnacknowledged(cwd, sid) {
			out = append(out, e.File)
		}
		return out
	}
	for _, tc := range []struct {
		name, sid string
		want      []string
	}{
		{"all", "", o.ReadScope.All},
		{"mine", "me", o.ReadScope.OnlyMine},
		{"them", "them", o.ReadScope.OnlyThem},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotList := got(tc.sid)
			if strings.Join(gotList, ",") != strings.Join(tc.want, ",") {
				t.Errorf("会话 %q：Go=%v TS=%v", tc.sid, gotList, tc.want)
			}
		})
	}
}

// TestJournalAcknowledgeScope —— ack 只影响指定会话，且写入 acknowledged 键。
func TestJournalAcknowledgeScope(t *testing.T) {
	o := loadRecoveryOracle(t)
	cwd := t.TempDir()

	_ = RecordRecovery(cwd, RecoveryEntry{File: "legacy.ts", Action: "edit", LinesLost: 1}, "")
	_ = RecordRecovery(cwd, RecoveryEntry{File: "mine.ts", Action: "edit", LinesLost: 2}, "me")
	_ = RecordRecovery(cwd, RecoveryEntry{File: "other.ts", Action: "edit", LinesLost: 3}, "them")

	if err := AcknowledgeAll(cwd, "me"); err != nil {
		t.Fatalf("AcknowledgeAll 失败：%v", err)
	}

	var all []string
	for _, e := range ReadUnacknowledged(cwd, "") {
		all = append(all, e.File)
	}
	if strings.Join(all, ",") != strings.Join(o.AfterAckMine.All, ",") {
		t.Errorf("ack 后全部：Go=%v TS=%v", all, o.AfterAckMine.All)
	}
	var mine []string
	for _, e := range ReadUnacknowledged(cwd, "me") {
		mine = append(mine, e.File)
	}
	if len(mine) != 0 {
		t.Errorf("ack 后自己会话应清空，得到 %v", mine)
	}

	// 行字节与键序（acknowledged 追加在末尾）
	got := scrubTS(journalLines(t, cwd))
	if len(got) != len(o.AfterAckMine.Raw) {
		t.Fatalf("行数不符：Go=%d TS=%d", len(got), len(o.AfterAckMine.Raw))
	}
	for i, want := range o.AfterAckMine.Raw {
		if got[i] != want {
			t.Errorf("[%d] ack 后行不符：\n  Go=%s\n  TS=%s", i, got[i], want)
		}
	}
}

// TestJournalHandoffScope —— handoff 只影响指定会话，写入 handedOff 键。
func TestJournalHandoffScope(t *testing.T) {
	o := loadRecoveryOracle(t)
	cwd := t.TempDir()

	_ = RecordRecovery(cwd, RecoveryEntry{File: "legacy.ts", Action: "edit", LinesLost: 1}, "")
	_ = RecordRecovery(cwd, RecoveryEntry{File: "mine.ts", Action: "edit", LinesLost: 2}, "me")
	_ = RecordRecovery(cwd, RecoveryEntry{File: "other.ts", Action: "edit", LinesLost: 3}, "them")

	if err := AcknowledgeAll(cwd, "me"); err != nil {
		t.Fatal(err)
	}
	if err := HandoffRecoveries(cwd, "them"); err != nil {
		t.Fatalf("HandoffRecoveries 失败：%v", err)
	}

	var them []string
	for _, e := range ReadUnacknowledged(cwd, "them") {
		them = append(them, e.File)
	}
	if len(them) != 0 {
		t.Errorf("handoff 后 them 应清空，得到 %v", them)
	}

	got := scrubTS(journalLines(t, cwd))
	for i, want := range o.AfterHandoffThem.Raw {
		if i >= len(got) {
			t.Fatalf("行数不足：Go=%d TS=%d", len(got), len(o.AfterHandoffThem.Raw))
		}
		if got[i] != want {
			t.Errorf("[%d] handoff 后行不符：\n  Go=%s\n  TS=%s", i, got[i], want)
		}
	}
}

// TestJournalEmptyAndCorrupt —— 空文件与损坏行的容错。
func TestJournalEmptyAndCorrupt(t *testing.T) {
	o := loadRecoveryOracle(t)

	// 空（文件不存在）
	cwd := t.TempDir()
	if got := ReadUnacknowledged(cwd, ""); len(got) != len(o.EmptyJournal.Entries) {
		t.Errorf("空 journal 应返回 %d 条，得到 %d", len(o.EmptyJournal.Entries), len(got))
	}

	// 损坏行跳过
	cwd2 := t.TempDir()
	_ = os.MkdirAll(filepath.Join(cwd2, ".rivet"), 0o755)
	content := `{"file":"ok.ts","action":"edit","ts":"2026-01-01T00:00:00.000Z","linesLost":1}` + "\n" +
		"{not json\n" +
		`{"file":"ok2.ts","action":"edit","ts":"2026-01-01T00:00:01.000Z","linesLost":2}` + "\n"
	if err := os.WriteFile(filepath.Join(cwd2, ".rivet", "recovery-journal.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, e := range ReadUnacknowledged(cwd2, "") {
		files = append(files, e.File)
	}
	if strings.Join(files, ",") != strings.Join(o.CorruptTolerance.Files, ",") {
		t.Errorf("损坏行容错：Go=%v TS=%v", files, o.CorruptTolerance.Files)
	}
}

// TestRenderRecoveryStack —— 渲染形态。
func TestRenderRecoveryStack(t *testing.T) {
	cwd := t.TempDir()
	if got := RenderRecoveryStack(cwd, ""); !strings.Contains(got, "empty") {
		t.Errorf("空栈应提示 empty，得到 %q", got)
	}

	_ = RecordRecovery(cwd, RecoveryEntry{File: "a.ts", Action: "edit", LinesLost: 7}, "s")
	got := RenderRecoveryStack(cwd, "s")
	for _, want := range []string{"Recovery stack (1):", "a.ts", "edit", "7 lines lost"} {
		if !strings.Contains(got, want) {
			t.Errorf("渲染缺 %q：%s", want, got)
		}
	}
}

// extractKeyOrder 从原始 JSON 行提取**保序**的键列表。
func extractKeyOrder(line string) []string {
	var out []string
	depth := 0
	i := 0
	for i < len(line) {
		c := line[i]
		switch c {
		case '{', '[':
			depth++
			i++
		case '}', ']':
			depth--
			i++
		case '"':
			if depth == 1 {
				// 读键
				j := i + 1
				var sb strings.Builder
				for j < len(line) {
					if line[j] == '\\' {
						sb.WriteByte(line[j])
						j++
						if j < len(line) {
							sb.WriteByte(line[j])
							j++
						}
						continue
					}
					if line[j] == '"' {
						break
					}
					sb.WriteByte(line[j])
					j++
				}
				key := sb.String()
				// 键后应跟 ':' 才是键（否则是字符串值）
				k := j + 1
				for k < len(line) && line[k] == ' ' {
					k++
				}
				if k < len(line) && line[k] == ':' {
					out = append(out, key)
				}
				i = j + 1
				continue
			}
			// 跳过字符串值
			j := i + 1
			for j < len(line) {
				if line[j] == '\\' {
					j += 2
					continue
				}
				if line[j] == '"' {
					break
				}
				j++
			}
			i = j + 1
		default:
			i++
		}
	}
	return out
}
