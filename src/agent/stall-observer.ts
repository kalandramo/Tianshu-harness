/**
 * 无进展哨兵（stall observer）——2026-09-08 write_file 写后挂起两次复现的教训：
 * 被动留痕（超时错误 / [tool-timeout]）只在触发点执行，而真正的卡死形态（总闸
 * 覆盖外的无界 await）静默无痕——21 分钟死锁、loop-lag 失明、两次都抓不到日志。
 *
 * 本模块提供主动观测：任何「回合级活动」打点（touchActivity），观察器周期性
 * 检查每个会话距最后打点的时长——超过阈值即 console.warn，指认**哪个会话**、
 * **最后活动是什么**（工具名/事件类型）、**静默了多久**。不依赖超时触发、
 * 不打断执行、不依赖用户现场操作（kill -USR1 / inspector）。
 *
 * 打点源（接线处见调用方）：
 *  - 会话事件落盘：session-manager 的 append（桌面端/远端——事件停止 = 回合死锁）
 *  - 工具执行起止：tool-pipeline execute（CLI/server 共用 agent 内核）
 *
 * 多会话隔离：活动表 per-session key——一个会话活跃不会掩盖另一个会话的静默
 * （并发的 A 卡死、B 正常时，A 仍会被报出）。
 *
 * 覆盖边界：本哨兵治**异步挂起**（loop 健康、回合无进展）；对**同步阻塞**
 * （事件循环被 2-4s+ 同步段卡住）setInterval 同样被推迟、观测失明——同步
 * 阻塞归 loop-lag 遥测 + 现场抓栈，两者互补。阈值 150s 高于工具 120s 超时
 * 上限（DEFAULT_TOOL_TIMEOUT_MS），合法慢工具不被误报；21 分钟级死锁仍远
 * 超阈值，告警时机不损失。
 */

import { AsyncLocalStorage } from 'node:async_hooks'
import { randomUUID } from 'node:crypto'

const runContext = new AsyncLocalStorage<{ key: string; generation: string }>()

export interface StallActivity {
  phase?: 'running' | 'tool' | 'approval' | 'saving'
  deadlineAt?: number
  generation?: string
  ts: number
  source: string
  /** Only beginRun may activate a completed run. */
  idle?: boolean
}

/** per-session 活动表。上限防泄漏：极端多会话时清理最老的半数（stall 会话
 *  若被清掉会在其 key 下次 touch 时重新计——可接受的退化）。 */
const activityByKey = new Map<string, StallActivity>()
const MAX_ACTIVITY_KEYS = 1000

/** 记录一次回合级活动。key：会话维度（server 用 sessionId，CLI 用会话 id）；
 *  source 示例：'event:tool_result' / 'tool:write_file:start'。首次打点时懒
 *  安装默认观察器（60s tick / 150s 阈值——高于 120s 工具超时，避免慢工具
 *  在合法执行期内被误报；unref 不阻塞退出）——任何运行形态
 *  （CLI/server/桌面端）只要开始打点即有观测，不依赖入口显式接线；显式
 *  installStallObserver 可覆盖参数（幂等）。 */
export function touchActivity(key: string, source: string): void {
  const current = activityByKey.get(key)
  const context = runContext.getStore()
  if (!current || current.idle || context?.key !== key || context.generation !== current.generation) return
  activityByKey.set(key, { ...current, ts: Date.now(), source })
}

export function setActivityPhase(key: string, phase: StallActivity['phase'], deadlineAt?: number): void {
  const current = activityByKey.get(key)
  const context = runContext.getStore()
  if (!current || current.idle || context?.key !== key || context.generation !== current.generation) return
  activityByKey.set(key, { ...current, phase, deadlineAt, ts: Date.now() })
}

export function beginRun(key: string, generation: string): void {
  if (!installed) installStallObserver()
  if (!activityByKey.has(key) && activityByKey.size >= MAX_ACTIVITY_KEYS) {
    // 清理最老的一半（按 ts 排序取前 500）
    const sorted = [...activityByKey.entries()].sort((a, b) => a[1].ts - b[1].ts)
    for (let i = 0; i < sorted.length / 2; i++) activityByKey.delete(sorted[i]![0])
  }
  activityByKey.set(key, { ts: Date.now(), source: 'run:start', idle: false, generation, phase: 'running' })
}

export function withActivityRun<T>(key: string, generation: string, fn: () => T): T {
  return runContext.run({ key, generation }, fn)
}

export async function observeRun<T>(key: string, fn: () => Promise<T>): Promise<T> {
  const inherited = runContext.getStore()
  if (inherited?.key === key) return fn()
  const generation = randomUUID()
  beginRun(key, generation)
  try { return await withActivityRun(key, generation, fn) }
  finally { finishRun(key, generation) }
}

export function finishRun(key: string, generation: string): void {
  const cur = activityByKey.get(key)
  if (cur?.generation === generation) activityByKey.set(key, { ...cur, idle: true })
}

/** 会话显式声明进入空闲（如用户回合完成、等待下一条输入）：观察器跳过该
 *  会话直至下一次 touchActivity（自动解除 idle）。回合完成≠stall——交付后
 *  用户阅读回复的静默期不应被报为无进展。 */
export function markIdle(key: string): void {
  const cur = activityByKey.get(key)
  const context = runContext.getStore()
  if (cur && context?.key === key && context.generation === cur.generation) finishRun(key, context.generation)
}

/** 会话终结（worker 收尾/会话 close）：从活动表移除，杜绝结束后残留条目被
 *  周期性重报。进程内表是唯一的——clear 后该 key 不再有任何告警，直到下次
 *  touchActivity 重新登记。 */
export function clearActivity(key: string): void {
  activityByKey.delete(key)
}

/** 测试/诊断用。key 无记录时返回 boot 时间。 */
export function getLastActivity(key: string): StallActivity {
  return activityByKey.get(key) ?? { ts: Date.now(), source: 'boot' }
}

/** loop-lag 归因辅助：列出全部会话的最后活动（最近活跃优先）。 */
export function listStallActivities(): Array<{ key: string; activity: StallActivity }> {
  return [...activityByKey.entries()]
    .map(([key, activity]) => ({ key, activity }))
    .sort((a, b) => b.activity.ts - a.activity.ts)
}

/** 测试用：清空活动表与已安装观察器，保证用例间隔离。 */
export function _resetStallObserverForTest(): void {
  installed?.dispose()
  installed = null
  activityByKey.clear()
}

export interface StallObserverOptions {
  /** 检查间隔 ms（默认 60s）。 */
  intervalMs?: number
  /** 距最后活动超过该值即告警 ms（默认 150s——高于 120s 工具超时上限，
   *  避免慢工具在合法执行期内被误报）。 */
  thresholdMs?: number
  warn?: (msg: string) => void
}

export interface StallObserverHandle {
  dispose(): void
}

let installed: StallObserverHandle | null = null

/** 安装无进展观察器。重复 install 会先 dispose 旧实例。返回 dispose 句柄。 */
export function installStallObserver(opts?: StallObserverOptions): StallObserverHandle {
  const intervalMs = opts?.intervalMs ?? 60_000
  const thresholdMs = opts?.thresholdMs ?? 150_000
  const warn = opts?.warn ?? ((msg: string) => console.warn(msg))
  installed?.dispose()

  // per-key 告警节流：多会话同时静默时每个 stall 会话都要报（全局节流会漏报
  // 同 tick 内后续会话）；同一静默期 5 分钟内同 key 只报一次（防刷屏），
  // 超 5 分钟重报防漏看。
  const lastWarnByKey = new Map<string, number>()
  const timer = setInterval(() => {
    const now = Date.now()
    for (const [key, act] of activityByKey) {
      // 空闲会话（markIdle：回合完成等待用户等）跳过——不是 stall。
      if (act.idle || act.phase === 'approval' || (act.deadlineAt !== undefined && now <= act.deadlineAt)) continue
      const elapsedMs = now - act.ts
      if (elapsedMs > thresholdMs) {
        const lastWarn = lastWarnByKey.get(key) ?? 0
        if (now - lastWarn > 300_000) {
          lastWarnByKey.set(key, now)
          const elapsedS = Math.round(elapsedMs / 1000)
          warn(
            `[stall-observer] session "${key}" no activity for ${elapsedS}s`
            + ` (last: ${act.source} @ ${new Date(act.ts).toISOString()})`
            + ' — no run progress observed; inspect execution phase and loop-delay metrics',
          )
        }
      }
    }
  }, intervalMs)
  if (typeof timer.unref === 'function') timer.unref()

  const handle: StallObserverHandle = { dispose: () => clearInterval(timer) }
  installed = handle
  return handle
}
