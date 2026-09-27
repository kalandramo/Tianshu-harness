/**
 * 共享重试预算（PLAN §3：「提供商重试与 Agent 重试共享一个预算，防止多层相乘」）。
 *
 * 背景：provider 侧有 per-category 重试（`retry-engine`），agent 侧有有界重连
 * （`agentReconnect`）。两层各自的预算是**相乘**的（3×3=9 次），用户看到的是
 * 「明明设了 3 次却打了 9 次」。把预算抽成一个显式对象，两层各自 `take()` 同一份，
 * 总次数才有上界。
 *
 * 语义：滑动窗口内的尝试计数。`windowMs` 省略 = 只按次数封顶（等价于整段运行共用）。
 * 纯内存、无副作用；调用方负责在**用户取消**时不再 `take()`（取消即放弃预算）。
 */
export interface RetryBudgetOptions {
  /** 窗口内允许的重试次数（不含首次调用）。 */
  maxAttempts: number
  /** 滑动窗口毫秒；省略 = 不随时间回收。 */
  windowMs?: number
}

export class RetryBudget {
  private attempts: number[] = []

  constructor(private readonly opts: RetryBudgetOptions) {
    if (!Number.isInteger(opts.maxAttempts) || opts.maxAttempts < 0) {
      throw new Error(`Invalid RetryBudget.maxAttempts: ${String(opts.maxAttempts)}`)
    }
  }

  /** 申请一次重试额度：允许则记账并返回 true，用尽返回 false。 */
  take(now: number = Date.now()): boolean {
    this.prune(now)
    if (this.attempts.length >= this.opts.maxAttempts) return false
    this.attempts.push(now)
    return true
  }

  remaining(now: number = Date.now()): number {
    this.prune(now)
    return Math.max(0, this.opts.maxAttempts - this.attempts.length)
  }

  /** 用户取消 / 任务不再自动恢复时清零（取消即放弃已耗额度，不影响新任务）。 */
  reset(): void { this.attempts = [] }

  private prune(now: number): void {
    if (this.opts.windowMs === undefined) return
    this.attempts = this.attempts.filter((t) => now - t < this.opts.windowMs!)
  }
}

/** 整段运行共用一份预算（多层各持引用即可）。 */
export function createRunRetryBudget(maxAttempts: number, windowMs?: number): RetryBudget {
  return new RetryBudget({ maxAttempts, ...(windowMs !== undefined ? { windowMs } : {}) })
}
