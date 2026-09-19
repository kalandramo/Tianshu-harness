package context

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ClaimStore 是 claim 事件存储（JSONL 落盘 + 投影读回）。
//
// 对账 src/context/claim-store.ts 的 ContextClaimStore。**Scope Check**：
// TS 侧含写链治理（异步写 / 梯度重试 / 停链诊断 / 外部修改检测）——那是为
// 治理 Node 事件循环饥饿设计的（TS 注释引 issue #61 族）。**Go 的并发模型
// 不同，不复刻这套**；这里用惯用方式（同步写 + 显式错误返回）实现等价语义。
//
// **格式必须与 TS 兼容**（跨版本可读）：JSONL 每行一个事件，
// `JSON.stringify({...event, seq})`——**键序是构造序 + seq 在末尾**。
type ClaimStore struct {
	// Path 是 JSONL 落盘路径（`<dir>/<sessionId>.claims.jsonl`）。
	Path string
	// SessionID 是会话标识。
	SessionID string

	// events 是内存事件流（权威）。
	events []ClaimEvent
	// nextSeq 是单写者序号。
	//
	// 对账 TS 注释：`Single-writer assumption: seq is per-instance, not
	// coordinated across processes`——claim-store 是会话级的，每个会话恰好
	// 一个写者。
	nextSeq int64
}

// NewClaimStore 构造 store 并尝试从盘读回既有事件。
//
// 对账 ContextClaimStore 构造 + appendEvent 的惰性读。
// **读失败不阻塞**——store 仍可用（只是丢历史），错误由调用方决定是否上报。
func NewClaimStore(dir, sessionID string) (*ClaimStore, error) {
	if sessionID == "" {
		return nil, errors.New("claim-store: sessionId 不能为空")
	}
	s := &ClaimStore{
		Path:      filepath.Join(dir, sessionID+".claims.jsonl"),
		SessionID: sessionID,
		nextSeq:   1,
	}
	// 尽力读回（文件不存在不是错误——全新会话）
	if err := s.readEvents(); err != nil {
		return s, err
	}
	return s, nil
}

// AppendEvent 追加一条事件（内存 + 落盘）。
//
// 对账 appendEvent。**与 TS 的差异**：TS 走异步写链（内存先更新、行入队后
// 异步落盘），这里**同步落盘**——Go 无事件循环饥饿问题，同步写更简单且
// 无崩溃窗口。**代价**：写失败会直接返回错误（TS 是延后诊断）。
//
// **seq 分配**：未指定时用 nextSeq（对账 `event.seq ?? this.nextSeq`）。
func (s *ClaimStore) AppendEvent(ev ClaimEvent) error {
	if ev.Seq == 0 {
		ev.Seq = s.nextSeq
	}
	if ev.Seq >= s.nextSeq {
		s.nextSeq = ev.Seq + 1
	}

	line, err := marshalClaimEvent(ev)
	if err != nil {
		return err
	}

	f, err := os.OpenFile(s.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		return err
	}

	s.events = append(s.events, ev)
	return nil
}

// readEvents 从盘读回事件流。
//
// 对账 readEvents 的**无缓存路径**（首次读）。**不复刻** TS 的外部修改检测
// （靠字节数比对 + 在途字节基线）——那与异步写链配套，Go 侧同步写无此问题。
func (s *ClaimStore) readEvents() error {
	f, err := os.Open(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 全新会话
		}
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	// 大行支持（claim 对象可能很大）
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	var events []ClaimEvent
	var maxSeq int64
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		ev, err := unmarshalClaimEvent([]byte(line))
		if err != nil {
			// **坏行跳过而非中断**——单行损坏不该让整个 store 不可用
			continue
		}
		events = append(events, ev)
		if ev.Seq > maxSeq {
			maxSeq = ev.Seq
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}

	s.events = events
	if maxSeq+1 > s.nextSeq {
		s.nextSeq = maxSeq + 1
	}
	return nil
}

// ListClaims 返回投影后的 claim（可按维度过滤）。
func (s *ClaimStore) ListClaims(status []ContextClaimStatus, kind []ContextClaimKind, scope []ContextClaimScope) []ContextClaim {
	return FilterClaims(ProjectClaims(s.events), status, kind, scope)
}

// Events 返回内存事件流的副本（诊断用）。
func (s *ClaimStore) Events() []ClaimEvent {
	return append([]ClaimEvent(nil), s.events...)
}

// NextSeq 返回下一个将分配的序号。
func (s *ClaimStore) NextSeq() int64 { return s.nextSeq }

// Propose 固化一条 claim 提案并落盘事件。
//
// 对账 propose。返回固化后的 claim（**即使已存在同 ID 也返回**——
// 对账 TS 的 `listClaims().find(id)`）。
func (s *ClaimStore) Propose(p ClaimProposal) (ContextClaim, error) {
	claim := CreateClaimFromProposal(p)
	// 对账 TS 的 eventId：`${proposal.source.eventId}:claim:${claim.id}`
	eventID := p.Source.EventID + ":claim:" + claim.ID

	if err := s.AppendEvent(ClaimEvent{
		Type:      EventClaimProposed,
		EventID:   eventID,
		CreatedAt: p.CreatedAt,
		Claim:     &claim,
	}); err != nil {
		return ContextClaim{}, err
	}
	// 从投影取回（幂等语义下可能与传入的不同）
	for _, c := range s.ListClaims(nil, nil, nil) {
		if c.ID == claim.ID {
			return c, nil
		}
	}
	return claim, nil
}

// UpdateClaimStatus 改 claim 状态并落盘。
//
// 对账 updateClaimStatus。**eventId 格式**：`${id}:status:${status}:${now}`。
func (s *ClaimStore) UpdateClaimStatus(id string, status ContextClaimStatus, reason string, now int64) (ContextClaim, error) {
	eventID := id + ":status:" + string(status) + ":" + strconv.FormatInt(now, 10)
	if err := s.AppendEvent(ClaimEvent{
		Type:      EventClaimStatusChanged,
		EventID:   eventID,
		CreatedAt: now,
		ClaimID:   id,
		Status:    status,
		Reason:    reason,
	}); err != nil {
		return ContextClaim{}, err
	}
	for _, c := range s.ListClaims(nil, nil, nil) {
		if c.ID == id {
			return c, nil
		}
	}
	return ContextClaim{}, errors.New("claim-store: claim 不存在 " + id)
}

// RecordClaimUsed 记一次消费并落盘。
//
// 对账 recordClaimUsed。**eventId 格式**：`${id}:used:${consumerId}:${usedAt}`。
func (s *ClaimStore) RecordClaimUsed(id string, consumerID, consumerKind string, usedAt int64) (ContextClaim, error) {
	eventID := id + ":used:" + consumerID + ":" + strconv.FormatInt(usedAt, 10)
	if err := s.AppendEvent(ClaimEvent{
		Type:         EventClaimUsed,
		EventID:      eventID,
		CreatedAt:    usedAt,
		ClaimID:      id,
		ConsumerID:   consumerID,
		ConsumerKind: consumerKind,
	}); err != nil {
		return ContextClaim{}, err
	}
	for _, c := range s.ListClaims(nil, nil, nil) {
		if c.ID == id {
			return c, nil
		}
	}
	return ContextClaim{}, errors.New("claim-store: claim 不存在 " + id)
}

// BoostFitness 提升 claim 的 fitness 并落盘。
//
// 对账 boostFitness：**取当前值 + delta，封顶 cap**。事件里带**结果值**
// （不是增量）——投影是覆盖语义。
func (s *ClaimStore) BoostFitness(id string, delta, cap float64, now int64) (ContextClaim, error) {
	var current *ContextClaim
	for _, c := range s.ListClaims(nil, nil, nil) {
		if c.ID == id {
			cc := c
			current = &cc
			break
		}
	}
	if current == nil {
		return ContextClaim{}, errors.New("claim-store: claim 不存在 " + id)
	}

	newFitness := current.Fitness + delta
	if newFitness > cap {
		newFitness = cap
	}

	eventID := id + ":boost:" + strconv.FormatInt(now, 10)
	if err := s.AppendEvent(ClaimEvent{
		Type:      EventClaimBoosted,
		EventID:   eventID,
		CreatedAt: now,
		ClaimID:   id,
		Fitness:   newFitness,
	}); err != nil {
		return ContextClaim{}, err
	}

	out := *current
	out.Fitness = newFitness
	return out, nil
}

// MarkClaimsStaleForFile 把引用指定文件的 claim 标为 stale。
//
// 对账 markClaimsStaleForFile。**这是 consistency-check hook 的真实副作用**——
// 文件被改后，引用它旧状态的 claim 过期。
//
// **跳过已 stale / quarantined 的**（对账 TS 的 continue）。
func (s *ClaimStore) MarkClaimsStaleForFile(path, reason string, now int64) ([]ContextClaim, error) {
	var changed []ContextClaim
	for _, c := range s.ListClaims(nil, nil, nil) {
		if !ClaimHasFileEvidence(c, path) {
			continue
		}
		if c.Status == StatusStale || c.Status == StatusQuarantined {
			continue
		}
		updated, err := s.UpdateClaimStatus(c.ID, StatusStale, reason, now)
		if err != nil {
			return changed, err
		}
		changed = append(changed, updated)
	}
	return changed, nil
}

// ── JSON 编解码（键序敏感，不能用 struct tag 的默认序）──

// marshalClaimEvent 序列化事件为 JSONL 行。
//
// **必须精确复刻 TS 的 `JSON.stringify({...event, seq})`**：
//   - 键序 = 事件构造序（type → eventId → createdAt → 各变体字段）
//   - `seq` 在**末尾**（展开序）
//   - undefined 字段**省略**（如 claim 的 expiresAt 为 undefined 时不输出）
func marshalClaimEvent(ev ClaimEvent) (string, error) {
	var b strings.Builder
	b.WriteByte('{')
	b.WriteString(`"type":`)
	b.WriteString(jsonString(string(ev.Type)))
	b.WriteString(`,"eventId":`)
	b.WriteString(jsonString(ev.EventID))
	b.WriteString(`,"createdAt":`)
	b.WriteString(strconv.FormatInt(ev.CreatedAt, 10))

	switch ev.Type {
	case EventClaimProposed:
		if ev.Claim != nil {
			b.WriteString(`,"claim":`)
			b.WriteString(marshalClaim(*ev.Claim))
		}
	case EventClaimStatusChanged:
		b.WriteString(`,"claimId":`)
		b.WriteString(jsonString(ev.ClaimID))
		b.WriteString(`,"status":`)
		b.WriteString(jsonString(string(ev.Status)))
		b.WriteString(`,"reason":`)
		b.WriteString(jsonString(ev.Reason))
	case EventClaimUsed:
		b.WriteString(`,"claimId":`)
		b.WriteString(jsonString(ev.ClaimID))
		b.WriteString(`,"consumerId":`)
		b.WriteString(jsonString(ev.ConsumerID))
		b.WriteString(`,"consumerKind":`)
		b.WriteString(jsonString(ev.ConsumerKind))
	case EventClaimBoosted:
		b.WriteString(`,"claimId":`)
		b.WriteString(jsonString(ev.ClaimID))
		b.WriteString(`,"fitness":`)
		b.WriteString(formatFloat(ev.Fitness))
	}

	b.WriteString(`,"seq":`)
	b.WriteString(strconv.FormatInt(ev.Seq, 10))
	b.WriteByte('}')
	return b.String(), nil
}

// marshalClaim 序列化 claim 对象（键序对账 TS 的 ContextClaim 字面量序）。
//
// **键序**：id → kind → scope → status → text → confidence → fitness →
// source → evidence → consumers → counterevidence → createdAt → lastUsedAt →
// [expiresAt] → tags
//
// **expiresAt 的特殊处理**：TS 里它是 `expiresAt?: number`——undefined 时
// JSON.stringify **省略该键**。Go 侧用 0 表示未设，故 0 时省略。
func marshalClaim(c ContextClaim) string {
	var b strings.Builder
	b.WriteByte('{')
	b.WriteString(`"id":`)
	b.WriteString(jsonString(c.ID))
	b.WriteString(`,"kind":`)
	b.WriteString(jsonString(string(c.Kind)))
	b.WriteString(`,"scope":`)
	b.WriteString(jsonString(string(c.Scope)))
	b.WriteString(`,"status":`)
	b.WriteString(jsonString(string(c.Status)))
	b.WriteString(`,"text":`)
	b.WriteString(jsonString(c.Text))
	b.WriteString(`,"confidence":`)
	b.WriteString(formatFloat(c.Confidence))
	b.WriteString(`,"fitness":`)
	b.WriteString(formatFloat(c.Fitness))
	b.WriteString(`,"source":`)
	b.WriteString(marshalSource(c.Source))
	b.WriteString(`,"evidence":`)
	b.WriteString(marshalEvidenceList(c.Evidence))
	b.WriteString(`,"consumers":`)
	b.WriteString(marshalConsumerList(c.Consumers))
	b.WriteString(`,"counterevidence":`)
	b.WriteString(marshalEvidenceList(c.Counterevidence))
	b.WriteString(`,"createdAt":`)
	b.WriteString(strconv.FormatInt(c.CreatedAt, 10))
	b.WriteString(`,"lastUsedAt":`)
	b.WriteString(strconv.FormatInt(c.LastUsedAt, 10))
	// expiresAt：0（未设）时省略——对账 TS 的 undefined
	if c.ExpiresAt != 0 {
		b.WriteString(`,"expiresAt":`)
		b.WriteString(strconv.FormatInt(c.ExpiresAt, 10))
	}
	b.WriteString(`,"tags":`)
	b.WriteString(marshalStringList(c.Tags))
	b.WriteByte('}')
	return b.String()
}

func marshalSource(s ClaimSource) string {
	return `{"actor":` + jsonString(string(s.Actor)) +
		`,"sessionId":` + jsonString(s.SessionID) +
		`,"turn":` + strconv.Itoa(s.Turn) +
		`,"eventId":` + jsonString(s.EventID) + `}`
}

// marshalEvidenceList 序列化证据列表。
//
// **键序**：id → kind → summary → [path] → createdAt。
// **path 为空时省略**——对账 TS 的 `path?: string`。
func marshalEvidenceList(evs []EvidenceRef) string {
	if len(evs) == 0 {
		return "[]"
	}
	parts := make([]string, 0, len(evs))
	for _, e := range evs {
		var b strings.Builder
		b.WriteString(`{"id":`)
		b.WriteString(jsonString(e.ID))
		b.WriteString(`,"kind":`)
		b.WriteString(jsonString(string(e.Kind)))
		b.WriteString(`,"summary":`)
		b.WriteString(jsonString(e.Summary))
		if e.Path != "" {
			b.WriteString(`,"path":`)
			b.WriteString(jsonString(e.Path))
		}
		b.WriteString(`,"createdAt":`)
		b.WriteString(strconv.FormatInt(e.CreatedAt, 10))
		b.WriteByte('}')
		parts = append(parts, b.String())
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func marshalConsumerList(cs []ConsumerRef) string {
	if len(cs) == 0 {
		return "[]"
	}
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		parts = append(parts, `{"id":`+jsonString(c.ID)+
			`,"kind":`+jsonString(c.Kind)+
			`,"usedAt":`+strconv.FormatInt(c.UsedAt, 10)+`}`)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func marshalStringList(xs []string) string {
	if len(xs) == 0 {
		return "[]"
	}
	parts := make([]string, 0, len(xs))
	for _, x := range xs {
		parts = append(parts, jsonString(x))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// formatFloat 格式化浮点数（对账 JS 的 Number → JSON 序列化）。
//
// **注意**：JS 的 JSON.stringify 对整数浮点输出 `0.5`、`1`（无小数点），
// 对 `0.7` 输出 `0.7`。Go 的 strconv.FormatFloat(v,'f',-1,64) 行为一致。
func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// unmarshalClaimEvent 解析 JSONL 行为事件。
//
// **用 encoding/json 的 map 解析**——读路径不需要键序（只有写路径需要）。
func unmarshalClaimEvent(line []byte) (ClaimEvent, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		return ClaimEvent{}, err
	}

	var ev ClaimEvent
	if v, ok := raw["type"]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			ev.Type = ClaimEventType(s)
		}
	}
	if v, ok := raw["eventId"]; ok {
		_ = json.Unmarshal(v, &ev.EventID)
	}
	if v, ok := raw["createdAt"]; ok {
		_ = json.Unmarshal(v, &ev.CreatedAt)
	}
	if v, ok := raw["seq"]; ok {
		_ = json.Unmarshal(v, &ev.Seq)
	}
	if v, ok := raw["claimId"]; ok {
		_ = json.Unmarshal(v, &ev.ClaimID)
	}
	if v, ok := raw["status"]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			ev.Status = ContextClaimStatus(s)
		}
	}
	if v, ok := raw["reason"]; ok {
		_ = json.Unmarshal(v, &ev.Reason)
	}
	if v, ok := raw["consumerId"]; ok {
		_ = json.Unmarshal(v, &ev.ConsumerID)
	}
	if v, ok := raw["consumerKind"]; ok {
		_ = json.Unmarshal(v, &ev.ConsumerKind)
	}
	if v, ok := raw["fitness"]; ok {
		_ = json.Unmarshal(v, &ev.Fitness)
	}
	if v, ok := raw["claim"]; ok {
		var c ContextClaim
		if err := json.Unmarshal(v, &c); err == nil {
			ev.Claim = &c
		}
	}
	return ev, nil
}
