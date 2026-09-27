/**
 * assistant 文本流的 commit 屏障。
 *
 * 事故形状（2026-09-26 用户报告）：agent 给出交付结论 → 被要求补动作 → 工具调用
 * → 再给结论。用户最后只看到第二段结论，第一段的尾段像被工具组吞掉——实测的
 * scrollback 形态是「…后续建议 / ▶ Read 1 file · 7ms / └─ a.ts」：标题落盘了，
 * 内容不见了。
 *
 * 根因：BlockStreamWriter 的出口阈值（首块 15 字符、之后 100 字符，段落切点须落在
 * minChars*0.5 之后）使长文本**最后一块**滞留在自己的 buffer 里；而工具组 / 工具
 * 卡片会抢先 commit 到 scrollback，顺序颠倒成「工具组 → 文本尾」。
 *
 * 不变量：任何「非 assistant 文本内容」写入 scrollback **之前**，必须先调本函数。
 *
 * 实现要点：BlockStreamWriter.flush() 的 onBlock 回调是**同步**的（文本同帧落盘），
 * 结尾的 `await this.sending` 只是等一个已 settle 的 Promise——因此可以在同步调用链
 * （handleToolUse 是同步回调）里用 void 触发，无需把调用链改成 async。
 *
 * 命名与位置对齐既有提取模式（cf. stream-render-controller.ts）：TuiApp 持有状态，
 * 契约与边界判定放在可独立测试的小模块里。
 */

/** BlockStreamWriter 的最小结构（避免把整个类拖进来，便于测试替身）。 */
export interface BlockFlusher {
  flush(): Promise<void>
}

/** StreamRenderer 的最小结构：finalize 落盘 pending 并返回是否 commit 过内容。 */
export interface StreamFinalizer {
  finalize(): boolean
}

/**
 * 把 assistant 文本流的滞留部分立即落盘。
 *
 * 调用点（缺一不可）：
 * - handleToolUse 开头：工具卡片 / 折叠组随后就要 commit
 * - flushToolGroup / flushBashGroup 落盘前：组插队会把文本尾段挤到组后面，
 *   含回合末尾的残余组 flush 路径
 */
export function flushPendingAssistantText(
  blockWriter: BlockFlusher,
  streamRenderer: StreamFinalizer,
): void {
  void blockWriter.flush().catch(() => {
    // 渲染层异常不应阻断工具流程；文本会在回合末尾（handleTurnComplete）重试落盘。
  })
  streamRenderer.finalize()
}
