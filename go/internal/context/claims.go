// Package context 提供认知上下文层。
//
// 对账 src/context/。**当前已移植**：rounds 分组（rounds.go）、claim 纯逻辑层
// （本文件）、promotion 判定（promotion.go）。
//
// **未移植**：claim-store 的事件溯源与落盘（I/O 层）、cognitive-ledger /
// stigmergy / task-contract / compact-policy 等。见 HANDOFF。
package context

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// ContextClaimKind 是 claim 的种类。
//
// 对账 ContextClaimKind。
type ContextClaimKind string

const (
	ClaimUserConstraint   ContextClaimKind = "user_constraint"
	ClaimUserPreference   ContextClaimKind = "user_preference"
	ClaimDecision         ContextClaimKind = "decision"
	ClaimFileObservation  ContextClaimKind = "file_observation"
	ClaimVerificationFact ContextClaimKind = "verification_fact"
	ClaimFailurePattern   ContextClaimKind = "failure_pattern"
	ClaimSecurityFinding  ContextClaimKind = "security_finding"
	ClaimWorkerFinding    ContextClaimKind = "worker_finding"
	ClaimProjectRule      ContextClaimKind = "project_rule"
)

// ContextClaimScope 是 claim 的作用域。
type ContextClaimScope string

const (
	ScopeTurn    ContextClaimScope = "turn"
	ScopeSession ContextClaimScope = "session"
	ScopeProject ContextClaimScope = "project"
	ScopeRepo    ContextClaimScope = "repo"
	ScopeGlobal  ContextClaimScope = "global"
)

// ContextClaimStatus 是 claim 的生命周期状态。
type ContextClaimStatus string

const (
	StatusEphemeral        ContextClaimStatus = "ephemeral"
	StatusActive           ContextClaimStatus = "active"
	StatusDurableCandidate ContextClaimStatus = "durable_candidate"
	StatusDurable          ContextClaimStatus = "durable"
	StatusStale            ContextClaimStatus = "stale"
	StatusConflicted       ContextClaimStatus = "conflicted"
	StatusQuarantined      ContextClaimStatus = "quarantined"
)

// EvidenceKind 是证据的种类。
type EvidenceKind string

// ContextActor 是行为主体。
type ContextActor string

// EvidenceRef 是一条证据引用。
//
// 对账 EvidenceRef。
type EvidenceRef struct {
	ID      string
	Kind    EvidenceKind
	Summary string
	// Path 是证据指向的文件路径（空 = 非文件类证据）。
	Path      string
	CreatedAt int64
}

// ConsumerRef 是一次消费记录。
type ConsumerRef struct {
	ID     string
	Kind   string // prompt | tool | test | worker
	UsedAt int64
}

// ClaimSource 是 claim 的来源。
type ClaimSource struct {
	Actor     ContextActor
	SessionID string
	Turn      int
	EventID   string
}

// ContextClaim 是一条已固化的 claim。
//
// 对账 ContextClaim。
type ContextClaim struct {
	ID              string
	Kind            ContextClaimKind
	Scope           ContextClaimScope
	Status          ContextClaimStatus
	Text            string
	Confidence      float64
	Fitness         float64
	Source          ClaimSource
	Evidence        []EvidenceRef
	Consumers       []ConsumerRef
	Counterevidence []EvidenceRef
	CreatedAt       int64
	LastUsedAt      int64
	// ExpiresAt 为 0 表示无过期（对账 TS 的 optional）。
	ExpiresAt int64
	Tags      []string
}

// ClaimProposal 是一条待固化的 claim 提案。
type ClaimProposal struct {
	Kind       ContextClaimKind
	Scope      ContextClaimScope
	Text       string
	Confidence float64
	Fitness    float64
	Source     ClaimSource
	Evidence   []EvidenceRef
	CreatedAt  int64
	ExpiresAt  int64
	Tags       []string
}

// ClaimProposalMeta 是从 anchor 派生提案时的元信息。
type ClaimProposalMeta struct {
	Actor     ContextActor
	SessionID string
	Turn      int
	EventID   string
	CreatedAt int64
}

// ContextAnchor 是 ledger 里的一个锚点。
//
// 对账 ContextAnchor。
type ContextAnchor struct {
	Kind             string
	Text             string
	SourceRoundIndex int
	Salience         float64
}

// MaxPromptClaims 是 prompt 里最多渲染的 claim 数。
//
// 对账 MAX_PROMPT_CLAIMS。
const MaxPromptClaims = 20

// normalizeClaimText 归一化 claim 文本。
//
// 对账 normalizeClaimText：trim → 连续空白折叠为单空格 → 小写。
func normalizeClaimText(text string) string {
	return strings.ToLower(collapseWhitespace(strings.TrimSpace(text)))
}

// collapseWhitespace 把连续空白折叠为单空格。
//
// 对账 JS 的 `replace(/\s+/g, ' ')`——**注意 JS 的 \s 包含 Unicode 空白**
// （\t \n \r \f \v 空格 以及 \u00a0 \u2028 等）。Go 的 unicode.IsSpace 覆盖
// 范围与 JS 略异，这里用显式字符集对齐常见情形。
func collapseWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inSpace := false
	for _, r := range s {
		if isJSSpace(r) {
			if !inSpace {
				b.WriteByte(' ')
				inSpace = true
			}
			continue
		}
		inSpace = false
		b.WriteRune(r)
	}
	return b.String()
}

// isJSSpace 判定 rune 是否为 JS 正则 \s 覆盖的空白。
func isJSSpace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '\f', '\v':
		return true
	case 0x00A0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

// ClaimIDFor 派生 claim ID（sha256 前 12 hex）。
//
// 对账 claimIdFor。**键序是关键**：TS 用对象字面量
// `{kind, scope, text, sessionId}`——`JSON.stringify` 保**字面量书写序**，
// 故这里手写固定键序的 JSON（不能用 Go map，会按键排序）。
func ClaimIDFor(p ClaimProposal) string {
	// 对账 JSON.stringify 的紧凑输出与键序
	payload := `{"kind":` + jsonString(string(p.Kind)) +
		`,"scope":` + jsonString(string(p.Scope)) +
		`,"text":` + jsonString(normalizeClaimText(p.Text)) +
		`,"sessionId":` + jsonString(p.Source.SessionID) + `}`
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])[:12]
}

// jsonString 序列化字符串（对账 JSON.stringify 的转义）。
//
// **注意**：TS 的 JSON.stringify 不转义 `<` `>` `&`（那是 HTML 安全的范畴），
// 但转义控制字符为 \u00XX。Go 的 encoding/json 默认会转义 `<>&`——
// 故这里手写以对齐。
func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r < 0x20 {
				b.WriteString(`\u`)
				const hexDigits = "0123456789abcdef"
				b.WriteByte(hexDigits[(r>>12)&0xF])
				b.WriteByte(hexDigits[(r>>8)&0xF])
				b.WriteByte(hexDigits[(r>>4)&0xF])
				b.WriteByte(hexDigits[r&0xF])
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// CreateClaimFromProposal 从提案固化 claim。
//
// 对账 createClaimFromProposal。**注意**：evidence / tags 是**拷贝**
// （TS 用 `[...proposal.evidence]`），不是共享引用。
func CreateClaimFromProposal(p ClaimProposal) ContextClaim {
	return ContextClaim{
		ID:              ClaimIDFor(p),
		Kind:            p.Kind,
		Scope:           p.Scope,
		Status:          StatusActive,
		Text:            p.Text,
		Confidence:      p.Confidence,
		Fitness:         p.Fitness,
		Source:          p.Source,
		Evidence:        append([]EvidenceRef(nil), p.Evidence...),
		Consumers:       nil,
		Counterevidence: nil,
		CreatedAt:       p.CreatedAt,
		LastUsedAt:      p.CreatedAt,
		ExpiresAt:       p.ExpiresAt,
		Tags:            append([]string(nil), p.Tags...),
	}
}

// IsPromptEligibleClaim 判定 claim 是否可进 prompt。
//
// 对账 isPromptEligibleClaim：未过期 **且** 状态为 active / durable_candidate /
// durable 之一。
func IsPromptEligibleClaim(c ContextClaim, now int64) bool {
	if c.ExpiresAt != 0 && c.ExpiresAt <= now {
		return false
	}
	return c.Status == StatusActive || c.Status == StatusDurableCandidate || c.Status == StatusDurable
}

// RenderActiveClaimsBlockAt 渲染活跃 claim 块。
//
// 对账 renderActiveClaimsBlock。**排序规则**（三级）：
//  1. fitness 降序
//  2. confidence 降序
//  3. createdAt 升序
//
// 取前 MaxPromptClaims 条。无可渲染 claim 时返回空串。
//
// ⚠ **对账 TS 的一处既有缺陷**（非本移植引入）：
//
//	// TS 源码
//	claims.filter(isPromptEligibleClaim)
//
// `Array.filter` 的回调签名是 `(element, index, array)`——**直接把函数引用
// 传进去，`now` 参数收到的是 index（0/1/2...），不是 `Date.now()`**。
// 后果：`expiresAt <= now` 的过期检查实际失效（除非 expiresAt ≤ 数组下标）。
//
// 实测确认（node -e）：
//
//	filter 直传：     [alive, exp, future]   ← exp 的 expiresAt 已过期却仍入选
//	显式传 Date.now()：[alive]                ← 正确行为
//
// **移植决策：忠实复刻**。理由——Go 侧若「修正」为真正的 now，同一份会话
// 状态下渲染出的 claim 集与 TS 分叉，破坏字节等价（本项目硬约束）。
// 若上游日后修了这个 bug，oracle 会红，届时同步跟进。
//
// 故本函数的 `now` 参数**只用于状态判定，不用于过期判定**——过期判定用
// 下标（对账 filter 的实参语义）。
func RenderActiveClaimsBlockAt(claims []ContextClaim, now int64) string {
	eligible := make([]ContextClaim, 0, len(claims))
	for idx, c := range claims {
		// 对账 filter 的实参：now = 数组下标
		if IsPromptEligibleClaim(c, int64(idx)) {
			eligible = append(eligible, c)
		}
	}
	_ = now // 保留参数以对齐调用方签名；过期判定不用它（见上）

	sort.SliceStable(eligible, func(i, j int) bool {
		a, b := eligible[i], eligible[j]
		if a.Fitness != b.Fitness {
			return a.Fitness > b.Fitness
		}
		if a.Confidence != b.Confidence {
			return a.Confidence > b.Confidence
		}
		return a.CreatedAt < b.CreatedAt
	})
	if len(eligible) > MaxPromptClaims {
		eligible = eligible[:MaxPromptClaims]
	}
	if len(eligible) == 0 {
		return ""
	}
	entries := make([]string, 0, len(eligible))
	for _, c := range eligible {
		evidence := ""
		if len(c.Evidence) > 0 {
			evidence = c.Evidence[0].ID
		}
		entries = append(entries,
			`  <claim id="`+escapeXML(c.ID)+
				`" kind="`+string(c.Kind)+
				`" scope="`+string(c.Scope)+
				`" confidence="`+formatConfidence(c.Confidence)+
				`" evidence="`+escapeXML(evidence)+
				`">`+escapeXML(c.Text)+`</claim>`)
	}
	return `<active-claims count="` + strconv.Itoa(len(eligible)) + `">` + "\n" +
		strings.Join(entries, "\n") + "\n</active-claims>"
}

// formatConfidence 格式化 confidence 为两位小数（对账 toFixed(2)）。
func formatConfidence(v float64) string {
	return strconv.FormatFloat(v, 'f', 2, 64)
}

// escapeXML 转义 XML 特殊字符。
//
// 对账 claims.ts 的 escapeXml。**顺序重要**：先 & 再其他。
func escapeXML(text string) string {
	s := strings.ReplaceAll(text, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}

// ClaimSnapshot 是 claim 快照（溶解即新生）。
type ClaimSnapshot struct {
	Version      int
	CreatedAt    int64
	LastEventSeq int64
	HasEventSeq  bool
	Claims       []ContextClaim
}

// CheckpointClaims 导出活跃 claim 快照。
//
// 对账 checkpointClaims：**只含 non-stale / non-quarantined / non-expired**。
// 溶解时丢弃已失效的信息。
func CheckpointClaims(claims []ContextClaim, now int64) ClaimSnapshot {
	alive := make([]ContextClaim, 0, len(claims))
	for _, c := range claims {
		if c.Status == StatusStale || c.Status == StatusQuarantined {
			continue
		}
		if c.ExpiresAt != 0 && c.ExpiresAt <= now {
			continue
		}
		alive = append(alive, c)
	}
	return ClaimSnapshot{Version: 1, CreatedAt: now, Claims: alive}
}

// LoadClaimSnapshot 从快照恢复 claim。
//
// 对账 loadClaimSnapshot：**version 不匹配返回空**（不报错——快照是尽力而为）。
// 恢复后所有 claim 的 lastUsedAt 更新为 now（标记「刚被唤醒」）。
func LoadClaimSnapshot(s ClaimSnapshot, now int64) []ContextClaim {
	if s.Version != 1 {
		return nil
	}
	out := make([]ContextClaim, 0, len(s.Claims))
	for _, c := range s.Claims {
		c.LastUsedAt = now
		out = append(out, c)
	}
	return out
}

// ClaimProposalFromAnchor 从 ledger anchor 派生 claim 提案。
//
// 对账 claimProposalFromAnchor。**映射规则**：
//
//	anchor.kind → claim.kind（见 kindFromAnchor）
//	scope 恒为 session
//	confidence 按 kind 分级（见 confidenceFromAnchor）
//	fitness = anchor.salience
//	evidence 单条，kind 按 actor 分（user → user_message，否则 assistant_message）
func ClaimProposalFromAnchor(anchor ContextAnchor, meta ClaimProposalMeta) ClaimProposal {
	evKind := EvidenceKind("assistant_message")
	if meta.Actor == "user" {
		evKind = "user_message"
	}
	return ClaimProposal{
		Kind:       kindFromAnchor(anchor),
		Scope:      ScopeSession,
		Text:       anchor.Text,
		Confidence: confidenceFromAnchor(anchor),
		Fitness:    anchor.Salience,
		Source: ClaimSource{
			Actor:     meta.Actor,
			SessionID: meta.SessionID,
			Turn:      meta.Turn,
			EventID:   meta.EventID,
		},
		Evidence: []EvidenceRef{{
			ID:        meta.EventID + ":anchor",
			Kind:      evKind,
			Summary:   anchor.Text,
			CreatedAt: meta.CreatedAt,
		}},
		CreatedAt: meta.CreatedAt,
		Tags:      []string{"anchor", anchor.Kind},
	}
}

// kindFromAnchor 映射 anchor kind → claim kind。
//
// 对账 kindFromAnchor：注意多个 anchor kind 折叠到同一 claim kind，
// 且**默认**（含 file / pending_task）落到 file_observation。
func kindFromAnchor(a ContextAnchor) ContextClaimKind {
	switch a.Kind {
	case "user_constraint":
		return ClaimUserConstraint
	case "user_preference":
		return ClaimUserPreference
	case "decision":
		return ClaimDecision
	case "verification":
		return ClaimVerificationFact
	case "error":
		return ClaimFailurePattern
	default:
		return ClaimFileObservation
	}
}

// confidenceFromAnchor 按 anchor kind 给 confidence 分级。
//
// 对账 confidenceFromAnchor：user_constraint 0.9 / verification 0.88 /
// decision 0.82 / **其余 0.7**。
func confidenceFromAnchor(a ContextAnchor) float64 {
	switch a.Kind {
	case "user_constraint":
		return 0.9
	case "decision":
		return 0.82
	case "verification":
		return 0.88
	default:
		return 0.7
	}
}

// 保留 encoding/json 引用（序列化事件流时用，当前仅占位避免未使用告警）。
var _ = json.Marshal
