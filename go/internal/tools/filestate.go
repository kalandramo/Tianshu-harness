package tools

// filestate.go —— 本会话观察到的文件状态（mtime + size）。
//
// 对账 TS `src/tools/read-file.ts` 的 `lastKnownFileState` + `getFileReadMtime`
// + `noteFileObserved`。
//
// # 作用
//
// read_section 的 `file_path` 分支用它做**陈旧性检查**：若文件自上次
// read_file 后变更过（mtime 不符），返回内容前必须告警——否则模型会把
// 「当前磁盘版本」误当成「上文读到的版本」，在已变更的文件上做错误推断。
//
// # 与 editfail.go 的关系
//
// 复用同一 `canonicalPathKey`（Windows 上 POSIX 化 + 小写）与同一并发模式
// （`sync.Mutex` + 上限裁剪）——两者都是「按规范化路径索引的会话级小表」。

import (
	"sync"
)

// knownFileState 是本会话观察到的一个文件状态。
//
// 对账 TS `KnownFileState`。
type knownFileState struct {
	mtimeMs   int64
	sizeBytes int64
}

// lastKnownMax 是表的条目上限（对账 TS 的 `LAST_KNOWN_MAX`）。
const lastKnownMax = 500

var (
	lastKnownMu    sync.Mutex
	lastKnownState = map[string]knownFileState{}
	// lastKnownOrder 记录插入序，用于超限时裁剪最旧的（对账 TS 的 trimLastKnown）。
	lastKnownOrder []string
)

// fileHistoryKey 对账 TS `fileHistoryKey`：`${sessionId ?? ”}::${canonicalPathKey}`。
//
// **sessionId 参与键**：同一进程内不同会话对同一文件的观察必须隔离，
// 否则 A 会话的读会让 B 会话的陈旧检查误判。
func fileHistoryKey(sessionID, canonicalPath string) string {
	return sessionID + "::" + canonicalPathKey(canonicalPath)
}

// NoteFileObserved 记录本会话刚观察到的文件状态（读或写之后调用）。
//
// 对账 TS `noteFileObserved`。**用途**：编辑成功后调用方把写入后的 mtime
// 记进来，让**下一次**编辑的陈旧检查不会误报我们自己刚做的写入
// （read-edit-stale 循环防护）。
func NoteFileObserved(canonicalPath string, mtimeMs, sizeBytes int64, sessionID string) {
	key := fileHistoryKey(sessionID, canonicalPath)
	lastKnownMu.Lock()
	defer lastKnownMu.Unlock()
	if _, exists := lastKnownState[key]; !exists {
		lastKnownOrder = append(lastKnownOrder, key)
	}
	lastKnownState[key] = knownFileState{mtimeMs: mtimeMs, sizeBytes: sizeBytes}
	trimLastKnownLocked()
}

// GetFileReadMtime 返回本会话上次观察到的 mtime（毫秒）；从未观察过返回
// (0, false)。
//
// 对账 TS `getFileReadMtime`（TS 返回 `number | null`）。
func GetFileReadMtime(canonicalPath, sessionID string) (int64, bool) {
	key := fileHistoryKey(sessionID, canonicalPath)
	lastKnownMu.Lock()
	defer lastKnownMu.Unlock()
	st, ok := lastKnownState[key]
	if !ok {
		return 0, false
	}
	return st.mtimeMs, true
}

// ForgetFileState 删除某文件的本会话状态（对账 TS 的 `lastKnownFileState.delete`）。
func ForgetFileState(canonicalPath, sessionID string) {
	key := fileHistoryKey(sessionID, canonicalPath)
	lastKnownMu.Lock()
	defer lastKnownMu.Unlock()
	delete(lastKnownState, key)
	for i, k := range lastKnownOrder {
		if k == key {
			lastKnownOrder = append(lastKnownOrder[:i], lastKnownOrder[i+1:]...)
			break
		}
	}
}

// trimLastKnownLocked 超限时裁掉最旧的 **20%**（须持锁）。
//
// 对账 TS `trimLastKnown`：`drop = Math.ceil(size * 0.2)`——**裁 20% 而非裁到上限**。
//
// **此前的不等价（第二十八刀修正）**：Go 裁 `size - max` 条（501 → 500），
// TS 裁 `ceil(size*0.2)` 条（501 → 400）。差异的后果：Go 每次只裁 1 条，
// 表容量长期贴着上限反复触发裁剪；TS 裁完留出 20% 余量。
//
// 与 `trimReadHistoryLocked`（readdedup.go）用**同一算式**——两表语义一致。
func trimLastKnownLocked() {
	if len(lastKnownOrder) <= lastKnownMax {
		return
	}
	drop := (len(lastKnownOrder) + 4) / 5 // ceil(size * 0.2)
	for _, k := range lastKnownOrder[:drop] {
		delete(lastKnownState, k)
	}
	lastKnownOrder = lastKnownOrder[drop:]
}

// ResetFileStateForTests 清空表（测试用）。
func ResetFileStateForTests() {
	lastKnownMu.Lock()
	defer lastKnownMu.Unlock()
	lastKnownState = map[string]knownFileState{}
	lastKnownOrder = nil
}
