package agent

// resourcesensor.go —— 内存压力比（`rssRatio`）探针。
//
// 对账 TS `src/agent/resource-sensor.ts` 的 `rssBytes / memoryLimitBytes` 算式，
// 以及 `turn-orchestrator.ts:590` 的 `rssRatio = snap ? rss/limit : 0`。
//
// # scope（明示收窄）
//
// TS `resource-sensor.ts` 是完整的**采样器**（环形缓冲、线性回归斜率、冷却样本、
// 心跳 tick）——那是独立子系统。本刀只做 `TurnBudget` 接线所需的**最小面**：
// 取当前内存压力比。
//
// # 与 TS 的差异（明示）
//
// TS 的 `memoryLimitBytes` 读 **V8 old-space ceiling**
// （`getHeapStatistics().heap_size_limit`），因为 Node 的崩溃点是 V8 堆而非 RSS。
// **Go 没有 V8**——故：
//
//  1. 优先读 `RIVET_MEMORY_LIMIT_BYTES`（**TS 也优先读它**，见
//     `resource-sensor.ts:48`）——这是两侧共有的显式配置通道。
//  2. 无该环境变量时回退到**进程 RSS 的软上限**：用 `runtime.MemStats.Sys`
//     （Go 向 OS 申请的总内存）作为分子、`RIVET_MEMORY_LIMIT_BYTES` 的默认值
//     （1 GiB，对账 TS 的 `defaultMemoryLimitBytes` 回退）作为分母。
//
// **这不是逐字节等价**（V8 堆 ≠ Go Sys），但**保留了语义**：比值越高越接近
// 资源上限，驱动同一组阈值（0.7 / 0.85）。已在 HANDOFF 记明。

import (
	"os"
	"runtime"
	"strconv"
)

// defaultMemoryLimitBytes 对账 TS `defaultMemoryLimitBytes` 的回退值（1 GiB）。
//
// TS 注释：桌面端 sidecar 以 `node main.js` 启动，tsup shebang 的
// `--max-old-space-size` 被忽略（Windows 上尤其），故硬编码 1GB 会让内存压力
// 信号与现实脱节——TS 因此**优先读 V8 的真实上限**。Go 侧同理优先读环境变量。
const defaultMemoryLimitBytes int64 = 1 << 30 // 1 GiB

// MemoryLimitBytes 返回进程的内存上限（对账 TS `defaultMemoryLimitBytes`）。
//
// 优先 `RIVET_MEMORY_LIMIT_BYTES`（> 0 才生效，对账 TS 的
// `Number.isFinite(configured) && configured > 0`）；否则回退默认值。
func MemoryLimitBytes() int64 {
	raw := os.Getenv("RIVET_MEMORY_LIMIT_BYTES")
	if raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return defaultMemoryLimitBytes
}

// CurrentRSSBytes 返回当前进程的内存占用（对账 TS `usage.rss`）。
//
// **实现差异（明示）**：Go 的 `runtime.MemStats.Sys` 是「向 OS 申请的总字节」
// ——语义上最接近 Node 的 `process.memoryUsage().rss`（两者都含非堆开销）。
// 不用 `HeapAlloc`（那对应 Node 的 `heapUsed`，是另一路信号）。
func CurrentRSSBytes() int64 {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return int64(ms.Sys)
}

// CurrentRSSRatio 返回内存压力比（对账 TS 的 `rssBytes / memoryLimitBytes`）。
//
// **limit <= 0 时返回 0**（对账 TS 的 `snap ? ... : 0` 保护——避免除零）。
func CurrentRSSRatio() float64 {
	limit := MemoryLimitBytes()
	if limit <= 0 {
		return 0
	}
	return float64(CurrentRSSBytes()) / float64(limit)
}
