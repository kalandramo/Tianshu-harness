package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestClaimStoreRoundTrip —— **落盘 + 读回往返**。
//
// 用户动作：往 store 写事件 → 新建 store 从同一目录读回。
// 观察到：投影出的 claim 与写入前**逐字段一致**。
//
// 这是落盘层的核心判据——写读不对称（如 JSON tag 缺失）会在这里暴露。
func TestClaimStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()

	s1, err := NewClaimStore(dir, "sess-rt")
	if err != nil {
		t.Fatalf("构造 store 失败：%v", err)
	}

	claim, err := s1.Propose(ClaimProposal{
		Kind:       ClaimFileObservation,
		Scope:      ScopeSession,
		Text:       "a.ts 里有函数 X",
		Confidence: 0.7,
		Fitness:    0.5,
		Source:     ClaimSource{Actor: "tool", SessionID: "sess-rt", Turn: 1, EventID: "e1"},
		Evidence: []EvidenceRef{
			{ID: "ev1", Kind: "tool_result", Summary: "读到 a.ts", Path: "a.ts", CreatedAt: T0},
		},
		CreatedAt: T0,
		Tags:      []string{"t"},
	})
	if err != nil {
		t.Fatalf("propose 失败：%v", err)
	}

	if _, err := s1.UpdateClaimStatus(claim.ID, StatusStale, "文件被改", T0+1000); err != nil {
		t.Fatalf("status 失败：%v", err)
	}
	if _, err := s1.RecordClaimUsed(claim.ID, "u1", "prompt", T0+2000); err != nil {
		t.Fatalf("use 失败：%v", err)
	}
	if _, err := s1.BoostFitness(claim.ID, 0.2, 1.0, T0+3000); err != nil {
		t.Fatalf("boost 失败：%v", err)
	}

	before := s1.ListClaims(nil, nil, nil)
	if len(before) != 1 {
		t.Fatalf("应有 1 条 claim，得到 %d", len(before))
	}

	// ── 新建 store 从盘读回 ──
	s2, err := NewClaimStore(dir, "sess-rt")
	if err != nil {
		t.Fatalf("重开 store 失败：%v", err)
	}
	after := s2.ListClaims(nil, nil, nil)

	if len(after) != 1 {
		t.Fatalf("读回应有 1 条 claim，得到 %d\n（0 条说明落盘格式与读回不匹配）", len(after))
	}

	g, w := after[0], before[0]
	if g.ID != w.ID {
		t.Errorf("ID：读回=%q 写入前=%q", g.ID, w.ID)
	}
	if g.Kind != w.Kind {
		t.Errorf("kind：读回=%q 写入前=%q", g.Kind, w.Kind)
	}
	if g.Scope != w.Scope {
		t.Errorf("scope：读回=%q 写入前=%q", g.Scope, w.Scope)
	}
	if g.Status != w.Status {
		t.Errorf("status：读回=%q 写入前=%q", g.Status, w.Status)
	}
	if g.Text != w.Text {
		t.Errorf("text：读回=%q 写入前=%q", g.Text, w.Text)
	}
	if g.Confidence != w.Confidence {
		t.Errorf("confidence：读回=%v 写入前=%v", g.Confidence, w.Confidence)
	}
	if g.Fitness != w.Fitness {
		t.Errorf("fitness：读回=%v 写入前=%v", g.Fitness, w.Fitness)
	}
	if g.Source.Actor != w.Source.Actor {
		t.Errorf("source.actor：读回=%q 写入前=%q", g.Source.Actor, w.Source.Actor)
	}
	if g.Source.SessionID != w.Source.SessionID {
		t.Errorf("source.sessionId：读回=%q 写入前=%q", g.Source.SessionID, w.Source.SessionID)
	}
	if g.CreatedAt != w.CreatedAt {
		t.Errorf("createdAt：读回=%d 写入前=%d", g.CreatedAt, w.CreatedAt)
	}
	if g.LastUsedAt != w.LastUsedAt {
		t.Errorf("lastUsedAt：读回=%d 写入前=%d", g.LastUsedAt, w.LastUsedAt)
	}
	if len(g.Evidence) != len(w.Evidence) {
		t.Fatalf("evidence 数：读回=%d 写入前=%d", len(g.Evidence), len(w.Evidence))
	}
	if len(g.Evidence) > 0 {
		if g.Evidence[0].ID != w.Evidence[0].ID {
			t.Errorf("evidence.id：读回=%q 写入前=%q", g.Evidence[0].ID, w.Evidence[0].ID)
		}
		if g.Evidence[0].Path != w.Evidence[0].Path {
			t.Errorf("evidence.path：读回=%q 写入前=%q", g.Evidence[0].Path, w.Evidence[0].Path)
		}
	}
	if len(g.Consumers) != len(w.Consumers) {
		t.Fatalf("consumers 数：读回=%d 写入前=%d", len(g.Consumers), len(w.Consumers))
	}
	if len(g.Consumers) > 0 && g.Consumers[0].ID != w.Consumers[0].ID {
		t.Errorf("consumer.id：读回=%q 写入前=%q", g.Consumers[0].ID, w.Consumers[0].ID)
	}
	if len(g.Counterevidence) != len(w.Counterevidence) {
		t.Errorf("反证数：读回=%d 写入前=%d", len(g.Counterevidence), len(w.Counterevidence))
	}
	if len(g.Tags) != len(w.Tags) {
		t.Errorf("tags 数：读回=%d 写入前=%d", len(g.Tags), len(w.Tags))
	}
}

// TestClaimStoreJSONLLineFormat —— **JSONL 行格式逐字节对账**。
//
// 对账 TS 的 `JSON.stringify({...event, seq})`：
//   - 键序 = 事件构造序，`seq` 在**末尾**
//   - undefined 字段省略（如 expiresAt）
func TestClaimStoreJSONLLineFormat(t *testing.T) {
	dir := t.TempDir()
	s, err := NewClaimStore(dir, "sess-fmt")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Propose(ClaimProposal{
		Kind:       ClaimFileObservation,
		Scope:      ScopeSession,
		Text:       "a.ts 有 X",
		Confidence: 0.7,
		Fitness:    0.5,
		Source:     ClaimSource{Actor: "tool", SessionID: "sess-fmt", Turn: 1, EventID: "e1"},
		Evidence: []EvidenceRef{
			{ID: "ev1", Kind: "tool_result", Summary: "s", Path: "a.ts", CreatedAt: T0},
		},
		CreatedAt: T0,
		Tags:      []string{"t"},
	}); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "sess-fmt.claims.jsonl"))
	if err != nil {
		t.Fatalf("读 JSONL 失败：%v", err)
	}
	line := strings.TrimSpace(string(raw))

	// ── 键序断言（seq 在末尾）──
	if !strings.HasSuffix(line, `,"seq":1}`) {
		t.Errorf("seq 应在末尾且值为 1，实际行尾：%q", line[max(0, len(line)-40):])
	}
	// type 在最前
	if !strings.HasPrefix(line, `{"type":"claim_proposed"`) {
		t.Errorf("type 应在最前，实际行首：%q", line[:min(60, len(line))])
	}
	// 键序：type → eventId → createdAt → claim → seq
	wantOrder := []string{`"type":`, `"eventId":`, `"createdAt":`, `"claim":`, `"seq":`}
	last := -1
	for _, k := range wantOrder {
		i := strings.Index(line, k)
		if i < 0 {
			t.Errorf("缺键 %s", k)
			continue
		}
		if i < last {
			t.Errorf("键 %s 顺序不对（应在位置 > %d，实际 %d）", k, last, i)
		}
		last = i
	}

	// ── expiresAt 省略（未设时）──
	if strings.Contains(line, `"expiresAt"`) {
		t.Errorf("expiresAt 未设时应省略，实际：%q", line)
	}

	// ── claim 内键序 ──
	//
	// **注意**：必须从 claim 对象**起点**开始扫——evidence 里也有
	// `"createdAt":`，全行 Index 会命中错误位置（首版断言就栽在这）。
	claimStart := strings.Index(line, `"claim":{`)
	if claimStart < 0 {
		t.Fatal("未找到 claim 对象")
	}
	claimPart := line[claimStart:]

	claimOrder := []string{
		`"id":`, `"kind":`, `"scope":`, `"status":`, `"text":`,
		`"confidence":`, `"fitness":`, `"source":`, `"evidence":`,
		`"consumers":`, `"counterevidence":`, `"lastUsedAt":`, `"tags":`,
	}
	last = -1
	for _, k := range claimOrder {
		i := strings.Index(claimPart, k)
		if i < 0 {
			t.Errorf("claim 缺键 %s", k)
			continue
		}
		if i < last {
			t.Errorf("claim 键 %s 顺序不对", k)
		}
		last = i
	}
	// claim 层级的 createdAt 应在 counterevidence 之后、lastUsedAt 之前
	ceIdx := strings.Index(claimPart, `"counterevidence":`)
	caIdx := strings.Index(claimPart[ceIdx:], `"createdAt":`)
	if caIdx < 0 {
		t.Error("claim 层级缺 createdAt")
	}
}

// TestClaimStoreSeqMonotonic —— seq 单调递增且跨重开延续。
func TestClaimStoreSeqMonotonic(t *testing.T) {
	dir := t.TempDir()
	s1, _ := NewClaimStore(dir, "sess-seq")

	c, _ := s1.Propose(ClaimProposal{
		Kind: ClaimDecision, Scope: ScopeSession, Text: "d", Confidence: 0.8, Fitness: 0.5,
		Source:    ClaimSource{Actor: "assistant", SessionID: "sess-seq", Turn: 1, EventID: "e1"},
		Evidence:  []EvidenceRef{{ID: "ev", Kind: "assistant_message", Summary: "s", CreatedAt: T0}},
		CreatedAt: T0,
	})
	_, _ = s1.UpdateClaimStatus(c.ID, StatusStale, "r", T0+1)

	if got := s1.NextSeq(); got != 3 {
		t.Errorf("两条事件后 nextSeq 应为 3，得到 %d", got)
	}

	// 重开 → 应延续（从 max seq + 1）
	s2, err := NewClaimStore(dir, "sess-seq")
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.NextSeq(); got != 3 {
		t.Errorf("重开后 nextSeq 应延续为 3，得到 %d", got)
	}
}

// TestClaimStoreMarkStaleForFile —— **consistency-check 的真实副作用**。
//
// 用户动作：写入某文件 → 调用 MarkClaimsStaleForFile。
// 观察到：引用该文件的 claim 被标 stale，其余不受影响。
func TestClaimStoreMarkStaleForFile(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewClaimStore(dir, "sess-stale")

	mk := func(text, path string) ContextClaim {
		c, err := s.Propose(ClaimProposal{
			Kind: ClaimFileObservation, Scope: ScopeSession, Text: text,
			Confidence: 0.7, Fitness: 0.5,
			Source:    ClaimSource{Actor: "tool", SessionID: "sess-stale", Turn: 1, EventID: "e:" + text},
			Evidence:  []EvidenceRef{{ID: "ev", Kind: "tool_result", Summary: "s", Path: path, CreatedAt: T0}},
			CreatedAt: T0,
		})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}

	cA := mk("a.ts 有 X", "a.ts")
	cB := mk("b.ts 有 Y", "b.ts")

	changed, err := s.MarkClaimsStaleForFile("a.ts", "文件被改", T0+1000)
	if err != nil {
		t.Fatalf("markStale 失败：%v", err)
	}
	if len(changed) != 1 || changed[0].ID != cA.ID {
		t.Fatalf("应只标记 a.ts 的 claim，得到 %d 条", len(changed))
	}

	claims := s.ListClaims(nil, nil, nil)
	for _, c := range claims {
		if c.ID == cA.ID && c.Status != StatusStale {
			t.Errorf("a.ts 的 claim 应 stale，得到 %q", c.Status)
		}
		if c.ID == cB.ID && c.Status != StatusActive {
			t.Errorf("b.ts 的 claim 应仍 active，得到 %q", c.Status)
		}
	}

	// 重复调用 → 已 stale 的跳过
	again, err := s.MarkClaimsStaleForFile("a.ts", "再改", T0+2000)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Errorf("已 stale 的 claim 应跳过，得到 %d 条", len(again))
	}
}

// TestClaimStoreReadCorruptedLineSkipped —— 坏行跳过而非中断。
func TestClaimStoreReadCorruptedLineSkipped(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewClaimStore(dir, "sess-bad")
	if _, err := s.Propose(ClaimProposal{
		Kind: ClaimDecision, Scope: ScopeSession, Text: "d", Confidence: 0.8, Fitness: 0.5,
		Source:    ClaimSource{Actor: "assistant", SessionID: "sess-bad", Turn: 1, EventID: "e1"},
		Evidence:  []EvidenceRef{{ID: "ev", Kind: "assistant_message", Summary: "s", CreatedAt: T0}},
		CreatedAt: T0,
	}); err != nil {
		t.Fatal(err)
	}

	// 追加一行坏 JSON
	f, _ := os.OpenFile(filepath.Join(dir, "sess-bad.claims.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("{ this is not json\n")
	f.Close()

	// 重开应仍能读出 1 条（坏行被跳过）
	s2, err := NewClaimStore(dir, "sess-bad")
	if err != nil {
		t.Fatalf("坏行不应让构造失败：%v", err)
	}
	if got := len(s2.ListClaims(nil, nil, nil)); got != 1 {
		t.Errorf("坏行应被跳过，仍读出 1 条，得到 %d", got)
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestClaimStoreSeqPreservedFromEvent —— **显式 seq 被沿用**（不被重分配）。
//
// 对账 `event.seq ?? this.nextSeq`——已指定 seq 时**不覆盖**。
func TestClaimStoreSeqPreservedFromEvent(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewClaimStore(dir, "sess-explicit-seq")

	// 显式给 seq=100
	if err := s.AppendEvent(ClaimEvent{
		Type:      EventClaimStatusChanged,
		EventID:   "e-explicit",
		CreatedAt: T0,
		Seq:       100,
		ClaimID:   "whatever",
		Status:    StatusStale,
		Reason:    "r",
	}); err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(filepath.Join(dir, "sess-explicit-seq.claims.jsonl"))
	if !strings.Contains(string(raw), `"seq":100`) {
		t.Errorf("显式 seq=100 应被沿用，实际：%s", string(raw))
	}
	// nextSeq 应推进到 101
	if got := s.NextSeq(); got != 101 {
		t.Errorf("显式 seq=100 后 nextSeq 应为 101，得到 %d", got)
	}
}

// TestClaimStoreBoostAccumulatesCurrent —— **boost 累加当前 fitness**。
//
// 对账 boostFitness 的 `Math.min(claim.fitness + delta, cap)`。
// 若实现只用 delta（不累加），首次 boost 恰好等价——故需**连续两次** boost
// 才能判别。
func TestClaimStoreBoostAccumulatesCurrent(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewClaimStore(dir, "sess-boost")

	c, err := s.Propose(ClaimProposal{
		Kind: ClaimDecision, Scope: ScopeSession, Text: "d",
		Confidence: 0.8, Fitness: 0.5,
		Source:    ClaimSource{Actor: "assistant", SessionID: "sess-boost", Turn: 1, EventID: "e1"},
		Evidence:  []EvidenceRef{{ID: "ev", Kind: "assistant_message", Summary: "s", CreatedAt: T0}},
		CreatedAt: T0,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 第一次：0.5 + 0.1 = 0.6
	got1, err := s.BoostFitness(c.ID, 0.1, 2.0, T0+1000)
	if err != nil {
		t.Fatal(err)
	}
	if got1.Fitness != 0.6 {
		t.Fatalf("首次 boost：0.5+0.1 应为 0.6，得到 %v", got1.Fitness)
	}

	// 第二次：0.6 + 0.1 = 0.7（**判别点**——只用 delta 的实现会给 0.1）
	got2, err := s.BoostFitness(c.ID, 0.1, 2.0, T0+2000)
	if err != nil {
		t.Fatal(err)
	}
	if got2.Fitness != 0.7 {
		t.Errorf("二次 boost：0.6+0.1 应为 0.7，得到 %v\n（若为 0.1 说明未累加当前值）", got2.Fitness)
	}
}

// TestClaimStoreEvidenceNoPathOmitted —— **path 为空时省略**。
//
// 对账 TS 的 `path?: string`——undefined 时 JSON.stringify 省略该键。
func TestClaimStoreEvidenceNoPathOmitted(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewClaimStore(dir, "sess-nopath")

	if _, err := s.Propose(ClaimProposal{
		Kind: ClaimDecision, Scope: ScopeSession, Text: "d",
		Confidence: 0.8, Fitness: 0.5,
		Source: ClaimSource{Actor: "assistant", SessionID: "sess-nopath", Turn: 1, EventID: "e1"},
		// **证据无 path**
		Evidence:  []EvidenceRef{{ID: "ev", Kind: "assistant_message", Summary: "s", CreatedAt: T0}},
		CreatedAt: T0,
	}); err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(filepath.Join(dir, "sess-nopath.claims.jsonl"))
	if strings.Contains(string(raw), `"path":`) {
		t.Errorf("路径为空的证据应省略 path 键，实际：%s", string(raw))
	}
}
