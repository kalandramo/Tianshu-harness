package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// modeloutput_test.go —— 对账 TS `buildModelOutput` / `extractErrorAwareLines`。
//
// oracle 由 `testdata/modeloutput/gen_oracle.ts` **真跑 TS 原实现**产出
// （两函数皆纯函数，无外部依赖）。生成命令：
//
//	node_modules/.bin/tsx go/testdata/modeloutput/gen_oracle.ts > go/testdata/modeloutput/oracle.json

type moCase struct {
	Name  string `json:"name"`
	Input struct {
		Raw        string  `json:"raw"`
		ExitCode   int     `json:"exitCode"`
		DurationMs int64   `json:"durationMs"`
		Command    string  `json:"command"`
		RawPath    *string `json:"rawPath"`
	} `json:"input"`
	ModelOutput string `json:"modelOutput"`
}

type eaCase struct {
	Name  string `json:"name"`
	Input struct {
		Lines    []string `json:"lines"`
		MaxLines int      `json:"maxLines"`
	} `json:"input"`
	Result []string `json:"result"`
}

func loadOracle(t *testing.T) ([]moCase, []eaCase) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "modeloutput", "oracle.json"))
	if err != nil {
		t.Skipf("oracle 缺失（需先跑 gen_oracle.ts）：%v", err)
	}
	var o struct {
		ModelOutput []moCase `json:"modelOutput"`
		ErrorAware  []eaCase `json:"errorAware"`
	}
	if err := json.Unmarshal(b, &o); err != nil {
		t.Fatalf("oracle 解析失败：%v", err)
	}
	return o.ModelOutput, o.ErrorAware
}

// TestBuildModelOutputOracle —— 逐字节对账 TS 的 13 例。
func TestBuildModelOutputOracle(t *testing.T) {
	cases, _ := loadOracle(t)
	if len(cases) == 0 {
		t.Fatal("oracle 无 modelOutput 用例")
	}
	for _, c := range cases {
		meta := ToolOutputMeta{
			Command:    c.Input.Command,
			ExitCode:   c.Input.ExitCode,
			DurationMs: c.Input.DurationMs,
		}
		if c.Input.RawPath != nil {
			meta.RawPath = *c.Input.RawPath
		}
		got := BuildModelOutput(c.Input.Raw, meta, nil)
		if got != c.ModelOutput {
			t.Errorf("%s 不符\n--- got ---\n%s\n--- want ---\n%s", c.Name, got, c.ModelOutput)
		}
	}
}

// TestExtractErrorAwareOracle —— 逐元素对账 TS 的 6 例。
func TestExtractErrorAwareOracle(t *testing.T) {
	_, cases := loadOracle(t)
	if len(cases) == 0 {
		t.Fatal("oracle 无 errorAware 用例")
	}
	for _, c := range cases {
		got := ExtractErrorAwareLines(c.Input.Lines, c.Input.MaxLines)
		if len(got) != len(c.Result) {
			t.Errorf("%s 长度不符：got %d want %d\ngot=%q", c.Name, len(got), len(c.Result), got)
			continue
		}
		for i := range got {
			if got[i] != c.Result[i] {
				t.Errorf("%s 第 %d 行不符：got %q want %q", c.Name, i, got[i], c.Result[i])
			}
		}
	}
}

// TestBuildModelOutputSuccessKeepsTail —— **本刀修的真实偏差**：
// 成功折叠保留**末尾 20 行**（不是单行提示）。
func TestBuildModelOutputSuccessKeepsTail(t *testing.T) {
	// 每行唯一（避免内容重复让"头部是否保留"的断言失真）。
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = "LINE" + itoa(i+1) // LINE1..LINE100
	}
	raw := strings.Join(lines, "\n")
	got := BuildModelOutput(raw, ToolOutputMeta{Command: "x", ExitCode: 0, DurationMs: 10}, nil)

	if !strings.Contains(got, "last 20 of 100 lines shown") {
		t.Errorf("应说明保留末尾 20 行：%.300s", got)
	}
	// 末尾内容确实在（第 100 行 = LINE100）。
	if !strings.Contains(got, "LINE100") {
		t.Errorf("应保留末行 LINE100：%.300s", tailSnippet(got, 150))
	}
	// 头部内容**不应**在（成功折叠只留尾部；第 1 行 = LINE1）。
	if strings.Contains(got, "LINE1\n") {
		t.Errorf("成功折叠不应保留头部 LINE1：%.300s", got)
	}
}

// TestBuildModelOutputEmptyConfirmed —— 空输出显式确认（防"命令没执行"误判）。
func TestBuildModelOutputEmptyConfirmed(t *testing.T) {
	got := BuildModelOutput("", ToolOutputMeta{Command: "touch x", ExitCode: 0, DurationMs: 5}, nil)
	if !strings.Contains(got, "confirmed empty") {
		t.Errorf("应显式确认空：%s", got)
	}
	if !strings.Contains(got, "read_file") {
		t.Errorf("成功空输出应提示用 read_file 确认：%s", got)
	}
	// 失败的空输出**不应**有"写文件正常"的提示（那是成功专属）。
	gotFail := BuildModelOutput("", ToolOutputMeta{Command: "false", ExitCode: 1, DurationMs: 5}, nil)
	if strings.Contains(gotFail, "写文件") {
		t.Errorf("失败的空输出不应有成功专属提示：%s", gotFail)
	}
}

// TestBuildModelOutputCommandFilterOnlyOnFailure —— 命令过滤**仅失败时**应用。
func TestBuildModelOutputCommandFilterOnlyOnFailure(t *testing.T) {
	calls := 0
	filter := func(command, stdout string, exitCode int) (string, bool) {
		calls++
		return "FILTERED", true
	}

	// 成功 → 不调用过滤器。
	BuildModelOutput("ok output", ToolOutputMeta{Command: "x", ExitCode: 0, DurationMs: 1}, filter)
	if calls != 0 {
		t.Errorf("成功时不应调用命令过滤器（对账 TS），实得 %d 次", calls)
	}

	// 失败 → 调用。
	BuildModelOutput("err output", ToolOutputMeta{Command: "x", ExitCode: 1, DurationMs: 1}, filter)
	if calls != 1 {
		t.Errorf("失败时应调用命令过滤器，实得 %d 次", calls)
	}
}

// TestBuildModelOutputRawPathHint —— 有 rawPath 时含恢复提示（防 doom-loop）。
func TestBuildModelOutputRawPathHint(t *testing.T) {
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = "L"
	}
	got := BuildModelOutput(strings.Join(lines, "\n"),
		ToolOutputMeta{Command: "x", ExitCode: 0, DurationMs: 1, RawPath: "/tmp/a.raw"}, nil)
	if !strings.Contains(got, "/tmp/a.raw") {
		t.Errorf("应含 rawPath 恢复提示：%.300s", got)
	}
	if !strings.Contains(got, "不要重跑命令") {
		t.Errorf("恢复提示应劝止重跑：%.300s", got)
	}
}

// TestBuildModelOutputNoRawPathNoHint —— 无 rawPath 时提示缺席。
func TestBuildModelOutputNoRawPathNoHint(t *testing.T) {
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = "L"
	}
	got := BuildModelOutput(strings.Join(lines, "\n"),
		ToolOutputMeta{Command: "x", ExitCode: 0, DurationMs: 1}, nil)
	if strings.Contains(got, "full output:") {
		t.Errorf("无 rawPath 不应有恢复提示：%.300s", got)
	}
}

// TestCountOutputLines —— 末尾空行不计（对账 TS `countLines`）。
func TestCountOutputLines(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"a", 1},
		{"a\n", 1},
		{"a\nb", 2},
		{"a\nb\n", 2},
		{"\n\n", 2},
	}
	for _, c := range cases {
		if got := countOutputLines(c.in); got != c.want {
			t.Errorf("countOutputLines(%q)=%d want %d", c.in, got, c.want)
		}
	}
}
