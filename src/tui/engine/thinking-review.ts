/**
 * thinking 回看仓（grok-build collapsed-thinking「诚实重印」对标本）。
 *
 * thinking 提交进 scrollback 的只有一行头部（`已推理 · Ns · N 行`），正文默认
 * 不落盘——长考正文几百行全进 scrollback 是更大的噪音。但「刚才它想了什么」
 * 是真实需求：这里留存最近一次 thinking 的正文，ctrl+t 在空闲时把全文
 * 重印进 scrollback（重印本即持久记录，取出即清，避免重复重印）。
 */

import { formatThinking } from '../format/thinking.js'
import type { RivetTheme } from '../theme.js'

export interface ThinkingReview {
  text: string
  elapsedMs: number
  domainId?: string
}

/** 留存上限：超长思考留尾部（结论在近处，与 live 区 tail 截断同向）。 */
const REVIEW_MAX_CHARS = 256_000

export class ThinkingReviewStore {
  private last: ThinkingReview | null = null

  save(review: ThinkingReview): void {
    const text = review.text.length > REVIEW_MAX_CHARS ? review.text.slice(-REVIEW_MAX_CHARS) : review.text
    if (!text.trim()) return
    this.last = { ...review, text }
  }

  peek(): ThinkingReview | null {
    return this.last
  }

  /** 取出并清空——重印是一次性的，重印本已在 scrollback。 */
  take(): ThinkingReview | null {
    const v = this.last
    this.last = null
    return v
  }

  clear(): void {
    this.last = null
  }
}

/** 重印本体：done 头 + 展开正文（逻辑行上限防一次性糊屏，超出仍走「上方省略」）。 */
export function formatThinkingReview(review: ThinkingReview, theme: RivetTheme): string[] {
  return formatThinking({
    text: review.text,
    elapsedMs: review.elapsedMs,
    done: true,
    expanded: true,
    maxLines: 400,
    domainId: review.domainId,
  }, theme)
}
