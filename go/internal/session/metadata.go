package session

import (
	"encoding/json"
	"os"
	"time"

	"github.com/kalandramo/tianshu/go/internal/api/wire"
)

// SessionMetadata 是会话元数据。
//
// 对账 src/context/types.ts 的 SessionMetadata。
//
// **只声明核心字段**：`compactEvents` 与 `lastLedger` 是复杂嵌套类型
// （压缩事件 / 上下文账本），它们的语义属于别的模块，此处**透传**不解析
// ——元数据存储的职责是合并与落盘，不是理解它们。
type SessionMetadata struct {
	SessionID     string
	CreatedAt     int64
	UpdatedAt     int64
	CompactEvents []any
	LastLedger    any
	Model         string
	Provider      string
	TokenUsage    *TokenUsage
	Title         string
	Status        string
	TurnCount     int
	ToolCallCount int
	// PresentKeys 是**write 路径**的键集（按序）。
	//
	// 为什么需要：TS 的 `write` 直接 `stringify(调用方对象)`——输出的键集
	// **完全等于调用方构造的键集**。Go 侧无法从结构体推断「哪些键被显式
	// 设置过」（零值 vs 缺失不可区分），故 write 调用方须显式给出键序。
	// 为 nil 时 write 走「核心字段全输出」的退化路径。
	//
	// **update 路径不用它**——那条路径的键序由合并逻辑固定。
	PresentKeys []string
	// 其余字段（如 worker 失败归因）透传
	Extra map[string]any
}

// TokenUsage 是累计 token 用量。
type TokenUsage struct {
	Prompt     int
	Completion int
	Total      int
}

// MetadataStore 是会话元数据存储（内存缓存 + 批量落盘节拍）。
//
// 对账 SessionMetadataStore（85 行）。设计动机：append 热路径每条消息都更新
// 元数据，每次写整个 meta.json 是读写放大热点。更新留在内存、搭 transcript
// 的批量 flush 节拍（~200ms）；init/write 路径保持同步持久。
type MetadataStore struct {
	metadataPath string

	// cached 三态（对账 TS 的 null/undefined/对象）：
	//   loaded == false         → 未加载（TS 的 null）
	//   loaded == true && data == nil → 磁盘无文件（TS 的 undefined）
	//   loaded == true && data != nil → 已加载
	loaded bool
	data   *SessionMetadata
	dirty  bool

	// now 可注入时钟（测试用）
	now func() int64
}

// NewMetadataStore 构造元数据存储。
func NewMetadataStore(metadataPath string) *MetadataStore {
	return &MetadataStore{
		metadataPath: metadataPath,
		now:          func() int64 { return time.Now().UnixMilli() },
	}
}

// Load 读取元数据（带内存缓存）。
//
// 对账 load：首次读盘并缓存；文件不存在或解析失败都缓存为 nil（避免反复读盘）。
func (s *MetadataStore) Load() *SessionMetadata {
	if s.loaded {
		return s.data
	}
	s.loaded = true
	raw, err := os.ReadFile(s.metadataPath)
	if err != nil {
		s.data = nil
		return nil
	}
	var m SessionMetadata
	if err := json.Unmarshal(raw, &m); err != nil {
		s.data = nil
		return nil
	}
	s.data = &m
	return s.data
}

// Write 同步持久写入（initMetadata / 外部写入方用）。
//
// 对账 write：**2 空格缩进 + 尾换行**。
func (s *MetadataStore) Write(metadata *SessionMetadata) error {
	s.loaded = true
	s.data = metadata
	s.dirty = false
	return writeFileAtomic(s.metadataPath, marshalMetadataIndented(metadata, false))
}

// Update 只在内存里 upsert 字段。
//
// 对账 update。**合并语义**（优先级从低到高）：
//  1. `compactEvents` 默认空数组
//  2. 既有值（existing）
//  3. patch 的值
//  4. **sessionId 由调用方权威**（覆盖前面）
//  5. `createdAt` 只在首次设置，之后保留
//  6. `updatedAt` 总是推进到当前时间
//  7. `tokenUsage` **嵌套合并**而非替换
//
// 落盘搭批量 flush 节拍（此处不写盘）。
func (s *MetadataStore) Update(patch *SessionMetadata) {
	existing := s.Load()

	merged := &SessionMetadata{}
	if existing != nil {
		*merged = *existing
		if existing.CompactEvents == nil {
			merged.CompactEvents = []any{}
		}
		if existing.TokenUsage != nil {
			tu := *existing.TokenUsage
			merged.TokenUsage = &tu
		}
	} else {
		merged.CompactEvents = []any{}
	}

	// patch 覆盖（仅非零字段——对账 TS 的展开语义：patch 里显式存在的键才覆盖）
	applyPatch(merged, patch)

	// 以下三项**最后**赋值，覆盖前面（对账 TS 的注释：These must win）
	if patch.SessionID != "" {
		merged.SessionID = patch.SessionID
	} else if existing != nil {
		merged.SessionID = existing.SessionID
	}
	if existing != nil {
		merged.CreatedAt = existing.CreatedAt
	} else {
		merged.CreatedAt = s.now()
	}
	merged.UpdatedAt = s.now()

	// tokenUsage 嵌套合并
	if existing != nil && existing.TokenUsage != nil || patch.TokenUsage != nil {
		tu := TokenUsage{}
		if existing != nil && existing.TokenUsage != nil {
			tu = *existing.TokenUsage
		}
		if patch.TokenUsage != nil {
			if patch.TokenUsage.Prompt != 0 {
				tu.Prompt = patch.TokenUsage.Prompt
			}
			if patch.TokenUsage.Completion != 0 {
				tu.Completion = patch.TokenUsage.Completion
			}
			if patch.TokenUsage.Total != 0 {
				tu.Total = patch.TokenUsage.Total
			}
		}
		merged.TokenUsage = &tu
	}

	s.loaded = true
	s.data = merged
	s.dirty = true
}

// applyPatch 把 patch 的非零字段覆盖到 target。
//
// 对账 TS 的 `...patch` 展开——TS 里 patch 的**存在键**才覆盖（值可以是
// undefined 从而显式清空）。Go 侧用「非零值才覆盖」近似：对字符串/数字
// 语义等价（TS 侧这几个字段正常路径不会传空串/0）。
func applyPatch(target, patch *SessionMetadata) {
	if patch == nil {
		return
	}
	if patch.Model != "" {
		target.Model = patch.Model
	}
	if patch.Provider != "" {
		target.Provider = patch.Provider
	}
	if patch.Title != "" {
		target.Title = patch.Title
	}
	if patch.Status != "" {
		target.Status = patch.Status
	}
	if patch.CompactEvents != nil {
		target.CompactEvents = patch.CompactEvents
	}
	if patch.LastLedger != nil {
		target.LastLedger = patch.LastLedger
	}
	if patch.Extra != nil {
		if target.Extra == nil {
			target.Extra = map[string]any{}
		}
		for k, v := range patch.Extra {
			target.Extra[k] = v
		}
	}
}

// Flush 在 dirty 时持久化内存元数据（批量 flush 节拍）。
//
// 对账 flush。**失败保持 dirty**——对账 TS 注释：写失败时清 dirty 会让自上次
// 成功 flush 以来的所有元数据更新静默丢失。
func (s *MetadataStore) Flush() error {
	if !s.dirty {
		return nil
	}
	var m *SessionMetadata
	if s.data != nil {
		m = s.data
	} else {
		m = &SessionMetadata{}
	}
	if err := writeFileAtomic(s.metadataPath, marshalMetadataIndented(m, true)); err != nil {
		s.dirty = true // 保持 dirty，下次重试
		return err
	}
	s.dirty = false
	return nil
}

// marshalMetadataIndented 按 TS 的 `JSON.stringify(m, null, 2) + '\n'` 序列化。
//
// 键序对账 TS 对象的插入序：sessionId → createdAt → updatedAt → compactEvents
// → 其余可选字段。**注意 TS 的 write 直接 stringify 传入对象**，键序取决于
// 调用方构造；此处用固定序（核心字段在前），与生产路径的构造序一致。
func marshalMetadataIndented(m *SessionMetadata, compactFirst bool) []byte {
	om := wire.NewOrderedMap()
	// **键序分两条路径**（oracle 锁定）：
	//   - update 路径（走合并构造）：compactEvents 最先——TS 的对象字面量里
	//     它最先声明，`...existing` 展开不改变已有键的位置
	//   - write 路径（直接 stringify 调用方对象）：键序 = **调用方构造序**，
	//     此处复刻生产路径的 initMetadata 字面量序
	//     （src/agent/session-persist.ts:519-529）
	if compactFirst {
		if m.CompactEvents != nil {
			om.Set("compactEvents", m.CompactEvents)
		}
		om.Set("sessionId", m.SessionID)
		om.Set("createdAt", m.CreatedAt)
		om.Set("updatedAt", m.UpdatedAt)
		if m.LastLedger != nil {
			om.Set("lastLedger", m.LastLedger)
		}
		if m.Model != "" {
			om.Set("model", m.Model)
		}
		if m.Provider != "" {
			om.Set("provider", m.Provider)
		}
		if m.TokenUsage != nil {
			om.Set("tokenUsage", tokenUsageWire(m.TokenUsage))
		}
		if m.Title != "" {
			om.Set("title", m.Title)
		}
		if m.Status != "" {
			om.Set("status", m.Status)
		}
	} else {
		// write 路径：输出的键集**精确等于调用方的键集**（对账 TS 的
		// `stringify(调用方对象)`）。按 PresentKeys 给定的序输出——
		// 未在其中的键**不输出**（即使字段有值）。
		keys := m.PresentKeys
		if keys == nil {
			// 退化：核心字段全输出
			keys = []string{"sessionId", "createdAt", "updatedAt", "compactEvents"}
		}
		for _, k := range keys {
			switch k {
			case "sessionId":
				om.Set("sessionId", m.SessionID)
			case "createdAt":
				om.Set("createdAt", m.CreatedAt)
			case "updatedAt":
				om.Set("updatedAt", m.UpdatedAt)
			case "compactEvents":
				om.Set("compactEvents", m.CompactEvents)
			case "status":
				om.Set("status", m.Status)
			case "turnCount":
				om.Set("turnCount", m.TurnCount)
			case "toolCallCount":
				om.Set("toolCallCount", m.ToolCallCount)
			case "tokenUsage":
				if m.TokenUsage != nil {
					om.Set("tokenUsage", tokenUsageWire(m.TokenUsage))
				}
			case "model":
				om.Set("model", m.Model)
			case "provider":
				om.Set("provider", m.Provider)
			case "title":
				om.Set("title", m.Title)
			case "lastLedger":
				if m.LastLedger != nil {
					om.Set("lastLedger", m.LastLedger)
				}
			}
		}
	}
	for k, v := range m.Extra {
		om.Set(k, v)
	}
	return []byte(indentJSON(om.Marshal(), 2) + "\n")
}

func tokenUsageWire(tu *TokenUsage) *wire.OrderedMap {
	m := wire.NewOrderedMap()
	m.Set("prompt", tu.Prompt)
	m.Set("completion", tu.Completion)
	m.Set("total", tu.Total)
	return m
}

// indentJSON 把紧凑 JSON 按 2 空格缩进（对账 JSON.stringify(x, null, 2)）。
func indentJSON(compact string, indent int) string {
	var out []byte
	depth := 0
	inStr := false
	esc := false
	for i := 0; i < len(compact); i++ {
		c := compact[i]
		if inStr {
			out = append(out, c)
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
			out = append(out, c)
		case '{', '[':
			out = append(out, c)
			depth++
			if i+1 < len(compact) && compact[i+1] != '}' && compact[i+1] != ']' {
				out = append(out, '\n')
				out = appendIndent(out, depth, indent)
			}
		case '}', ']':
			depth--
			if i > 0 && compact[i-1] != '{' && compact[i-1] != '[' {
				out = append(out, '\n')
				out = appendIndent(out, depth, indent)
			}
			out = append(out, c)
		case ',':
			out = append(out, c, '\n')
			out = appendIndent(out, depth, indent)
		case ':':
			out = append(out, ':', ' ')
		default:
			out = append(out, c)
		}
	}
	return string(out)
}

func appendIndent(b []byte, depth, n int) []byte {
	for i := 0; i < depth*n; i++ {
		b = append(b, ' ')
	}
	return b
}
