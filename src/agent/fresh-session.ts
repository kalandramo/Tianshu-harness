/**
 * /new 的会话身份层——生成全新会话并把运行时切过去。
 *
 * 为什么独立成模块而不是写进 bootstrap.ts：bootstrap 是 architecture-guards
 * 点名的巨石（max-lines ratchet「只降不升，沿接缝拆分」）。新能力进新模块，
 * 与 yolo-toggle.ts / zen-command.ts 同一处置。
 *
 * 依赖方向单向：本模块 → bootstrap（switchAgentSession / 类型），反向无引用，
 * 不成环。
 */

import { randomUUID } from 'node:crypto'
import { SessionPersist } from './session-persist.js'
import { switchAgentSession } from '../bootstrap.js'
import type { BootstrapContext, SwitchSessionResult } from '../bootstrap.js'

export interface FreshSessionResult {
  sessionId: string
  persist: SessionPersist
}

/**
 * 开新会话的准备步骤（/new 的第一段，确定性、可单测）：生成全新 session id
 * 并落 meta。**不碰运行时**——切身份是 startFreshSession 的第二段。
 *
 * meta 只写 cwd，两条都有具体理由：
 *  ① `listSessions`/`/resume` 读 meta，不写则 `/sessions` 里看不见它；
 *  ② `switchAgentSession` 的跨 cwd 守卫读 `meta.cwd`，缺字段是「放行」而非
 *     「拒绝」——显式写上才是可核对的事实。
 *
 * **刻意不写 model**：meta.model 是 switchAgentSession 的 resume 亲和开关，
 * 一旦写上就会走 `resolveProviderForModel` 分支——模型不在当前 provider 配置
 * 里时 fail-closed 拒绝，文案还是「请开新会话继续」（用户正在开新会话，荒谬）。
 * 空着则命中「无 model 记录 → 保留当前模型」分支，正是 /new 想要的语义：
 * 延续当前模型，不做任何「恢复」决策。代价仅是下次 resume 这个新会话时按当时
 * 的模型续跑（用户在新会话里 /model 切换时 meta 会被补上）。
 *
 * 不复制任何历史——与 /fork 的分野：fork 复制 jsonl（保上下文、开分支），
 * new 从零开始（弃上下文、留存档）。
 */
export function createFreshSession(cwd: string): FreshSessionResult {
  const sessionId = randomUUID()
  const persist = new SessionPersist(sessionId, cwd)
  persist.initMetadata({ cwd })
  return { sessionId, persist }
}

export interface StartFreshSessionResult extends SwitchSessionResult {
  /** 新会话 id——失败时也可能已分配（meta 已落盘，无害）。 */
  sessionId?: string
  /** 被切走的旧会话 id，供调用方提示「/resume 可回访」。 */
  previousSessionId?: string
}

/**
 * 会话中途开新会话（TUI /new）——对齐 Claude Code 的 /clear：同一个进程里换
 * 一段干净上下文，旧上下文完整留在磁盘上可回访。
 *
 * 与 switchAgentSession 的分工：它做重活（重建 AgentLoop、replaceMessages、
 * pointer、registry 迁移），这里只回答「目标会话从哪来」——一个全新的空会话。
 * 因此这**不是 resume**：没有历史 replay、没有模型亲和回退；新会话的冻结快照
 * 不存在 → 前缀 byte-0 重建，这是「重置上下文」的固有代价，不是缺陷。
 *
 * 调用方职责：切换前先 flush 旧会话写缓冲（`ctx.persist.flushSessionBuffer()`），
 * 否则还排在旧 batchWriter 队列里的末几条消息会随旧实例一起被丢弃（与 /fork
 * 同款纪律）。flush 不放这里做：本函数是同步的，有 await 能力的是 handler 层。
 *
 * 重建失败（如凭据在切换瞬间失效）时返回 ok:false 而非抛出——旧会话文件本身
 * 不受影响，用户可重试或重启进程。
 */
export function startFreshSession(ctx: BootstrapContext): StartFreshSessionResult {
  const previousSessionId = ctx.sessionId

  let fresh: FreshSessionResult
  try {
    fresh = createFreshSession(ctx.cwd)
  } catch (err) {
    return { ok: false, error: `无法创建新会话：${(err as Error).message}` }
  }

  let res: SwitchSessionResult
  try {
    res = switchAgentSession(ctx, fresh.sessionId)
  } catch (err) {
    // 重建中途抛错：ctx 可能半更新，但磁盘上的旧会话完好——如实报告并让用户
    // 决定重试还是重启，不假装成功。
    return {
      ok: false,
      error: `切换新会话失败（旧会话文件未受影响）：${(err as Error).message}`,
      sessionId: fresh.sessionId,
      previousSessionId,
    }
  }

  return { ...res, sessionId: fresh.sessionId, previousSessionId }
}
