/**
 * browser-debug/frame-stream — 实时帧订阅的中枢（跨会话重建共享）。
 *
 * 为什么单独一层：内嵌浏览器面板（SSE）在连接时订阅一次，返回的退订闭包
 * 会被长期持有。会话因浏览器死亡被驱逐重建时，如果订阅集合跟着旧会话对象
 * 一起销毁：
 *   1. 新会话没有订阅者 → 推流不重启，面板冻结到用户手动重连；
 *   2. 面板断开时执行的是旧会话的退订闭包 → 新会话的订阅永远清不掉，
 *      浏览器持续编码帧（引用计数失效）。
 *
 * 解法：订阅集合与 driver 解耦。重建时新会话**接管同一个 FrameStream**
 * （driver 重绑到新实例、按需重启推流并补一帧），退订闭包始终操作同一集合。
 */

import type { BrowserDebugDriver, ScreencastFrame, ScreencastOptions } from './driver.js'

export class FrameStream {
  private subscribers = new Set<(frame: ScreencastFrame) => void>()
  private active = false

  constructor(private driver: BrowserDebugDriver) {}

  get streaming(): boolean {
    return this.active
  }

  get subscriberCount(): number {
    return this.subscribers.size
  }

  /** 订阅实时画面，返回幂等退订函数。driver 无帧流能力时返回 no-op 退订。 */
  async subscribe(
    onFrame: (frame: ScreencastFrame) => void,
    opts?: ScreencastOptions,
  ): Promise<() => void> {
    if (typeof this.driver.startScreencast !== 'function') {
      return () => {}
    }
    this.subscribers.add(onFrame)
    if (!this.active) {
      this.active = true
      try {
        await this.driver.startScreencast(opts ?? {}, (frame) => this.broadcast(frame))
      } catch (err) {
        this.active = false
        this.subscribers.delete(onFrame)
        throw err
      }
    }
    let unsubscribed = false
    return () => {
      if (unsubscribed) return
      unsubscribed = true
      this.subscribers.delete(onFrame)
      if (this.subscribers.size === 0 && this.active) {
        this.active = false
        void this.driver.stopScreencast?.().catch(() => {})
      }
    }
  }

  /** 旧 driver 退役：停推流但**保留订阅者**，等新会话 rebind 接管。 */
  async detach(): Promise<void> {
    if (!this.active) return
    this.active = false
    try {
      await this.driver.stopScreencast?.()
    } catch {
      /* 浏览器可能已经死了——退役是清理语义，不因此失败 */
    }
  }

  /**
   * 新 driver 接管：重绑并（若有订阅者）重启推流 + 补一帧。
   * 补帧是必要的：静态页不产帧，面板否则停在旧浏览器/旧页面的最后一帧。
   * 返回成功接管的订阅者数量（driver 无能力或启动失败时为 0）。
   */
  async rebind(driver: BrowserDebugDriver): Promise<number> {
    this.driver = driver
    const count = this.subscribers.size
    if (count === 0 || typeof driver.startScreencast !== 'function') return 0
    this.active = true
    try {
      await driver.startScreencast({}, (frame) => this.broadcast(frame))
    } catch {
      this.active = false
      return 0
    }
    try {
      const frame = await driver.captureFrame?.()
      if (frame) this.broadcast(frame)
    } catch {
      /* 补帧失败不影响推流本身 */
    }
    return count
  }

  /** 放弃订阅（会话彻底关闭或重建失败且无接管者时调用）。 */
  clear(): void {
    this.subscribers.clear()
    this.active = false
  }

  private broadcast(frame: ScreencastFrame): void {
    for (const cb of this.subscribers) {
      try {
        cb(frame)
      } catch {
        /* 单个订阅者抛错不影响其余 */
      }
    }
  }
}
