package plan

import "time"

// nowISO 返回当前时间的 ISO-8601 字符串（对账 JS 的 `new Date().toISOString()`）。
//
// **格式必须与 JS 逐字一致**：`2026-09-22T05:12:07.169Z`——毫秒 3 位、
// 末尾 `Z`。Go 的 `time.RFC3339Nano` 会省略尾随零且精度可变，**不能用**。
//
// 该字符串进计划文件的 `> **Status: ...** — <ts>` 行——它是**文件内容**，
// 会影响前缀缓存稳定性，故格式必须确定。
func nowISO() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}
