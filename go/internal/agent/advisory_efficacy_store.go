package agent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// AdvisoryEfficacyStore 是 advisory 效能的**跨会话信息素**（JSONL 持久化）。
//
// 对账 TS 的 `context/advisory-efficacy-store.ts`。
//
// **解决的问题**：`AdvisoryReadback` 的 per-key 统计随会话死亡——习惯化每会话
// 冷启动、副驾闸门要求「决出样本 >= 10」意味着每个新会话前十几轮副驾必然沉睡、
// holdout 资格（送达 >= 3）也从零攒起。跨会话先验是**冷启动的主数据源**。
//
// **机制**：per-key 效能计数落 `<cwd>/.rivet/knowledge/advisory-efficacy.jsonl`
// （一行一 key），加载时按年龄 EWMA 衰减（半衰期 14 天）——陈旧数据自然让位
// 于新证据。会话中每 20 轮 + 会话结束以**增量**合并写回。
//
// **已知局限（TS 注释原文）**：per-key 聚合抹平会话类型差异——同一 advisory 在
// bugfix 会话可能有效、重构会话可能无效，EWMA 会把它们混在一起。session 类型
// 聚类会让复杂度暴涨，留待后续；**消费方不应把先验当会话内实测用**。
type AdvisoryEfficacyStore struct {
	dir      string
	path     string
	lockPath string
}

// EfficacyPrior 是单个 key 的效能先验（衰减后可为小数）。
//
// 对账 EfficacyPrior。
type EfficacyPrior struct {
	Key             string
	Delivered       float64
	Adopted         float64
	Ignored         float64
	ShadowHeld      float64
	ShadowSatisfied float64
	UpdatedAt       int64
}

// EfficacyDelta 是会话侧增量（整数计数，与 AdvisoryKeyStats 的持久化子集同构）。
//
// 对账 EfficacyDelta。**必须是自上次 flush 以来的增量**——重复提交同一累计值
// 会翻倍计数（调用方负责差分）。
type EfficacyDelta struct {
	Delivered       float64
	Adopted         float64
	Ignored         float64
	ShadowHeld      float64
	ShadowSatisfied float64
}

// 衰减与容量常量。
//
// 对账 TS 的 HALF_LIFE_MS / PRUNE_THRESHOLD / MAX_KEYS。
const (
	// EfficacyHalfLifeMs 是 EWMA 半衰期——14 天。
	EfficacyHalfLifeMs int64 = 14 * 24 * 60 * 60 * 1000
	// efficacyPruneThreshold 是剔除阈值：衰减后**各计数**全低于此值的 key
	// 从文件剔除（防无界增长）。
	efficacyPruneThreshold = 0.05
	// efficacyMaxKeys 是文件最多保留的 key 数（按 delivered+shadowHeld 降序）。
	efficacyMaxKeys = 200
	// 锁重试参数（对账 LOCK_RETRY_MAX_MS / LOCK_RETRY_INTERVAL_MS）。
	efficacyLockRetryMaxMs      = 500
	efficacyLockRetryIntervalMs = 20
)

// efficacyCounterFields 是可衰减计数字段名（对账 COUNTER_FIELDS）。
//
// **顺序即 JSON 序列化顺序**——逐字节对账依赖它。
var efficacyCounterFields = []string{"delivered", "adopted", "ignored", "shadowHeld", "shadowSatisfied"}

// efficacyDecayFactor 计算衰减因子。
//
// 对账 decayFactor：age <= 0 返回 1（**未来时间戳不放大**），否则 0.5^(age/halfLife)。
func efficacyDecayFactor(ageMs int64) float64 {
	if ageMs <= 0 {
		return 1
	}
	return math.Pow(0.5, float64(ageMs)/float64(EfficacyHalfLifeMs))
}

// efficacyRound3 保留 3 位小数（对账 round3）。
func efficacyRound3(n float64) float64 {
	return math.Round(n*1000) / 1000
}

// NewAdvisoryEfficacyStore 构造 store（路径对账 TS 构造函数）。
func NewAdvisoryEfficacyStore(cwd string) *AdvisoryEfficacyStore {
	dir := filepath.Join(cwd, ".rivet", "knowledge")
	return &AdvisoryEfficacyStore{
		dir:      dir,
		path:     filepath.Join(dir, "advisory-efficacy.jsonl"),
		lockPath: filepath.Join(dir, "advisory-efficacy.jsonl.lock"),
	}
}

// Path 返回持久化文件路径（测试与诊断用）。
func (s *AdvisoryEfficacyStore) Path() string { return s.path }

// decayedPrior 返回按年龄衰减后的先验（updatedAt 重置为 now）。
func decayedPrior(p EfficacyPrior, now int64) EfficacyPrior {
	f := efficacyDecayFactor(now - p.UpdatedAt)
	return EfficacyPrior{
		Key:             p.Key,
		Delivered:       p.Delivered * f,
		Adopted:         p.Adopted * f,
		Ignored:         p.Ignored * f,
		ShadowHeld:      p.ShadowHeld * f,
		ShadowSatisfied: p.ShadowSatisfied * f,
		UpdatedAt:       now,
	}
}

// allBelowPruneThreshold 判断是否所有计数都低于剔除阈值。
func allBelowPruneThreshold(p EfficacyPrior) bool {
	return p.Delivered < efficacyPruneThreshold &&
		p.Adopted < efficacyPruneThreshold &&
		p.Ignored < efficacyPruneThreshold &&
		p.ShadowHeld < efficacyPruneThreshold &&
		p.ShadowSatisfied < efficacyPruneThreshold
}

// anyAtOrAbovePruneThreshold 判断是否有任一计数达标（mergeAndSave 的保留判据）。
func anyAtOrAbovePruneThreshold(p EfficacyPrior) bool {
	return p.Delivered >= efficacyPruneThreshold ||
		p.Adopted >= efficacyPruneThreshold ||
		p.Ignored >= efficacyPruneThreshold ||
		p.ShadowHeld >= efficacyPruneThreshold ||
		p.ShadowSatisfied >= efficacyPruneThreshold
}

// Load 加载并按年龄衰减——**会话启动时调用一次**，结果喂 `AdvisoryReadback.SeedPriors`。
//
// 对账 load：解析文件 → 逐条衰减 → 剔除衰减殆尽的 key。
//
// 文件不存在返回空（不是错误——首次运行或已清理）。
func (s *AdvisoryEfficacyStore) Load(now int64) map[string]EfficacyPrior {
	out := map[string]EfficacyPrior{}
	for key, prior := range s.parseFile() {
		d := decayedPrior(prior, now)
		// **load 的剔除条件是「全部 < 阈值」**（对账 `COUNTER_FIELDS.every(...)`）。
		if allBelowPruneThreshold(d) {
			continue
		}
		out[key] = d
	}
	return out
}

// parseFile 解析 JSONL 文件（损坏行跳过，不炸）。
func (s *AdvisoryEfficacyStore) parseFile() map[string]EfficacyPrior {
	out := map[string]EfficacyPrior{}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var p struct {
			Key             string   `json:"key"`
			Delivered       *float64 `json:"delivered"`
			Adopted         *float64 `json:"adopted"`
			Ignored         *float64 `json:"ignored"`
			ShadowHeld      *float64 `json:"shadowHeld"`
			ShadowSatisfied *float64 `json:"shadowSatisfied"`
			UpdatedAt       *int64   `json:"updatedAt"`
		}
		if err := json.Unmarshal([]byte(line), &p); err != nil {
			continue // 损坏行跳过（对账 `catch { /* skip malformed lines */ }`）
		}
		// 必填字段校验（对账 `typeof p.key !== 'string' || typeof p.updatedAt !== 'number'`）
		if p.Key == "" || p.UpdatedAt == nil {
			continue
		}
		out[p.Key] = EfficacyPrior{
			Key:             p.Key,
			Delivered:       derefFloat(p.Delivered),
			Adopted:         derefFloat(p.Adopted),
			Ignored:         derefFloat(p.Ignored),
			ShadowHeld:      derefFloat(p.ShadowHeld),
			ShadowSatisfied: derefFloat(p.ShadowSatisfied),
			UpdatedAt:       *p.UpdatedAt,
		}
	}
	return out
}

func derefFloat(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// MergeAndSave 增量合并写回。
//
// 对账 mergeAndSave。**读最新文件**（其他会话可能已写）→ 衰减到 now →
// 叠加本会话增量 → 剔除衰减殆尽的 key → 按 (delivered+shadowHeld) 降序截到
// MAX_KEYS → 原子写。
//
// **deltas 必须是自上次 flush 以来的增量**——调用方负责差分。
//
// 无变化（所有 delta 全零）时不落盘（**文件都不创建**）。
func (s *AdvisoryEfficacyStore) MergeAndSave(deltas map[string]EfficacyDelta, now int64) error {
	if !hasAnyDelta(deltas) {
		return nil
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("建 knowledge 目录失败：%w", err)
	}

	release := s.acquireLock()
	defer release()

	merged := map[string]EfficacyPrior{}
	for key, prior := range s.parseFile() {
		merged[key] = decayedPrior(prior, now)
	}
	for key, delta := range deltas {
		base, ok := merged[key]
		if !ok {
			base = EfficacyPrior{Key: key, UpdatedAt: now}
		}
		base.Delivered += delta.Delivered
		base.Adopted += delta.Adopted
		base.Ignored += delta.Ignored
		base.ShadowHeld += delta.ShadowHeld
		base.ShadowSatisfied += delta.ShadowSatisfied
		base.UpdatedAt = now
		merged[key] = base
	}

	kept := make([]EfficacyPrior, 0, len(merged))
	for _, p := range merged {
		// **mergeAndSave 的保留条件是「有任一 >= 阈值」**（对账 `some(...)`）——
		// 与 load 的剔除条件逻辑等价。
		if anyAtOrAbovePruneThreshold(p) {
			kept = append(kept, p)
		}
	}
	// 按 (delivered + shadowHeld) 降序；平手时按 key 升序保证确定性
	// （JS 的 Array.sort 在现代引擎是稳定的，插入序即 map 迭代序——Go 的
	// map 迭代随机，必须显式定序才能可复现）。
	sort.Slice(kept, func(i, j int) bool {
		si := kept[i].Delivered + kept[i].ShadowHeld
		sj := kept[j].Delivered + kept[j].ShadowHeld
		if si != sj {
			return si > sj
		}
		return kept[i].Key < kept[j].Key
	})
	if len(kept) > efficacyMaxKeys {
		kept = kept[:efficacyMaxKeys]
	}

	lines := make([]string, 0, len(kept))
	for _, p := range kept {
		lines = append(lines, serializeEfficacyPrior(p))
	}
	content := ""
	if len(lines) > 0 {
		content = strings.Join(lines, "\n") + "\n"
	}
	return s.atomicWrite(content)
}

// serializeEfficacyPrior 手工序列化——**字段序与 TS 的 JSON.stringify 一致**。
//
// 不能用 encoding/json 的 struct tag：Go 的 map/struct 序列化顺序虽由字段
// 声明序决定，但数字格式化规则不同（TS 的 `0.5` vs Go 的 `0.5` 一致，
// 但整数值 TS 输出 `8` 而 Go 的 float64 也输出 `8`——**这层恰好一致**。
// 真正的风险在 round3 后的精度表示，故显式用 strconv 控制。
func serializeEfficacyPrior(p EfficacyPrior) string {
	var b strings.Builder
	b.WriteString(`{"key":`)
	b.WriteString(jsonString(p.Key))
	b.WriteString(`,"delivered":`)
	b.WriteString(jsonNumber(efficacyRound3(p.Delivered)))
	b.WriteString(`,"adopted":`)
	b.WriteString(jsonNumber(efficacyRound3(p.Adopted)))
	b.WriteString(`,"ignored":`)
	b.WriteString(jsonNumber(efficacyRound3(p.Ignored)))
	b.WriteString(`,"shadowHeld":`)
	b.WriteString(jsonNumber(efficacyRound3(p.ShadowHeld)))
	b.WriteString(`,"shadowSatisfied":`)
	b.WriteString(jsonNumber(efficacyRound3(p.ShadowSatisfied)))
	b.WriteString(`,"updatedAt":`)
	b.WriteString(strconv.FormatInt(p.UpdatedAt, 10))
	b.WriteString(`}`)
	return b.String()
}

// jsonString 序列化字符串（对账 JSON.stringify 的转义规则）。
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// jsonNumber 序列化数字——整数值不带小数点（对账 JSON.stringify(3) === "3"）。
func jsonNumber(f float64) string {
	if f == math.Trunc(f) && !math.IsInf(f, 0) && math.Abs(f) < 1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// hasAnyDelta 判断增量是否非零（对账 `COUNTER_FIELDS.some(f => d[f] > 0)`）。
func hasAnyDelta(deltas map[string]EfficacyDelta) bool {
	for _, d := range deltas {
		if d.Delivered > 0 || d.Adopted > 0 || d.Ignored > 0 ||
			d.ShadowHeld > 0 || d.ShadowSatisfied > 0 {
			return true
		}
	}
	return false
}

// atomicWrite 原子写：写临时文件 + rename。
//
// 对账 atomicWrite（Node 的 writeFileSync + renameSync）。rename 在同文件系统内
// 是原子的——读者要么看到旧内容、要么看到新内容，不会看到半截。
func (s *AdvisoryEfficacyStore) atomicWrite(content string) error {
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return fmt.Errorf("生成临时文件名失败：%w", err)
	}
	tmp := filepath.Join(s.dir, ".efficacy."+hex.EncodeToString(suffix)+".tmp")
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return fmt.Errorf("写临时文件失败：%w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp) // 清理残留，避免污染目录
		return fmt.Errorf("原子替换失败：%w", err)
	}
	return nil
}

// acquireLock 获取文件锁（O_CREAT|O_EXCL + 重试）。
//
// 对账 acquireLock。**多会话共享 cwd 必须**——并发 mergeAndSave 会互相覆盖。
//
// **与 TS 的差异**：TS 在等待时忙等（`while (Date.now() < waitUntil)`），
// Go 用 time.Sleep（同一语义，不烧 CPU）。超时后返回**空释放函数**——
// 对账 TS 的 `return () => {}`（**放弃锁但不阻塞**，宽容降级而非 fail-closed：
// efficacy 是尽力而为的信息素，不值得因锁竞争阻断会话）。
//
// **已知局限**：超时降级意味着极端竞争下可能丢一次写。对账 TS 同样如此。
func (s *AdvisoryEfficacyStore) acquireLock() func() {
	deadline := time.Now().Add(time.Duration(efficacyLockRetryMaxMs) * time.Millisecond)
	for {
		f, err := os.OpenFile(s.lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_, _ = f.WriteString(strconv.Itoa(os.Getpid()))
			_ = f.Close()
			return func() { _ = os.Remove(s.lockPath) }
		}
		if time.Now().After(deadline) {
			return func() {} // 超时：放弃锁，宽容降级
		}
		time.Sleep(time.Duration(efficacyLockRetryIntervalMs) * time.Millisecond)
	}
}
