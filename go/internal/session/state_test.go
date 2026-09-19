package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// ssOracle 是 TS SessionStateManager 的真实产出。
// 生成命令：npx tsx go/testdata/sessionstate/gen-oracle.ts
type ssOracle struct {
	Cases map[string]struct {
		Note           string          `json:"note"`
		Rendered       string          `json:"rendered"`
		FileIndexKeys  []string        `json:"fileIndexKeys"`
		ModifiedFlags  map[string]bool `json:"modifiedFlags"`
		DecisionsCount int             `json:"decisionsCount"`
		Verification   []struct {
			Target string `json:"target"`
			Status string `json:"status"`
		} `json:"verification"`
		TaskList []struct {
			ID          string `json:"id"`
			Content     string `json:"content"`
			Status      string `json:"status"`
			TurnCreated int    `json:"turnCreated"`
			TurnUpdated int    `json:"turnUpdated"`
		} `json:"taskList"`
		LastExtract []struct {
			ID          string `json:"id"`
			Content     string `json:"content"`
			Status      string `json:"status"`
			TurnCreated int    `json:"turnCreated"`
			TurnUpdated int    `json:"turnUpdated"`
		} `json:"lastExtract"`
		LastUpdate *bool `json:"lastUpdate"`
	} `json:"cases"`
}

func loadSSOracle(t *testing.T) ssOracle {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "sessionstate", "oracle.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败（%s）：%v\n生成命令：npx tsx go/testdata/sessionstate/gen-oracle.ts", path, err)
	}
	var o ssOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return o
}

// op 是 oracle 用例里的一步操作。
type op struct {
	Op         string `json:"op"`
	Path       string `json:"path"`
	ArtifactID string `json:"artifactId"`
	Decision   string `json:"decision"`
	Reason     string `json:"reason"`
	Turn       int    `json:"turn"`
	Target     string `json:"target"`
	Status     string `json:"status"`
	Fact       string `json:"fact"`
	Evidence   string `json:"evidence"`
	Text       string `json:"text"`
	ID         string `json:"id"`
}

// opsByCase 复刻生成器里的操作序列（golden 里不含 ops，故在此重列）。
var opsByCase = map[string][]op{
	"empty": {},
	"onlyModifiedFiles": {
		{Op: "trackFileModified", Path: "src/a.ts"},
		{Op: "trackFileModified", Path: "src/b.ts"},
	},
	"readOnlyNotRendered": {
		{Op: "trackFileRead", Path: "src/read-only.ts", ArtifactID: "art1"},
		{Op: "trackFileModified", Path: "src/changed.ts"},
	},
	"readThenModified": {
		{Op: "trackFileRead", Path: "src/x.ts", ArtifactID: "art1"},
		{Op: "trackFileModified", Path: "src/x.ts"},
	},
	"decisionsOnly": {
		{Op: "recordDecision", Decision: "用 Go 重写", Reason: "字节等价", Turn: 1},
	},
	"decisionsCappedAt5InRender": mkDecisions(8),
	"failedOnly": {
		{Op: "recordVerification", Target: "go test", Status: "passed"},
		{Op: "recordVerification", Target: "npm test", Status: "failed"},
		{Op: "recordVerification", Target: "lint", Status: "not-run"},
	},
	"verificationDedup": {
		{Op: "recordVerification", Target: "go test", Status: "failed"},
		{Op: "recordVerification", Target: "go test", Status: "passed"},
	},
	"allThree": {
		{Op: "trackFileModified", Path: "src/a.ts"},
		{Op: "recordDecision", Decision: "D1", Reason: "r", Turn: 1},
		{Op: "recordVerification", Target: "T1", Status: "failed"},
	},
	"manyModifiedFiles": mkFiles(15),
	"longDecisionsTriggerTrim": append(
		[]op{{Op: "trackFileModified", Path: "src/short.ts"}},
		mkLongDecisions(8)...,
	),
	"veryLongSingleLine": {
		{Op: "trackFileModified", Path: "src/" + rep("x", 600) + ".ts"},
	},
	"taskListBasic": {
		{Op: "extractTaskList", Text: "- P1: 修复认证 bug\n- T2: 补充测试\n3. S1: 探索方案设计", Turn: 2},
	},
	"taskListStatusMarkers": {
		{Op: "extractTaskList", Text: "- P1: 完成的事情 ✓\n- T2: 卡住的事情 ⊗\n- S1: 正在做的事情 ⏳", Turn: 1},
	},
	"taskListMerge": {
		{Op: "extractTaskList", Text: "- P1: 这是第一个任务的描述 ✓", Turn: 1},
		{Op: "extractTaskList", Text: "- P1: 这是更新后的描述文本\n- T2: 这是新增的任务项", Turn: 5},
	},
	"taskListFilterThreshold": {
		{Op: "extractTaskList", Text: "- P1: 原始任务描述", Turn: 1},
		{Op: "extractTaskList", Text: "- P1: 新描述\n- T2: 新增项", Turn: 5},
	},
	"taskListMergeStatusOverride": {
		{Op: "extractTaskList", Text: "- P1: 这是任务的描述文本", Turn: 1},
		{Op: "extractTaskList", Text: "- P1: 这是任务的描述文本 ✓", Turn: 5},
	},
	"taskListTooShort": {
		{Op: "extractTaskList", Text: "- P1: a\n- T2: 这是有效内容", Turn: 1},
	},
	"taskListEmptyResult": {
		{Op: "extractTaskList", Text: "- P1: 原有的任务项描述", Turn: 1},
		{Op: "extractTaskList", Text: "没有任务编号的普通文本", Turn: 2},
	},
	"taskListUpdateItem": {
		{Op: "extractTaskList", Text: "- P1: 这是任务描述", Turn: 1},
		{Op: "updateTaskListItem", ID: "P1", Status: "completed", Turn: 3},
	},
	"taskListUpdateMissing": {
		{Op: "updateTaskListItem", ID: "ZZ", Status: "completed", Turn: 1},
	},
	"taskListMarkerOrder": {
		{Op: "extractTaskList", Text: "- P1: 完成的事情 ✓ 但被阻塞了", Turn: 1},
	},
	"taskListMarkerOrderWord": {
		{Op: "extractTaskList", Text: "- P1: this task is done and blocked", Turn: 1},
	},
	"taskListBoldFormat": {
		{Op: "extractTaskList", Text: "**P1**: 加粗格式的任务", Turn: 1},
	},
	"taskListContentTruncated160": {
		{Op: "extractTaskList", Text: "- P1: " + rep("x", 200), Turn: 1},
	},
}

func mkDecisions(n int) []op {
	out := make([]op, n)
	for i := 0; i < n; i++ {
		out[i] = op{Op: "recordDecision", Decision: "决策" + itoa(i), Reason: "r" + itoa(i), Turn: i}
	}
	return out
}

func mkLongDecisions(n int) []op {
	out := make([]op, n)
	for i := 0; i < n; i++ {
		out[i] = op{Op: "recordDecision", Decision: rep("很长的决策描述", 8) + itoa(i), Reason: "r", Turn: i}
	}
	return out
}

func mkFiles(n int) []op {
	out := make([]op, n)
	for i := 0; i < n; i++ {
		out[i] = op{Op: "trackFileModified", Path: "src/file" + itoa(i) + ".ts"}
	}
	return out
}

func rep(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

// runOps 执行一个用例的操作序列，返回 (lastExtract, lastUpdate)。
func runOps(m *Manager, ops []op) ([]TaskListItem, *bool) {
	var lastExtract []TaskListItem
	var lastUpdate *bool
	for _, o := range ops {
		switch o.Op {
		case "trackFileRead":
			m.TrackFileRead(o.Path, o.ArtifactID)
		case "trackFileModified":
			m.TrackFileModified(o.Path)
		case "recordDecision":
			m.RecordDecision(o.Decision, o.Reason, o.Turn)
		case "recordVerification":
			m.RecordVerification(o.Target, o.Status)
		case "recordFact":
			m.RecordFact(o.Fact, o.Evidence)
		case "extractTaskList":
			lastExtract = m.ExtractTaskList(o.Text, o.Turn)
		case "updateTaskListItem":
			r := m.UpdateTaskListItem(o.ID, o.Status, o.Turn)
			lastUpdate = &r
		}
	}
	return lastExtract, lastUpdate
}

func cmpTaskList(t *testing.T, label string, got []TaskListItem, want []struct {
	ID          string `json:"id"`
	Content     string `json:"content"`
	Status      string `json:"status"`
	TurnCreated int    `json:"turnCreated"`
	TurnUpdated int    `json:"turnUpdated"`
}) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s 长度不符：Go=%d TS=%d\n  Go =%+v\n  TS =%+v", label, len(got), len(want), got, want)
		return
	}
	for i := range got {
		if got[i].ID != want[i].ID || got[i].Content != want[i].Content ||
			got[i].Status != want[i].Status || got[i].TurnCreated != want[i].TurnCreated ||
			got[i].TurnUpdated != want[i].TurnUpdated {
			t.Errorf("%s[%d] 不符\n  Go =%+v\n  TS =%+v", label, i, got[i], want[i])
		}
	}
}

// TestSessionStateRenderedParity —— renderForVolatile 逐字节对账。
func TestSessionStateRenderedParity(t *testing.T) {
	o := loadSSOracle(t)
	checked := 0
	for name, c := range o.Cases {
		ops, ok := opsByCase[name]
		if !ok {
			t.Errorf("[%s] 缺操作序列", name)
			continue
		}
		checked++
		t.Run(name, func(t *testing.T) {
			m := New("oracle-session")
			runOps(m, ops)
			if got := m.RenderForVolatile(); got != c.Rendered {
				t.Errorf("渲染不符\n  Go =%q\n  TS =%q", got, c.Rendered)
			}
		})
	}
	if checked == 0 {
		t.Fatal("oracle 无用例")
	}
	t.Logf("对账了 %d 个用例的渲染", checked)
}

// TestSessionStateTaskListParity —— taskList 与 lastExtract 对账。
func TestSessionStateTaskListParity(t *testing.T) {
	o := loadSSOracle(t)
	for name, c := range o.Cases {
		ops, ok := opsByCase[name]
		if !ok {
			continue
		}
		t.Run(name, func(t *testing.T) {
			m := New("oracle-session")
			lastExtract, lastUpdate := runOps(m, ops)
			cmpTaskList(t, "taskList", m.TaskList(), c.TaskList)
			if c.LastExtract != nil {
				cmpTaskList(t, "lastExtract", lastExtract, c.LastExtract)
			}
			if c.LastUpdate != nil {
				if lastUpdate == nil {
					t.Fatal("TS 有 lastUpdate，Go 为 nil")
				}
				if *lastUpdate != *c.LastUpdate {
					t.Errorf("lastUpdate 不符：Go=%v TS=%v", *lastUpdate, *c.LastUpdate)
				}
			}
		})
	}
}

// TestSessionStateVerificationParity —— verification 替换语义对账。
func TestSessionStateVerificationParity(t *testing.T) {
	o := loadSSOracle(t)
	for name, c := range o.Cases {
		ops, ok := opsByCase[name]
		if !ok {
			continue
		}
		t.Run(name, func(t *testing.T) {
			m := New("oracle-session")
			runOps(m, ops)
			snap := m.Snapshot()
			if len(snap.Verification) != len(c.Verification) {
				t.Errorf("verification 长度不符：Go=%d TS=%d", len(snap.Verification), len(c.Verification))
				return
			}
			for i := range snap.Verification {
				if snap.Verification[i].Target != c.Verification[i].Target ||
					snap.Verification[i].Status != c.Verification[i].Status {
					t.Errorf("verification[%d] 不符", i)
				}
			}
			if len(snap.Decisions) != c.DecisionsCount {
				t.Errorf("decisions 数量不符：Go=%d TS=%d", len(snap.Decisions), c.DecisionsCount)
			}
		})
	}
}

// TestRenderEmptyReturnsEmptyString —— 空状态返回空串，不是空壳。
func TestRenderEmptyReturnsEmptyString(t *testing.T) {
	m := New("s")
	if got := m.RenderForVolatile(); got != "" {
		t.Errorf("空状态应返回空串，得到 %q", got)
	}
	// 只有 read（未改）也不渲染
	m.TrackFileRead("src/a.ts", "art")
	if got := m.RenderForVolatile(); got != "" {
		t.Errorf("只读不渲染，得到 %q", got)
	}
}

// TestRenderUnderMaxChars —— 渲染结果不超 500 字符。
func TestRenderUnderMaxChars(t *testing.T) {
	m := New("s")
	m.TrackFileModified("src/" + rep("x", 600) + ".ts")
	got := m.RenderForVolatile()
	if utf16Len(got) > volatileMaxChars {
		t.Errorf("渲染 %d 字符，超出上限 %d", utf16Len(got), volatileMaxChars)
	}
	if !containsSub(got, "</session-state>") {
		t.Errorf("截断后仍应有收尾标签：%q", got)
	}
}

// TestRenderOnlyFailedVerification —— 只有 failed 渲染。
func TestRenderOnlyFailedVerification(t *testing.T) {
	m := New("s")
	m.RecordVerification("a", "passed")
	m.RecordVerification("b", "not-run")
	if got := m.RenderForVolatile(); got != "" {
		t.Errorf("passed/not-run 不应渲染，得到 %q", got)
	}
	m.RecordVerification("c", "failed")
	if got := m.RenderForVolatile(); !containsSub(got, "Failed: c") {
		t.Errorf("failed 应渲染：%q", got)
	}
}

// TestRenderDecisionsOnlyLast5 —— 渲染只取最后 5 条。
func TestRenderDecisionsOnlyLast5(t *testing.T) {
	m := New("s")
	for i := 0; i < 8; i++ {
		m.RecordDecision("D"+itoa(i), "r", i)
	}
	got := m.RenderForVolatile()
	if containsSub(got, "D0") || containsSub(got, "D2") {
		t.Errorf("应只渲染最后 5 条（D3..D7）：%q", got)
	}
	if !containsSub(got, "D7") {
		t.Errorf("应含 D7：%q", got)
	}
}

// TestExtractTaskListFilterThreshold —— 中文三字被过滤（UTF-16 语义）。
func TestExtractTaskListFilterThreshold(t *testing.T) {
	m := New("s")
	got := m.ExtractTaskList("- P1: 新描述", 1)
	if len(got) != 0 {
		t.Errorf("中文三字应被过滤（length==3 不满足 >3），得到 %+v", got)
	}
	// 四字通过
	got2 := m.ExtractTaskList("- P1: 新描述x", 1)
	if len(got2) != 1 {
		t.Errorf("四字应通过，得到 %+v", got2)
	}
}

// TestExtractTaskListMerge —— 合并语义：保留旧 status/turnCreated。
func TestExtractTaskListMerge(t *testing.T) {
	m := New("s")
	m.ExtractTaskList("- P1: 这是第一个任务的描述 ✓", 1)
	got := m.ExtractTaskList("- P1: 这是更新后的描述文本\n- T2: 这是新增的任务项", 5)
	if len(got) != 2 {
		t.Fatalf("应有 2 项，得到 %+v", got)
	}
	if got[0].Status != "completed" {
		t.Errorf("P1 应保留 completed（第二轮无显式标记），得到 %q", got[0].Status)
	}
	if got[0].TurnCreated != 1 {
		t.Errorf("P1 的 turnCreated 应保留 1，得到 %d", got[0].TurnCreated)
	}
	if got[0].TurnUpdated != 5 {
		t.Errorf("P1 的 turnUpdated 应更新为 5，得到 %d", got[0].TurnUpdated)
	}
	if got[0].Content != "这是更新后的描述文本" {
		t.Errorf("P1 的 content 应更新，得到 %q", got[0].Content)
	}
	if got[1].ID != "T2" || got[1].Status != "pending" || got[1].TurnCreated != 5 {
		t.Errorf("T2 应追加且 pending，得到 %+v", got[1])
	}
}

// TestExtractTaskListEmptyReturnsCopy —— 提取不到时返回现有拷贝（不清空）。
func TestExtractTaskListEmptyReturnsCopy(t *testing.T) {
	m := New("s")
	m.ExtractTaskList("- P1: 原有的任务项描述", 1)
	got := m.ExtractTaskList("没有任务编号的普通文本", 2)
	if len(got) != 1 || got[0].ID != "P1" {
		t.Errorf("应返回现有列表，得到 %+v", got)
	}
}

// TestExtractTaskListContentTruncated160 —— content 截断到 160。
func TestExtractTaskListContentTruncated160(t *testing.T) {
	m := New("s")
	got := m.ExtractTaskList("- P1: "+rep("x", 200), 1)
	if len(got) != 1 {
		t.Fatalf("应有 1 项，得到 %+v", got)
	}
	if utf16Len(got[0].Content) != 160 {
		t.Errorf("content 应为 160 字符，得到 %d", utf16Len(got[0].Content))
	}
}

// TestExtractTaskListStatusMarkers —— 状态标记留存在 content 里（不剥离）。
func TestExtractTaskListStatusMarkers(t *testing.T) {
	m := New("s")
	got := m.ExtractTaskList("- P1: 完成的事情 ✓\n- T2: 卡住的事情 ⊗\n- S1: 正在做的事情 ⏳", 1)
	if len(got) != 3 {
		t.Fatalf("应有 3 项，得到 %+v", got)
	}
	if got[0].Status != "completed" || got[1].Status != "blocked" || got[2].Status != "in_progress" {
		t.Errorf("状态不符：%q %q %q", got[0].Status, got[1].Status, got[2].Status)
	}
	if !containsSub(got[0].Content, "✓") {
		t.Errorf("标记应留存在 content 里：%q", got[0].Content)
	}
}

// TestUpdateTaskListItemMissing —— 不存在的 id 返回 false。
func TestUpdateTaskListItemMissing(t *testing.T) {
	m := New("s")
	if m.UpdateTaskListItem("ZZ", "completed", 1) {
		t.Error("不存在的 id 应返回 false")
	}
}

// TestUtf16Semantics —— UTF-16 计长（emoji 计 2）。
func TestUtf16Semantics(t *testing.T) {
	if got := utf16Len("😀"); got != 2 {
		t.Errorf("emoji 的 UTF-16 长度应为 2，得到 %d", got)
	}
	if got := utf16Len("中文"); got != 2 {
		t.Errorf("中文两字的 UTF-16 长度应为 2，得到 %d", got)
	}
	// 过滤阈值：emoji 三连 = 6 units > 3，应通过
	m := New("s")
	got := m.ExtractTaskList("- P1: 😀😀😀", 1)
	if len(got) != 1 {
		t.Errorf("emoji 三连（6 units）应通过过滤，得到 %+v", got)
	}
}

func containsSub(h, n string) bool {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return true
		}
	}
	return false
}
