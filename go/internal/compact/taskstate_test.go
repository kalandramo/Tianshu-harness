package compact

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/prompt"
)

// taskStateOracle 是 TS 侧三模块（trajectory / todo-deps / task-state）的产出。
// 生成命令：node_modules/.bin/tsx go/testdata/taskstate/gen-oracle.ts
//
// **形态是 {input, output} 数据驱动**——测试直接重放 oracle 的 input，
// 零手工重建。首版只存 output，测试要在 Go 侧重建输入；重建一旦与生成
// 脚本不一致就是**假绿**（测的是重建的输入，不是 oracle 的）。
type taskStateOracle struct {
	Trajectory map[string]struct {
		Input struct {
			MaxEntries *int              `json:"maxEntries"`
			Entries    []trajectoryEntry `json:"entries"`
		} `json:"input"`
		Output struct {
			Entries   []trajectoryEntry `json:"entries"`
			Summarize struct {
				TotalTools    int `json:"totalTools"`
				Failures      int `json:"failures"`
				Retries       int `json:"retries"`
				AvgDurationMs int `json:"avgDurationMs"`
			} `json:"summarize"`
			ExportJSON string `json:"exportJson"`
			// AfterReset 仅 after_reset 用例存在。
			AfterReset *struct {
				Entries   []trajectoryEntry `json:"entries"`
				Summarize struct {
					TotalTools    int `json:"totalTools"`
					Failures      int `json:"failures"`
					Retries       int `json:"retries"`
					AvgDurationMs int `json:"avgDurationMs"`
				} `json:"summarize"`
				ExportJSON string `json:"exportJson"`
			} `json:"afterReset"`
		} `json:"output"`
	} `json:"trajectory"`

	Deps map[string]struct {
		Input struct {
			Todos []todoItem `json:"todos"`
		} `json:"input"`
		Output struct {
			Deps           []prompt.TodoDep `json:"deps"`
			MaxDepth       json.RawMessage  `json:"maxDepth"`
			FindExecutable []string         `json:"findExecutable"`
			OrderPending   []string         `json:"orderPending"`
		} `json:"output"`
	} `json:"deps"`

	TaskState map[string]struct {
		Input struct {
			Entries []trajectoryEntry `json:"entries"`
			Text    string            `json:"text"`
		} `json:"input"`
		Output taskStateJSON `json:"output"`
	} `json:"taskState"`

	FromTodos map[string]struct {
		Input struct {
			Todos     []todoItem `json:"todos"`
			Decisions []string   `json:"decisions"`
		} `json:"input"`
		Output taskStateJSON `json:"output"`
	} `json:"fromTodos"`
}

// trajectoryEntry 是 oracle 里的轨迹条目（camelCase JSON）。
type trajectoryEntry struct {
	Turn          int    `json:"turn"`
	Tool          string `json:"tool"`
	Target        string `json:"target"`
	DurationMs    int    `json:"durationMs"`
	Status        string `json:"status"`
	InputSummary  string `json:"inputSummary"`
	ResultSummary string `json:"resultSummary"`
	ErrorClass    string `json:"errorClass"`
}

func (e trajectoryEntry) toGo() TrajectoryEntry {
	return TrajectoryEntry{
		Turn:          e.Turn,
		Tool:          e.Tool,
		Target:        e.Target,
		DurationMs:    e.DurationMs,
		Status:        TrajectoryStatus(e.Status),
		InputSummary:  e.InputSummary,
		ResultSummary: e.ResultSummary,
		ErrorClass:    e.ErrorClass,
	}
}

// todoItem 是 oracle 里的 todo 条目。
type todoItem struct {
	ID         string `json:"id"`
	Content    string `json:"content"`
	Status     string `json:"status"`
	ActiveForm string `json:"activeForm"`
}

func (t todoItem) toGo() prompt.TodoItem {
	return prompt.TodoItem{ID: t.ID, Content: t.Content, Status: t.Status, ActiveForm: t.ActiveForm}
}

// taskStateJSON 是 oracle 里的 TaskState（camelCase）。
type taskStateJSON struct {
	Completed []string `json:"completed"`
	Current   string   `json:"current"`
	Remaining []string `json:"remaining"`
	Decisions []string `json:"decisions"`
}

func loadTaskStateOracle(t *testing.T) taskStateOracle {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "taskstate", "oracle.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败（%s）：%v\n生成命令：node_modules/.bin/tsx go/testdata/taskstate/gen-oracle.ts",
			path, err)
	}
	var o taskStateOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	if len(o.Trajectory) == 0 || len(o.Deps) == 0 || len(o.TaskState) == 0 || len(o.FromTodos) == 0 {
		t.Fatal("oracle 为空或结构不符——重新生成")
	}
	return o
}

// ─────────────────────────────────────────────────────────────
// 1. TrajectoryRecorder
// ─────────────────────────────────────────────────────────────

func TestTrajectoryRecorderParity(t *testing.T) {
	o := loadTaskStateOracle(t)
	for name, c := range o.Trajectory {
		t.Run(name, func(t *testing.T) {
			maxEntries := 0
			if c.Input.MaxEntries != nil {
				maxEntries = *c.Input.MaxEntries
			}
			// **关键**：零值构造必须与 TS 的「无参数构造」等价。
			// TS 无参数 → DEFAULT_MAX_ENTRIES；Go 传 0 → 也应得默认值。
			rec := NewTrajectoryRecorder(maxEntries)
			for _, e := range c.Input.Entries {
				rec.Record(e.toGo())
			}

			got := rec.Entries()
			if len(got) != len(c.Output.Entries) {
				t.Fatalf("条目数：期望 %d，实得 %d\n期望 %+v\n实得 %+v",
					len(c.Output.Entries), len(got), c.Output.Entries, got)
			}
			for i := range got {
				if got[i] != c.Output.Entries[i].toGo() {
					t.Errorf("条目[%d]：期望 %+v，实得 %+v", i, c.Output.Entries[i].toGo(), got[i])
				}
			}

			s := rec.Summarize()
			want := c.Output.Summarize
			if s.TotalTools != want.TotalTools || s.Failures != want.Failures ||
				s.Retries != want.Retries || s.AvgDurationMs != want.AvgDurationMs {
				t.Errorf("summarize：期望 %+v，实得 %+v", want, s)
			}

			// exportJson 逐字对账（含键序与 omitempty 行为）。
			gotJSON, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("序列化失败：%v", err)
			}
			if string(gotJSON) != c.Output.ExportJSON {
				t.Errorf("exportJson 字节不等价：\n期望 %s\n实得 %s", c.Output.ExportJSON, gotJSON)
			}

			if c.Output.AfterReset != nil {
				rec.Reset()
				if n := len(rec.Entries()); n != 0 {
					t.Errorf("reset 后应无条目，实得 %d", n)
				}
				rs := rec.Summarize()
				w := c.Output.AfterReset.Summarize
				if rs.TotalTools != w.TotalTools || rs.Failures != w.Failures ||
					rs.Retries != w.Retries || rs.AvgDurationMs != w.AvgDurationMs {
					t.Errorf("reset 后 summarize：期望 %+v，实得 %+v", w, rs)
				}
			}
		})
	}
}

// TestTrajectoryEntriesIsCopy —— Entries 必须返回副本（改它不能影响内部）。
func TestTrajectoryEntriesIsCopy(t *testing.T) {
	rec := NewTrajectoryRecorder(10)
	rec.Record(TrajectoryEntry{Tool: "a", Status: TrajectorySuccess})
	got := rec.Entries()
	got[0].Tool = "mutated"
	if rec.Entries()[0].Tool != "a" {
		t.Error("Entries 返回了内部切片——调用方可篡改记录器状态")
	}
}

// TestTrajectoryDefaultMaxEntries —— 零值构造不得静默失效。
func TestTrajectoryDefaultMaxEntries(t *testing.T) {
	rec := NewTrajectoryRecorder(0)
	for i := 0; i < 10; i++ {
		rec.Record(TrajectoryEntry{Tool: "t", Status: TrajectorySuccess})
	}
	if n := len(rec.Entries()); n != 10 {
		t.Errorf("零值构造应使用默认上限（200），实得 %d 条（说明 maxEntries=0 在清空）", n)
	}
}

// ─────────────────────────────────────────────────────────────
// 2. todo-deps
// ─────────────────────────────────────────────────────────────

func TestTodoDepsParity(t *testing.T) {
	o := loadTaskStateOracle(t)
	for name, c := range o.Deps {
		t.Run(name, func(t *testing.T) {
			todos := make([]prompt.TodoItem, len(c.Input.Todos))
			for i, ti := range c.Input.Todos {
				todos[i] = ti.toGo()
			}
			got := prompt.DetectDependencies(todos)
			want := c.Output.Deps

			if len(got) != len(want) {
				t.Fatalf("依赖边数：期望 %d，实得 %d", len(want), len(got))
			}
			for i := range got {
				if got[i].ID != want[i].ID {
					t.Errorf("边[%d].id：期望 %q，实得 %q", i, want[i].ID, got[i].ID)
				}
				if len(got[i].DependsOn) != len(want[i].DependsOn) {
					t.Errorf("边[%d] (%s).dependsOn：期望 %v，实得 %v", i, got[i].ID, want[i].DependsOn, got[i].DependsOn)
					continue
				}
				for j := range got[i].DependsOn {
					if got[i].DependsOn[j] != want[i].DependsOn[j] {
						t.Errorf("边[%d].dependsOn[%d]：期望 %q，实得 %q", i, j, want[i].DependsOn[j], got[i].DependsOn[j])
					}
				}
			}

			// maxDepth：oracle 用 "Infinity" 字符串表示环。
			gotDepth := prompt.ComputeMaxDepth(got)
			var wantDepth int
			if string(c.Output.MaxDepth) == `"Infinity"` {
				wantDepth = -1
			} else {
				if err := json.Unmarshal(c.Output.MaxDepth, &wantDepth); err != nil {
					t.Fatalf("解析 maxDepth 失败：%v", err)
				}
			}
			if gotDepth != wantDepth {
				t.Errorf("maxDepth：期望 %d（oracle %s），实得 %d", wantDepth, c.Output.MaxDepth, gotDepth)
			}

			gotExec := ids(prompt.FindExecutable(todos, got))
			assertStrings(t, "findExecutable", gotExec, c.Output.FindExecutable)

			gotOrder := ids(prompt.OrderPendingByExecutability(todos, got))
			assertStrings(t, "orderPending", gotOrder, c.Output.OrderPending)
		})
	}
}

func ids(items []prompt.TodoItem) []string {
	out := make([]string, len(items))
	for i, t := range items {
		out[i] = t.ID
	}
	return out
}

func assertStrings(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s：期望 %v，实得 %v", label, want, got)
		return
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("%s[%d]：期望 %q，实得 %q（完整 期望 %v 实得 %v）", label, i, want[i], got[i], want, got)
		}
	}
}

// TestOrderPendingNeverDropsItems —— **核心不变量**：只重排，不丢项。
//
// 这是 taskStateFromTodos 用它的理由——伪依赖不得让真实工作从提示词消失。
func TestOrderPendingNeverDropsItems(t *testing.T) {
	todos := []prompt.TodoItem{
		{ID: "X", Content: "基于 T99 的伪依赖", Status: "pending"},
		{ID: "Y", Content: "普通任务", Status: "pending"},
		{ID: "Z", Content: "另一普通任务", Status: "pending"},
	}
	deps := prompt.DetectDependencies(todos)
	got := prompt.OrderPendingByExecutability(todos, deps)
	if len(got) != 3 {
		t.Fatalf("不变量破坏：pending 项从 3 变 %d——有项被丢弃", len(got))
	}
}

// ─────────────────────────────────────────────────────────────
// 3. task-state
// ─────────────────────────────────────────────────────────────

func TestExtractTaskStateParity(t *testing.T) {
	o := loadTaskStateOracle(t)
	for name, c := range o.TaskState {
		t.Run(name, func(t *testing.T) {
			entries := make([]TrajectoryEntry, len(c.Input.Entries))
			for i, e := range c.Input.Entries {
				entries[i] = e.toGo()
			}
			got := ExtractTaskState(entries, c.Input.Text)
			assertTaskState(t, got, c.Output)
		})
	}
}

func TestTaskStateFromTodosParity(t *testing.T) {
	o := loadTaskStateOracle(t)
	for name, c := range o.FromTodos {
		t.Run(name, func(t *testing.T) {
			todos := make([]prompt.TodoItem, len(c.Input.Todos))
			for i, ti := range c.Input.Todos {
				todos[i] = ti.toGo()
			}
			got := TaskStateFromTodos(todos, c.Input.Decisions)
			assertTaskState(t, got, c.Output)
		})
	}
}

func assertTaskState(t *testing.T, got TaskState, want taskStateJSON) {
	t.Helper()
	assertStrings(t, "completed", got.Completed, want.Completed)
	if got.Current != want.Current {
		t.Errorf("current：期望 %q，实得 %q", want.Current, got.Current)
	}
	assertStrings(t, "remaining", got.Remaining, want.Remaining)
	assertStrings(t, "decisions", got.Decisions, want.Decisions)
}

// TestExtractTaskStateEmptyEntriesBranch —— 空轨迹的 early-return 分支。
//
// 这条单独写（不只靠 oracle）：该分支返回的 current 是字面量 "starting"，
// **不是零值 ""**——最容易被"简化"成零值。
func TestExtractTaskStateEmptyEntriesBranch(t *testing.T) {
	got := ExtractTaskState(nil, "some text")
	if got.Current != "starting" {
		t.Errorf("空轨迹 current 应为字面量 %q，实得 %q", "starting", got.Current)
	}
	if got.Completed == nil || got.Remaining == nil || got.Decisions == nil {
		t.Error("空轨迹应返回空切片（非 nil）——nil 序列化成 null 而非 []")
	}
}

// TestUtf16SliceCJK —— 截断按 UTF-16 code unit，不是字节。
//
// TS 的 slice(0,60) 对中文截 60 个**字符**；若按字节切只有 20 个。
func TestUtf16SliceCJK(t *testing.T) {
	s := "很" + "很" + "很"
	if got := utf16Slice(s, 2); got != "很很" {
		t.Errorf("utf16Slice 应按码点截断：期望 %q，实得 %q", "很很", got)
	}
	if got := utf16Slice(s, 0); got != "" {
		t.Errorf("n=0 应返回空串，实得 %q", got)
	}
	if got := utf16Slice(s, 100); got != s {
		t.Errorf("n 超长应返回全串，实得 %q", got)
	}
}

// TestBasenameSplitsOnForwardSlashOnly —— 对账 TS 的 split('/')。
//
// **不得**用 filepath.Base：Windows 反斜杠路径在 TS 侧整串保留。
func TestBasenameSplitsOnForwardSlashOnly(t *testing.T) {
	cases := map[string]string{
		"src/a.ts":  "a.ts",
		"a/b/c.ts":  "c.ts",
		"ls":        "ls",
		`a\b\c.ts`:  `a\b\c.ts`, // 反斜杠不分割（TS 行为）
		"trailing/": "",
	}
	for in, want := range cases {
		if got := basename(in); got != want {
			t.Errorf("basename(%q)：期望 %q，实得 %q", in, want, got)
		}
	}
}
