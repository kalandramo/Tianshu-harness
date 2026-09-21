package tools

// readdedup.go —— 读去重（表1 双表 + 重复读判定 + read-ref 引用化）。
//
// 对账 TS `src/tools/read-file.ts` 的 `readHistory` / `fileReadHistory` /
// `isUnchangedRepeatRead` / `invalidateReadHistory` / `invalidateSessionReadDedup`
// 及 read-ref 分支（`:862-908`）。
//
// # 为什么需要它
//
// TS 侧 `RIVET_READ_REF` **默认开启**（`!== '0'`）——同一文件在未变更时重复读，
// TS 返回**紧凑引用**而非全文（省掉一次 cacheCreate）。Go 侧此前既无表也无引用，
// **总是重发全文**——这已是可观测的行为分歧，不只是"少个优化"。
//
// # 两张表的分工（对账 TS 注释）
//
//   - 表1a `readHistory`：**按切片**（sessionId + cwd + path + offset + limit）。
//     「模型实际读过的内容」。编辑会删其条目（`InvalidateReadHistory`），
//     使 read-ref 永不能对着**编辑前**的内容声称"未变"。
//   - 表1b `fileReadHistory`：**按文件**（sessionId + path，无 offset/limit）。
//     整文件读的记录，用于让**分片**读不必再读盘即可判定"已读全文件"。
//
// 两表**独立裁剪**（500 / 200）——故 read-ref 命中后必须**重登记表2**
// （`NoteFileObserved`）：表2 可能已淘汰该条目而表1 仍在，否则编辑工具的
// "先读再改"指引会死循环（读了被 read-ref 短路 → 表2 仍空 → 仍被拒）。
//
// # 与 filestate.go（表2）的关系
//
// 表2（`lastKnownFileState`）记录**文件状态**，读写都写；表1 只由**读**路径写，
// 编辑**删**。语义不同，故必须分开——合并会让 read-ref 对着编辑前内容说"未变"。

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// readHistoryEntry 是表1a 的一条记录（按切片）。
//
// 对账 TS `ReadHistoryEntry`。
type readHistoryEntry struct {
	mtimeMs    int64
	sizeBytes  int64
	rawBytes   int
	modelBytes int
	truncated  bool
	recordedAt int64
	// artifactID 是原读取时落盘的 artifact（当前 Go 侧未接 re-serve——
	// `sliceFromArtifact` 未移植；字段先留以保持记录结构对账）。
	artifactID string
	// refServed 是**已对该条目发放过多少次 read-ref 引用**。
	//
	// 模型在已收到引用后**又回来要同一未变更切片**，说明引用**没起作用**
	// （其"回看上文"目标可能已被裁剪/压缩出请求视图）——此时降级为真实读取，
	// 而不是无限发引用。对账 TS 的 degrade gate。
	refServed int
}

// fileReadHistoryEntry 是表1b 的一条记录（按文件，仅整文件读）。
//
// 对账 TS `FileReadHistoryEntry`。
type fileReadHistoryEntry struct {
	mtimeMs    int64
	sizeBytes  int64
	totalLines int
	rawBytes   int
	modelBytes int
	artifactID string
	recordedAt int64
	refServed  int
}

const (
	// readHistoryMax 对账 TS `READ_HISTORY_MAX`。
	readHistoryMax = 500
	// fileReadHistoryMax 对账 TS `FILE_READ_HISTORY_MAX`。
	fileReadHistoryMax = 200
	// readRefThreshold 是 read-ref 生效的最小 modelBytes。
	//
	// 对账 TS `READ_REF_THRESHOLD`——更小的重复读仍直接返回内容，
	// 避免为极小片段浪费一次往返。
	readRefThreshold = 2048
)

var (
	readHistoryMu     sync.Mutex
	readHistory       = map[string]readHistoryEntry{}
	readHistoryOrder  []string
	fileReadHistMu    sync.Mutex
	fileReadHistory   = map[string]fileReadHistoryEntry{}
	fileReadHistOrder []string

	// readRefSavedBytes / readRefCount 是进程级 telemetry 兜底
	// （未注入 per-session 累加器时用）。对账 TS 的同名模块级变量。
	readRefStatsMu  sync.Mutex
	readRefSaved    int64
	readRefExecuted int64
)

// ReadRefStats 是 per-session 的 read-ref telemetry（注入时优先于进程级兜底）。
//
// 对账 TS `ReadRefStats`。**为什么要 per-session**：并发会话（fork/worker）
// 共享进程时，进程级累加会把别人的节省算进来。
type ReadRefStats struct {
	SavedBytes int64
	Count      int64
}

// IsReadRefEnabled 判定 read-ref 是否开启（**默认开**，`RIVET_READ_REF=0` 关）。
//
// 对账 TS `isReadRefEnabled`。**调用时判定**（非模块加载时），以便测试动态切换。
func IsReadRefEnabled() bool {
	return os.Getenv("RIVET_READ_REF") != "0"
}

// readHistoryKey 是表1a 的键：`sessionId::cwd::path::offset::limit`。
//
// 对账 TS `readHistoryKey`。`limKey` 由调用方按 TS 的 `limit ?? 'all'` 语义算好
// ——**注意显式 `limit: 0` 在 TS 里键是 `"0"`（`0 ?? 'all'` 得 0），不是 `"all"`**，
// 故不能用「limit <= 0 → all」近似（那会把两次不同的调用并成一条）。
func readHistoryKey(cwd, canonicalPath string, offset int, limKey, sessionID string) string {
	return sessionID + "::" + canonicalPathKey(cwd) + "::" + canonicalPathKey(canonicalPath) +
		"::" + strconv.Itoa(offset) + "::" + limKey
}

// IsUnchangedRepeatRead 判定「同一文件此前读过、且自那以来 mtime 与 size 都没变」。
//
// 对账 TS `isUnchangedRepeatRead`。两条命中路径：
//
//  1. **同切片**：表1a 命中且 mtime/size 一致。
//  2. **全文件包含**：表1b 命中且 mtime/size 一致，**且本次请求是整文件读**
//     （`offset === 1 && !limit`）——整文件读必然覆盖之前的任何读。
//
// **`limitFalsy` 而非 `limit`**：TS 的 `!limit` 是 JS falsy 语义——**缺失/null/0
// 为真，负数也为真**（`!(-5) === false`）。用 `limit <= 0` 近似会把负数误判为
// falsy。故由调用方按 JS 语义算好布尔再传入（见 `limitIsFalsy`）。
func IsUnchangedRepeatRead(canonical string, currentMtimeMs, currentSizeBytes int64,
	dedupKey string, offset int, limitFalsy bool, sessionID string) bool {

	readHistoryMu.Lock()
	priorSame, hasSame := readHistory[dedupKey]
	readHistoryMu.Unlock()
	if hasSame && priorSame.mtimeMs == currentMtimeMs && priorSame.sizeBytes == currentSizeBytes {
		return true
	}

	fileReadHistMu.Lock()
	fullPrior, hasFull := fileReadHistory[fileHistoryKey(sessionID, canonical)]
	fileReadHistMu.Unlock()
	if hasFull && fullPrior.mtimeMs == currentMtimeMs && fullPrior.sizeBytes == currentSizeBytes &&
		offset == 1 && limitFalsy {
		return true
	}
	return false
}

// lookupReadHistory 取表1a 条目（read-ref 分支用）。
func lookupReadHistory(dedupKey string) (readHistoryEntry, bool) {
	readHistoryMu.Lock()
	defer readHistoryMu.Unlock()
	e, ok := readHistory[dedupKey]
	return e, ok
}

// lookupFileReadHistory 取表1b 条目（read-ref 分支用）。
func lookupFileReadHistory(sessionID, canonical string) (fileReadHistoryEntry, bool) {
	fileReadHistMu.Lock()
	defer fileReadHistMu.Unlock()
	e, ok := fileReadHistory[fileHistoryKey(sessionID, canonical)]
	return e, ok
}

// RecordRead 写表1a（按切片）。对账 TS 的 `recordDedup` 闭包。
//
// `truncated` 判定：rawBytes != modelBytes（对账 TS 的
// `payload.rawContent.length !== payload.modelContent.length`）。
func RecordRead(dedupKey string, mtimeMs, sizeBytes int64, rawBytes, modelBytes int,
	artifactID, sessionID string) {

	if dedupKey == "" {
		return
	}
	readHistoryMu.Lock()
	defer readHistoryMu.Unlock()
	if _, exists := readHistory[dedupKey]; !exists {
		readHistoryOrder = append(readHistoryOrder, dedupKey)
	}
	readHistory[dedupKey] = readHistoryEntry{
		mtimeMs:    mtimeMs,
		sizeBytes:  sizeBytes,
		rawBytes:   rawBytes,
		modelBytes: modelBytes,
		truncated:  rawBytes != modelBytes,
		recordedAt: time.Now().UnixMilli(),
		artifactID: artifactID,
	}
	trimReadHistoryLocked()
}

// RecordFileRead 写表1b（按文件，**仅整文件读**）。对账 TS 的 `recordFileDedup`。
//
// **只记整文件读**：`offset == 1 && limit == 0`——分片读不构成"已读全文件"。
func RecordFileRead(canonical string, mtimeMs, sizeBytes int64, totalLines, rawBytes, modelBytes int,
	artifactID, sessionID string) {

	if canonical == "" {
		return
	}
	key := fileHistoryKey(sessionID, canonical)
	fileReadHistMu.Lock()
	defer fileReadHistMu.Unlock()
	if _, exists := fileReadHistory[key]; !exists {
		fileReadHistOrder = append(fileReadHistOrder, key)
	}
	fileReadHistory[key] = fileReadHistoryEntry{
		mtimeMs:    mtimeMs,
		sizeBytes:  sizeBytes,
		totalLines: totalLines,
		rawBytes:   rawBytes,
		modelBytes: modelBytes,
		artifactID: artifactID,
		recordedAt: time.Now().UnixMilli(),
	}
	trimFileReadHistoryLocked()
}

// InvalidateReadHistory 删除某路径的**全部**读去重记录（表1a + 表1b）。
//
// 对账 TS `invalidateReadHistory`。**调用时机**：编辑成功后——磁盘内容已不再
// 等于任何会话读过的内容，故"已读且未变"的声称与基于 artifact 的切片都必须失效。
//
// **跨会话删除是安全的**：那些条目本就会因 mtime+size 不符而失效。
//
// **键匹配用 canonicalPathKey 归一**：跨会话 file_changed 事件带来的路径可能与
// 建键时的大小写/分隔符不同（另一进程、另一种 shell），不归一会**静默失效**
// ——正是 Windows 上 read-ref 中毒的复发通道。
func InvalidateReadHistory(canonicalPath string) {
	keyPath := canonicalPathKey(canonicalPath)

	fileReadHistMu.Lock()
	suffix := "::" + keyPath
	for _, k := range fileReadHistOrder {
		if len(k) >= len(suffix) && k[len(k)-len(suffix):] == suffix {
			delete(fileReadHistory, k)
		}
	}
	fileReadHistMu.Unlock()

	// 表1a 的键在路径**之后**还有 offset/limit，故匹配路径段。
	readHistoryMu.Lock()
	segment := "::" + keyPath + "::"
	for _, k := range readHistoryOrder {
		if strings.Contains(k, segment) {
			delete(readHistory, k)
		}
	}
	readHistoryMu.Unlock()
}

// InvalidateSessionReadDedup 删除**某会话的全部**读去重记录（表1a + 表1b）。
//
// 对账 TS `invalidateSessionReadDedup`。**调用时机**：任何消息历史重写之后
// （压缩阶梯 / agent-diet / stale-round / heap micro-compact）——read-ref 指向的
// 历史 tool_result 可能已被删除/摘要化/替换为占位符，故"已读"声称必须随之消亡。
//
// **表2 与 sessionFileEdits 不受影响**：它们跟踪的是**文件状态**，不是历史存在性。
//
// 成本上界：一次重写历史（此时前缀缓存本已破裂）后每文件多一次真实读。
func InvalidateSessionReadDedup(sessionID string) {
	prefix := sessionID + "::"

	readHistoryMu.Lock()
	for _, k := range readHistoryOrder {
		if strings.HasPrefix(k, prefix) {
			delete(readHistory, k)
		}
	}
	readHistoryMu.Unlock()

	fileReadHistMu.Lock()
	for _, k := range fileReadHistOrder {
		if strings.HasPrefix(k, prefix) {
			delete(fileReadHistory, k)
		}
	}
	fileReadHistMu.Unlock()
}

// ReadRefTelemetry 返回进程级 read-ref telemetry（未注入 per-session 时的兜底）。
//
// 对账 TS `getReadRefStats`。
func ReadRefTelemetry() (savedBytes, count int64) {
	readRefStatsMu.Lock()
	defer readRefStatsMu.Unlock()
	return readRefSaved, readRefExecuted
}

// accumulateReadRef 累加一次 read-ref 的节省字节。
//
// **per-session 优先**（对账 TS）：注入 `stats` 时写它，否则写进程级兜底。
func accumulateReadRef(stats *ReadRefStats, entryBytes int) {
	if stats != nil {
		stats.SavedBytes += int64(entryBytes)
		stats.Count++
		return
	}
	readRefStatsMu.Lock()
	readRefSaved += int64(entryBytes)
	readRefExecuted++
	readRefStatsMu.Unlock()
}

// bumpRefServed 递增两表命中条目的 refServed 计数。
//
// 对账 TS：`if (matchedSame) matchedSame.refServed = (matchedSame.refServed ?? 0) + 1`
// 及其 fullPrior 对应行——两处**独立**递增（TS 用 optional chaining，
// 未命中则不增）。Go 的 map 值不是引用类型，故需写回。
func bumpRefServed(dedupKey, sessionID, canonical string) {
	readHistoryMu.Lock()
	if e, ok := readHistory[dedupKey]; ok {
		e.refServed++
		readHistory[dedupKey] = e
	}
	readHistoryMu.Unlock()

	fileReadHistMu.Lock()
	key := fileHistoryKey(sessionID, canonical)
	if e, ok := fileReadHistory[key]; ok {
		e.refServed++
		fileReadHistory[key] = e
	}
	fileReadHistMu.Unlock()
}

// readRefServedAtLeastOnce 判定该切片是否**已发放过引用**（degrade gate 前提）。
//
// 对账 TS：`Math.max(matchedSame?.refServed ?? 0, matchedFull?.refServed ?? 0) >= 1`。
func readRefServedAtLeastOnce(dedupKey, sessionID, canonical string) bool {
	readHistoryMu.Lock()
	same := readHistory[dedupKey]
	readHistoryMu.Unlock()

	fileReadHistMu.Lock()
	full := fileReadHistory[fileHistoryKey(sessionID, canonical)]
	fileReadHistMu.Unlock()

	m := same.refServed
	if full.refServed > m {
		m = full.refServed
	}
	return m >= 1
}

// trimReadHistoryLocked 超限时裁掉最旧的 **20%**（须持锁）。
//
// 对账 TS `trimReadHistory`：`drop = Math.ceil(size * 0.2)`——裁 20% 而非
// 裁到上限，避免每次写入都触发裁剪。
func trimReadHistoryLocked() {
	if len(readHistoryOrder) <= readHistoryMax {
		return
	}
	drop := (len(readHistoryOrder) + 4) / 5 // ceil(size * 0.2)
	for _, k := range readHistoryOrder[:drop] {
		delete(readHistory, k)
	}
	readHistoryOrder = readHistoryOrder[drop:]
}

// trimFileReadHistoryLocked 同上（表1b，上限 200）。
func trimFileReadHistoryLocked() {
	if len(fileReadHistOrder) <= fileReadHistoryMax {
		return
	}
	drop := (len(fileReadHistOrder) + 4) / 5
	for _, k := range fileReadHistOrder[:drop] {
		delete(fileReadHistory, k)
	}
	fileReadHistOrder = fileReadHistOrder[drop:]
}

// ResetReadDedupForTests 清空两表与 telemetry（测试用）。
//
// 对账 TS `__resetReadHistoryForTests`。
func ResetReadDedupForTests() {
	readHistoryMu.Lock()
	readHistory = map[string]readHistoryEntry{}
	readHistoryOrder = nil
	readHistoryMu.Unlock()

	fileReadHistMu.Lock()
	fileReadHistory = map[string]fileReadHistoryEntry{}
	fileReadHistOrder = nil
	fileReadHistMu.Unlock()

	readRefStatsMu.Lock()
	readRefSaved = 0
	readRefExecuted = 0
	readRefStatsMu.Unlock()
}
