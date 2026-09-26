package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/session"
)

// evidenceOracle 是 oracle.json 的结构。
//
// 由 `go/testdata/evidence/gen-oracle.ts` 从**真实 TS 代码路径**导出。
type evidenceOracle struct {
	Scenarios []struct {
		Label string `json:"label"`
		Steps []struct {
			Kind   string `json:"kind"`
			Path   string `json:"path"`
			Status string `json:"status"`
		} `json:"steps"`
		Gate struct {
			FilesModified      int  `json:"filesModified"`
			Verifications      int  `json:"verifications"`
			EditsSinceLastTest int  `json:"editsSinceLastTest"`
			HasFailedTests     bool `json:"hasFailedTests"`
			HasCodeEdits       bool `json:"hasCodeEdits"`
			HasReadTestFiles   bool `json:"hasReadTestFiles"`
		} `json:"gate"`
		Evidence struct {
			FilesModified int `json:"filesModified"`
			Verifications int `json:"verifications"`
		} `json:"evidence"`
		HasVerificationDebt bool `json:"hasVerificationDebt"`
	} `json:"scenarios"`
}

func loadEvidenceOracle(t *testing.T) evidenceOracle {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "evidence", "oracle.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败：%v（先跑 npx tsx go/testdata/evidence/gen-oracle.ts）", err)
	}
	var o evidenceOracle
	if err := json.Unmarshal(data, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	if len(o.Scenarios) == 0 {
		t.Fatal("oracle 的 scenarios 为空——生成器可能失败")
	}
	return o
}

// replay 按事件序列重放，同时驱动 **gate 计数器**与**会话状态**。
//
// 这是接线后的真实形态：两个追踪器**并列**接收同一事件流
// （`loop.go` 的 `observeToolResult` 就是这么做的）。
func replay(steps []struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Status string `json:"status"`
}) (*evidenceTracker, session.SessionState) {
	e := newEvidenceTracker()
	m := session.New("oracle-replay")
	for _, s := range steps {
		switch s.Kind {
		case "read":
			m.TrackFileRead(s.Path, "")
		case "modify":
			m.TrackFileModified(s.Path)
			e.TrackFileModified(s.Path)
		case "verify":
			m.RecordVerification(s.Path, s.Status)
			e.TrackVerification(s.Status)
		}
	}
	return e, m.Snapshot()
}

// TestEvidenceTrackerOracle —— 按事件序列重放，逐值对账 TS。
func TestEvidenceTrackerOracle(t *testing.T) {
	o := loadEvidenceOracle(t)

	for _, sc := range o.Scenarios {
		t.Run(sc.Label, func(t *testing.T) {
			e, snap := replay(sc.Steps)

			// gate 计数从两个来源合成：会话状态（filesModified/verifications）
			// + 计数器（editsSinceLastTest/hasCodeEdits/hasFailedTests）。
			fm := EvidenceStateFromSession(snap)
			readPaths := snap.FileIndex.Keys()
			g := e.GateState(fm, readPaths)
			vc := g.Verifications

			if g.FilesModified != sc.Gate.FilesModified {
				t.Errorf("FilesModified = %d, want %d", g.FilesModified, sc.Gate.FilesModified)
			}
			if g.Verifications != sc.Gate.Verifications {
				t.Errorf("Verifications = %d, want %d", g.Verifications, sc.Gate.Verifications)
			}
			if g.EditsSinceLastTest != sc.Gate.EditsSinceLastTest {
				t.Errorf("EditsSinceLastTest = %d, want %d", g.EditsSinceLastTest, sc.Gate.EditsSinceLastTest)
			}
			if g.HasFailedTests != sc.Gate.HasFailedTests {
				t.Errorf("HasFailedTests = %v, want %v", g.HasFailedTests, sc.Gate.HasFailedTests)
			}
			if g.HasCodeEdits != sc.Gate.HasCodeEdits {
				t.Errorf("HasCodeEdits = %v, want %v", g.HasCodeEdits, sc.Gate.HasCodeEdits)
			}
			if g.HasReadTestFiles != sc.Gate.HasReadTestFiles {
				t.Errorf("HasReadTestFiles = %v, want %v", g.HasReadTestFiles, sc.Gate.HasReadTestFiles)
			}

			if fm != sc.Evidence.FilesModified {
				t.Errorf("evidenceState.filesModified = %d, want %d", fm, sc.Evidence.FilesModified)
			}
			if vc != sc.Evidence.Verifications {
				t.Errorf("evidenceState.verifiedCount = %d, want %d", vc, sc.Evidence.Verifications)
			}

			if got := e.HasVerificationDebt(); got != sc.HasVerificationDebt {
				t.Errorf("HasVerificationDebt = %v, want %v", got, sc.HasVerificationDebt)
			}
		})
	}
}

// TestIsCodeFile —— 代码文件判定（决定是否计入 gate 计数）。
//
// 对账 TS `CODE_EXTENSIONS`。**配置文件被刻意排除**——它们不需测试覆盖，
// 计入会让纯文档/配置工作被 gate 误拦。
func TestIsCodeFile(t *testing.T) {
	cases := []struct {
		path string
		want bool
		why  string
	}{
		{"src/a.ts", true, "TypeScript"},
		{"src/a.tsx", true, "TSX"},
		{"go/internal/x.go", true, "Go"},
		{"a.py", true, "Python"},
		{"a.rs", true, "Rust"},
		{"a.sh", true, "Shell"},
		{"a.css", true, "CSS"},
		{"README.md", false, "Markdown"},
		{"config.json", false, "JSON（配置，不需测试）"},
		{"x.yml", false, "YAML（配置）"},
		{"x.toml", false, "TOML（配置）"},
		{"x.ini", false, "INI（配置）"},
		{"x.env", false, "env（配置）"},
		{"a.txt", false, "纯文本"},
		{"noext", false, "无扩展名"},
		{"", false, "空路径"},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			if got := isCodeFile(c.path); got != c.want {
				t.Errorf("isCodeFile(%q) = %v, want %v（%s）", c.path, got, c.want, c.why)
			}
		})
	}
}

// TestIsScratchPath —— scratch 路径判定（微探针不计入 gate）。
//
// 对账 TS `isScratchPath`。**为什么要排除**：scratch 下是 throwaway 探针
// ——计入会让 RED gate **惩罚它本该鼓励的探针纪律**。
func TestIsScratchPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
		why  string
	}{
		{".rivet/scratch/probe.ts", true, "标准形态"},
		{"a/.rivet/scratch/x.go", true, "嵌套前缀"},
		{".rivet/scratch/sub/x.ts", true, "scratch 子目录"},
		{".rivet/scratch", true, "scratch 目录本身（结尾边界）"},
		{".rivet/scratchy/x.ts", false, "**不是** scratch（scratchy 是不同目录）"},
		{"src/.rivet/x.ts", false, "在 .rivet 但不在 scratch"},
		{"src/a.ts", false, "普通路径"},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			if got := isScratchPath(c.path); got != c.want {
				t.Errorf("isScratchPath(%q) = %v, want %v（%s）", c.path, got, c.want, c.why)
			}
		})
	}
}

// TestEvidenceTrackerKeySemantics —— 核心语义的**意图**测试。
//
// oracle 保证与 TS 等价，本测试保证语义可读、失败信号能定位。
func TestEvidenceTrackerKeySemantics(t *testing.T) {
	t.Run("文档编辑不计入 gate 计数", func(t *testing.T) {
		e := newEvidenceTracker()
		e.TrackFileModified("README.md")
		e.TrackFileModified("docs/x.md")
		g := e.GateState(2, nil)
		if g.EditsSinceLastTest != 0 || g.HasCodeEdits {
			t.Errorf("纯文档编辑不应计入 gate：edits=%d hasCode=%v", g.EditsSinceLastTest, g.HasCodeEdits)
		}
	})

	t.Run("scratch 探针不计入 gate 计数", func(t *testing.T) {
		e := newEvidenceTracker()
		e.TrackFileModified(".rivet/scratch/probe.ts")
		if g := e.GateState(1, nil); g.EditsSinceLastTest != 0 || g.HasCodeEdits {
			t.Errorf("scratch 探针不应计入 gate：edits=%d hasCode=%v", g.EditsSinceLastTest, g.HasCodeEdits)
		}
	})

	t.Run("任何验证都归零（含失败）", func(t *testing.T) {
		for _, st := range []string{"passed", "failed", "blocked"} {
			e := newEvidenceTracker()
			e.TrackFileModified("src/a.ts")
			e.TrackFileModified("src/b.ts")
			e.TrackVerification(st)
			if g := e.GateState(2, nil); g.EditsSinceLastTest != 0 {
				t.Errorf("验证 %s 后 editsSinceLastTest 应归零，实得 %d", st, g.EditsSinceLastTest)
			}
		}
	})

	t.Run("hasFailedTests 粘滞", func(t *testing.T) {
		e := newEvidenceTracker()
		e.TrackVerification("failed")
		e.TrackVerification("passed")
		if !e.GateState(0, nil).HasFailedTests {
			t.Error("hasFailedTests 一旦真应恒真（粘滞）")
		}
	})

	t.Run("验证债阈值是 3", func(t *testing.T) {
		e := newEvidenceTracker()
		for i := 0; i < 2; i++ {
			e.TrackFileModified("src/a.ts")
		}
		if e.HasVerificationDebt() {
			t.Error("2 次未验证编辑不该算债（阈值 3）")
		}
		e.TrackFileModified("src/a.ts")
		if !e.HasVerificationDebt() {
			t.Error("3 次未验证编辑应算债")
		}
	})

	t.Run("hasReadTestFiles 识别测试路径模式", func(t *testing.T) {
		e := newEvidenceTracker()
		for _, p := range []string{"src/a.test.ts", "go/x_test.go", "src/__tests__/a.ts", "src/a.spec.ts"} {
			if g := e.GateState(0, []string{p}); !g.HasReadTestFiles {
				t.Errorf("路径 %q 应被识别为测试文件", p)
			}
		}
		if g := e.GateState(0, []string{"src/a.ts"}); g.HasReadTestFiles {
			t.Error("普通源文件不该被识别为测试文件")
		}
	})
}

// TestEvidenceStateFromSession —— 从会话快照派生 evidenceState。
//
// **为什么派生而非另存**：`session.Manager` 已在追踪这两个事实
// （`loop.go` 已接线），另存一份会形成双重真相源。
func TestEvidenceStateFromSession(t *testing.T) {
	t.Run("只数 ModifiedByMe", func(t *testing.T) {
		m := session.New("t")
		m.TrackFileRead("src/read-only.ts", "")
		m.TrackFileModified("src/modified.ts")
		if fm := EvidenceStateFromSession(m.Snapshot()); fm != 1 {
			t.Errorf("filesModified 应只数 ModifiedByMe（1），实得 %d", fm)
		}
	})

	t.Run("验证计数由 tracker 自持（替换语义不等价）", func(t *testing.T) {
		// session.RecordVerification 是**同 target 替换**——两次同 target 只得 1。
		m := session.New("t")
		m.RecordVerification("same", "passed")
		m.RecordVerification("same", "failed")
		if n := len(m.Snapshot().Verification); n != 1 {
			t.Fatalf("前提：session 同 target 替换应得 1，实得 %d", n)
		}
		// 而 tracker 是**追加计数**——两次验证得 2（对账 TS）。
		e := newEvidenceTracker()
		e.TrackVerification("passed")
		e.TrackVerification("failed")
		if g := e.GateState(0, nil); g.Verifications != 2 {
			t.Errorf("tracker 验证计数应为 2（追加语义），实得 %d", g.Verifications)
		}
	})

	t.Run("空会话", func(t *testing.T) {
		if fm := EvidenceStateFromSession(session.New("t").Snapshot()); fm != 0 {
			t.Errorf("空会话 filesModified 应为 0，实得 %d", fm)
		}
	})
}
