package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// shellsplitOracle 是 oracle.json 的结构。
//
// 由 `go/testdata/shellsplit/gen-oracle.ts` 从**真实 TS 代码路径**导出。
// 手抄 golden 会引入自洽假绿（Go 与手抄双方同错）——本项目已因此出过事故。
type shellsplitOracle struct {
	Segments []struct {
		Label    string   `json:"label"`
		Cmd      string   `json:"cmd"`
		Segments []string `json:"segments"`
	} `json:"segments"`
	Denied []struct {
		Label    string   `json:"label"`
		Cmd      string   `json:"cmd"`
		Denylist []string `json:"denylist"`
		Denied   bool     `json:"denied"`
	} `json:"denied"`
}

func loadShellsplitOracle(t *testing.T) shellsplitOracle {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "shellsplit", "oracle.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败：%v（先跑 npx tsx go/testdata/shellsplit/gen-oracle.ts）", err)
	}
	var o shellsplitOracle
	if err := json.Unmarshal(data, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	if len(o.Segments) == 0 {
		t.Fatal("oracle 的 segments 为空——生成器可能失败")
	}
	return o
}

// TestSplitShellSegmentsOracle —— 分段结果与 TS 逐值对账。
func TestSplitShellSegmentsOracle(t *testing.T) {
	o := loadShellsplitOracle(t)
	for _, c := range o.Segments {
		t.Run(c.Label, func(t *testing.T) {
			got := splitShellSegments(c.Cmd)
			if len(got) != len(c.Segments) {
				t.Fatalf("段数不符：got %d 段 %q，want %d 段 %q（cmd=%q）",
					len(got), got, len(c.Segments), c.Segments, c.Cmd)
			}
			for i := range got {
				if got[i] != c.Segments[i] {
					t.Errorf("第 %d 段：got %q，want %q（cmd=%q）", i, got[i], c.Segments[i], c.Cmd)
				}
			}
		})
	}
}

// TestIsBashCommandDeniedOracle —— deny 判定与 TS 逐值对账（命令 × denylist 矩阵）。
func TestIsBashCommandDeniedOracle(t *testing.T) {
	o := loadShellsplitOracle(t)
	if len(o.Denied) == 0 {
		t.Fatal("oracle 的 denied 为空")
	}
	for _, c := range o.Denied {
		t.Run(c.Label, func(t *testing.T) {
			got := IsBashCommandDenied(c.Cmd, c.Denylist)
			if got != c.Denied {
				t.Errorf("IsBashCommandDenied(%q, %v) = %v, want %v",
					c.Cmd, c.Denylist, got, c.Denied)
			}
		})
	}
}

// TestIsBashCommandDeniedHiddenSegments —— **安全核心**：藏执行符后的命令仍被拦。
//
// 这些是 denylist 的存在意义所在。若分段只切顶层（不抽命令替换体），
// `echo $(taskkill …)` 会漏过——oracle 已固化 TS 行为，本测试把**意图**
// 也写下来（oracle 保证等价，本测试保证语义可读）。
func TestIsBashCommandDeniedHiddenSegments(t *testing.T) {
	deny := []string{"taskkill"}
	cases := []struct {
		label string
		cmd   string
		want  bool
	}{
		{"直接调用", "taskkill /f /im x.exe", true},
		{"分号后隐藏", "echo hi; taskkill /f /im x.exe", true},
		{"&&后隐藏", "ls && taskkill /f /im x.exe", true},
		{"||后隐藏", "ls || taskkill /f /im x.exe", true},
		{"管道后隐藏", "cat f | taskkill /f /im x.exe", true},
		{"换行后隐藏", "ls\ntaskkill /f /im x.exe", true},
		{"命令替换内隐藏", "echo $(taskkill /f /im x.exe)", true},
		{"反引号内隐藏", "echo `taskkill /f /im x.exe`", true},
		{"环境赋值前缀", "CI=true taskkill /f /im x.exe", true},
		// 反例：不得误伤。
		{"参数含该词不匹配", "echo taskkill", false},
		{"token 边界", "rmdir /s x", false}, // denylist 是 rm 时才测边界；此处 taskkill 无关
		{"无关命令", "ls -la", false},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			if got := IsBashCommandDenied(c.cmd, deny); got != c.want {
				t.Errorf("IsBashCommandDenied(%q, %v) = %v, want %v", c.cmd, deny, got, c.want)
			}
		})
	}
}

// TestSegmentMatchesDenyPrefixTokenBoundary —— token 边界（`rm` 不匹配 `rmdir`）。
//
// 对账 TS：前缀后必须是结尾/空格/tab，故 `rmdir` 不被 `rm` 命中——
// 否则 denylist 写 `rm` 会误伤所有以 `rm` 开头的命令。
func TestSegmentMatchesDenyPrefixTokenBoundary(t *testing.T) {
	cases := []struct {
		seg    string
		deny   []string
		want   bool
		reason string
	}{
		{"rm -rf build", []string{"rm"}, true, "空格边界"},
		{"rm", []string{"rm"}, true, "结尾边界"},
		{"rmdir x", []string{"rm"}, false, "无 token 边界——不得匹配"},
		{"rmx", []string{"rm"}, false, "无 token 边界"},
		{"  rm -rf x", []string{"rm"}, true, "前导空白被 trim"},
		{"CI=true rm -rf x", []string{"rm"}, true, "环境赋值被剥离"},
		{"A=1 B=2 rm x", []string{"rm"}, true, "多个环境赋值"},
		{"", []string{"rm"}, false, "空段"},
		{"rm x", []string{""}, false, "空 entry 不匹配"},
		{"git push origin main", []string{"git push"}, true, "含空格的前缀"},
		{"git pushx", []string{"git push"}, false, "含空格前缀的边界"},
	}
	for _, c := range cases {
		t.Run(c.seg+"|"+c.reason, func(t *testing.T) {
			if got := segmentMatchesDenyPrefix(c.seg, c.deny); got != c.want {
				t.Errorf("segmentMatchesDenyPrefix(%q, %v) = %v, want %v（%s）",
					c.seg, c.deny, got, c.want, c.reason)
			}
		})
	}
}
