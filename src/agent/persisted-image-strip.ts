/**
 * 服务端明确拒图后的「持久化剥图」——Grok Build `image_strip.rs` 同款纪律。
 *
 * 现状：413 / 图片被拒时客户端只改 wire 副本，会话历史仍保图 → 下一轮原样重发、
 * 再失败、再剥一次。每轮白付一次被拒请求；如果服务端对整段请求判死（413），
 * 这一轮所有图都会反复消失。
 *
 * 本模块把「唯一图 URL 被判死」的剥离写回 SessionContext：
 *   1. 会话历史里所有 image part 替换为 `STRIPPED_IMAGE_PLACEHOLDER`
 *      （与本次 wire 剥图逐字节相同，下一轮直接续上已缓存前缀）；
 *   2. `replaceMessages` 发出 replace mutation → session-persist-listener 原子
 *      重写 transcript，重启/恢复后图片不会回魂；
 *   3. blame 不唯一（请求里有 2+ 个不同 URL）时**只改 wire**——服务端 4xx/413
 *      指认的是请求而不是某张图，Grok 同样只在 `ServerRejected` 且 stripped_urls
 *      全等时改写历史。
 *
 * 用户可见性不受影响：事件日志里的 imageIds/缩略图仍在，用户能看到自己贴过什么；
 * 被移除的只是模型请求里的 base64。
 */

import type { SessionContext } from './context.js'
import { stripOaiImageParts } from '../api/oai-types.js'
import { debugLog } from '../utils/debug.js'

/** `onImageStripped` 回调携带的信息（与 StreamCallbacks 同形）。 */
export interface ImageStrippedInfo {
  removedCount: number
  /** 剥图前请求里不同 image URL 的数量；undefined（旧调用方）按不唯一处理。 */
  uniqueUrlCount?: number
}

/**
 * 把本轮剥离持久化写回会话历史。返回 true = 历史已重写（transcript 会异步原子重写）。
 *
 * 只在 blame 唯一（`uniqueUrlCount === 1`）时动手；其余情况保持 wire-only。
 * session 参数取最小接口，便于单测注入假 session。
 */
export function persistStrippedImagesIfUnambiguous(
  session: Pick<SessionContext, 'getMessages' | 'replaceMessages'>,
  info: ImageStrippedInfo,
): boolean {
  if (info.uniqueUrlCount !== 1) return false
  const current = session.getMessages()
  const { messages, removedCount } = stripOaiImageParts(current)
  if (removedCount === 0) return false
  session.replaceMessages(messages)
  debugLog(`[image-strip] 服务端明确拒图（唯一 URL）→ 已从会话历史移除 ${removedCount} 个 image part，后续轮次不再重发`)
  return true
}
