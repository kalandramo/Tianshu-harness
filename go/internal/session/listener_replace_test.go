package session

import (
	"os"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/prompt"
)

// newTestPersist 构造落盘到临时目录的 Persist。
func newTestPersist(t *testing.T, sessionID string) *Persist {
	t.Helper()
	p, err := NewPersist(sessionID, t.TempDir())
	if err != nil {
		t.Fatalf("NewPersist 失败：%v", err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func oaiMsg(role, content string) OaiMessage {
	c := content
	return OaiMessage{Role: role, Content: &c}
}

// TestOnReplaceRewritesFile —— 替换后文件内容与传入消息一致（全量重写）。
func TestOnReplaceRewritesFile(t *testing.T) {
	p := newTestPersist(t, "s1")
	// 先追加两条旧消息。
	if err := p.AppendOai(oaiMsg("user", "old-1"), true); err != nil {
		t.Fatal(err)
	}
	if err := p.AppendOai(oaiMsg("assistant", "old-2"), true); err != nil {
		t.Fatal(err)
	}

	l := NewPersistListener(p, New("s1"))
	l.OnReplace([]OaiMessage{oaiMsg("user", "new-1")})

	got := p.LoadOai()
	if len(got) != 1 {
		t.Fatalf("替换后应只有 1 条，实得 %d：%+v", len(got), got)
	}
	if got[0].Content == nil || *got[0].Content != "new-1" {
		t.Errorf("内容不符：%+v", got[0])
	}
}

// TestOnReplacePreservesAuditLines —— **核心不变量**：审计行必须在重写后存活。
//
// 对账 TS `collectAuditLines` 的注释：审计行从不进内存（回放时被跳过），
// 不保留的话第一次重写就会**静默销毁审计轨迹**。
func TestOnReplacePreservesAuditLines(t *testing.T) {
	p := newTestPersist(t, "s1")
	// 手工写入一条审计行（模拟 model_switch 记录）+ 一条普通消息。
	auditLine := `{"type":"model_switch","from":"a","to":"b"}`
	if err := p.AppendRaw(auditLine); err != nil {
		t.Fatal(err)
	}
	if err := p.AppendOai(oaiMsg("user", "hello"), true); err != nil {
		t.Fatal(err)
	}

	l := NewPersistListener(p, New("s1"))
	l.OnReplace([]OaiMessage{oaiMsg("user", "compacted")})

	// 审计行应仍在文件里。
	text := p.ReadTranscriptText()
	if !strings.Contains(text, `"model_switch"`) {
		t.Errorf("审计行应在重写后存活，实得文件内容：\n%s", text)
	}
	// 且它能被解析出来（校验和重新生成有效）。
	found := false
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if r := prompt.VerifyAndExtract(line); r.Valid && strings.Contains(r.JSON, "model_switch") {
			found = true
		}
	}
	if !found {
		t.Error("审计行应带有效校验和")
	}
	// 内存回放仍只看到 OAI 消息（审计行不进内存）。
	if got := p.LoadOai(); len(got) != 1 {
		t.Errorf("回放应只看到 1 条 OAI 消息，实得 %d", len(got))
	}
}

// TestOnReplaceIgnoresNonAuditLines —— 非审计行不得被保留（它们会被重写丢弃）。
func TestOnReplaceIgnoresNonAuditLines(t *testing.T) {
	p := newTestPersist(t, "s1")
	// 一条不在审计类型集合里的 type 行。
	if err := p.AppendRaw(`{"type":"something_else","x":1}`); err != nil {
		t.Fatal(err)
	}
	l := NewPersistListener(p, New("s1"))
	l.OnReplace([]OaiMessage{oaiMsg("user", "only")})

	text := p.ReadTranscriptText()
	if strings.Contains(text, "something_else") {
		t.Errorf("非审计行不应被保留：\n%s", text)
	}
}

// TestOnReplaceFlushesPendingFirst —— 先 flush 再重写（否则旧行会被重写后又写出）。
func TestOnReplaceFlushesPendingFirst(t *testing.T) {
	p := newTestPersist(t, "s1")
	// 追加但**不 flush**（进 pending 缓冲）。
	if err := p.AppendOai(oaiMsg("user", "pending-old"), false); err != nil {
		t.Fatal(err)
	}
	l := NewPersistListener(p, New("s1"))
	l.OnReplace([]OaiMessage{oaiMsg("user", "replaced")})

	got := p.LoadOai()
	if len(got) != 1 {
		t.Fatalf("替换后应只有 1 条（旧 pending 不得残留），实得 %d：%+v", len(got), got)
	}
	if *got[0].Content != "replaced" {
		t.Errorf("内容不符：%q", *got[0].Content)
	}
}

// TestOnReplaceEmptyMessages —— 空替换（清空历史）应可行。
func TestOnReplaceEmptyMessages(t *testing.T) {
	p := newTestPersist(t, "s1")
	p.AppendOai(oaiMsg("user", "x"), true)
	l := NewPersistListener(p, New("s1"))
	l.OnReplace(nil)

	if got := p.LoadOai(); len(got) != 0 {
		t.Errorf("空替换后应无消息，实得 %d", len(got))
	}
}

// TestOnReplaceAtomicNoTempLeftover —— 原子写不留下 tmp 文件。
func TestOnReplaceAtomicNoTempLeftover(t *testing.T) {
	p := newTestPersist(t, "s1")
	l := NewPersistListener(p, New("s1"))
	l.OnReplace([]OaiMessage{oaiMsg("user", "a")})

	dir := strings.TrimSuffix(p.FilePath(), "/"+lastPathSeg(p.FilePath()))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("目录不可读：%v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("不应残留临时文件：%s", e.Name())
		}
	}
}

func lastPathSeg(p string) string {
	i := strings.LastIndexAny(p, `/\`)
	if i < 0 {
		return p
	}
	return p[i+1:]
}
