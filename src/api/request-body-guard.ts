/**
 * 请求体体积护栏（可选，默认关闭）——provider 配置 `maxBodyBytes` 的执行点。
 *
 * 背景：provider 网关对请求体有自己的上限（DeepSeek 等约 4MB）。超限时**服务端
 * 按字节截断**，如果这一刀正好切进一个 `\uXXXX` 转义，用户看到的就是：
 *
 *   OpenAI API error (HTTP 400): Failed to parse the request body as JSON:
 *   messages[1095].content unexpected end of hex escape at line 1 column …
 *
 * 那是一条指向「模型侧什么都没发生」的死路：网关没进模型、用户不知道要做什么。
 * 常量 `MAX_JSON_BODY_BYTES` 早就写在 utils/sanitize.ts 里，但一直没有消费方；
 * 本模块把它接上。
 *
 * 默认不启用（issue #251 后续）：量体要把整个 body 再 JSON.stringify 一遍，没配置
 * 就不该付这个成本；且端点真实上限差异很大（官方约 4MB，中转可能只有 1MB），替用户
 * 猜一个值并不合适。未配置时上游报错（400 body-parse / 413）的文案会提示去配置
 * `provider.providers.<name>.maxBodyBytes` 来启用本护栏。
 *
 * 策略（确定性、只动历史、绝不 in-place 改入参）：
 *   ① 量体：JSON.stringify 后的 UTF-8 字节数
 *   ② 超限：从**最大的历史 tool 结果**开始截断（保头 + 保尾 + 显式标注），
 *      最近 `keepRecentMessages` 条消息一律不动——当前任务的上下文不许被削
 *   ③ 截完仍超限：按**从旧到新**的顺序移除 user 消息里的 image part（最老的图先走，
 *      本轮刚贴的图最后才动），替换为模型可见占位符——与 Grok Build 的
 *      `apply_image_budget` 同款纪律：静默丢图会被模型凭记忆瞎猜
 *   ④ 图片也移除完仍超限：抛 RequestBodyTooLargeError，附「谁最大」的清单。用户拿到
 *      的是可行动的中文（压缩 / 新会话 / 查中转 / 调 maxBodyBytes），不是网关的英文
 *      serde 报错
 *
 * 形态（shape）：②③ 的落点按 wire 体形态解析——OpenAI 兼容体与 Anthropic Messages
 * 体的工具输出/图片字段名不同（见 BodyGuardOptions.shape）。**降级纪律共用一份**，
 * 只有「去哪儿找」分叉：两个客户端若各写一套截断，迟早会漂移成两种行为。
 *
 * 为什么必须确定性：同一份 messages 每次要截出**逐字节相同**的体，否则每轮都碎
 * 前缀缓存。所以截断只依赖内容本身——无时间戳、无随机、无 Map 迭代序。
 *
 * 为什么先 tool 后图片：tool 结果是同一个会话里唯一「体积大且可再取回」的内容（原始
 * 输出在会话记录/artifact 里）。图片不可再取回，所以排在工具输出之后；而 user 文本
 * 是任务的语义骨架，一个字都不动（图片 part 换成占位文本属于 wire 层降级，原文仍在
 * 会话历史里）。
 */

/** 最近 N 条消息不参与截断（当前任务的活动上下文）。 */
export const GUARD_KEEP_RECENT_MESSAGES = 6
/** 小于这个体积的 tool 结果不值得动（截了也省不出多少，徒增缓存扰动）。 */
export const GUARD_MIN_TRUNCATABLE_BYTES = 8 * 1024
/** 截断后保留的头/尾字符数。 */
export const GUARD_HEAD_CHARS = 2000
export const GUARD_TAIL_CHARS = 1000

export interface BodyGuardOptions {
  /** 体积上限（字节）。未配置 = 不启用护栏（默认）——不做量体、不截断、不预警。 */
  limitBytes?: number
  /**
   * wire 形态。`openai`（默认）走 OpenAI 兼容体：工具输出是 `role:'tool'` 消息的字符串
   * content，图片是 user 消息里的 `image_url` part。
   * `anthropic` 走 Anthropic Messages 体：工具输出是 user 消息里的 `tool_result` block
   * （content 为字符串），图片是 `image` block——形态不同，但**降级纪律必须同一套**
   * （同一个体积上限、同一条「最老图片先走 + 留占位符」规则），否则 anthropic 侧
   * 一超限就只能直接抛错，白白丢掉它本来能自己腾出来的空间。
   */
  shape?: BodyShape
  /** 超过这个字节数即回报“逼近上限”（默认 = 上限的一半）。 */
  warnBytes?: number
  keepRecentMessages?: number
  minTruncatableBytes?: number
  headChars?: number
  tailChars?: number
}

/** wire 体形态（决定去哪里找「可截断的大块文本」与「图片」）。 */
export type BodyShape = 'openai' | 'anthropic'

/** 逼近上限的默认阈值比例：配置 4MB 上限则 2MB 起提醒（未配置护栏时无预警）。 */
export const GUARD_NEAR_LIMIT_RATIO = 0.5

/**
 * 图片被预算驱逐后在 wire 上的替身。措辞必须让模型知道「图不在了」而不是「图还在但看不清」
 * ——静默丢图会诱导模型凭记忆描述（Grok Build `image_budget.rs` 的 IMAGE_COMPACT_PLACEHOLDER
 * 同款纪律）。原文仍保留在会话历史里，用户重新贴图即可。
 */
export const IMAGE_EVICTED_PLACEHOLDER =
  '[An earlier image was removed to keep the request within its size limit and is no longer visible. '
  + 'Do not describe or reason about its contents from memory; ask the user to re-share it if you need to see it again.]'

/** 单条 wire 消息的最小计费单元：JSON.stringify(part) 的 UTF-8 字节数（含引号与转义）。 */
function measurePartBytes(part: unknown): number {
  try {
    return Buffer.byteLength(JSON.stringify(part) ?? '')
  } catch {
    return 0
  }
}

/**
 * 发给上层（进而发给用户的）体积事件。两种形态：
 * - `degraded`：这一轮的 wire 体已被截断（历史工具输出被削），模型看到的历史不完整；
 * - `near-limit`：还没截，但已逼近上限——**第三方中转常有更小的 body 上限**，
 *   提醒用户先压缩，别等到 400。
 */
export interface BodyGuardNotice {
  kind: 'degraded' | 'near-limit'
  bytes: number
  limitBytes: number
  degradedCount?: number
  removedBytes?: number
  /** 其中被移除的图片数（degradedCount 的子集）；仅用于让文案说清「图没了」。 */
  imagesEvicted?: number
}

/**
 * 人读体积（B / KB / MB）。**别用 `toFixed(1) + 'MB'` 一刀切**：那样 30KB 会显示成
 * 0.0MB——issue #251 里 4.7MB 的报错把五个「最大来源」全列成 0.0MB，排查线索归零。
 * 分档后 KB 档不再出现 0.0，小条目也还读得出量级。
 */
export function formatByteSize(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0B'
  if (bytes < 1024) return `${Math.round(bytes)}B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)}KB`
  return `${(bytes / 1024 / 1024).toFixed(1)}MB`
}

/** 通知文案（TUI 状态行 / 桌面相位标签共用，纯函数可测）。 */
export function formatBodyGuardNotice(info: BodyGuardNotice): string {
  if (info.kind === 'degraded') {
    const degraded = info.degradedCount ?? 0
    const images = Math.min(info.imagesEvicted ?? 0, degraded)
    const tools = Math.max(0, degraded - images)
    const actions: string[] = []
    if (tools > 0) actions.push(`截断 ${tools} 条历史工具输出`)
    if (images > 0) actions.push(`移除 ${images} 张较早图片`)
    const what = actions.length > 0 ? actions.join('、') : `${degraded} 处历史内容`
    return (
      `⚠ 对话体量超传输上限：本轮已${what}` +
      `（约 ${formatByteSize(info.removedBytes ?? 0)}，现 ${formatByteSize(info.bytes)}/${formatByteSize(info.limitBytes)}）——` +
      '模型看不到被移除的内容，建议 /compact 或新开会话'
    )
  }
  return (
    `⚠ 对话体量已接近传输上限（${formatByteSize(info.bytes)}/${formatByteSize(info.limitBytes)}）：` +
    '部分第三方中转的上限更低，建议尽早 /compact 或新开会话'
  )
}

export interface BodyDegrade {
  messageIndex: number
  /** 该处被削掉的字节数（按 UTF-8 计）。 */
  removedBytes: number
  /** 降级类型：`tool`=截断历史工具输出；`image`=移除较早图片。缺省按 `tool` 兼容旧消费方。 */
  kind?: 'tool' | 'image'
}

/**
 * 客户端侧的上报状态（每个 client 实例一份；护栏本身无状态）。
 * `degradeCount` 初值 -1 = 「还没报过」——0 是合法值（本轮没降级），不能用 0 当哨兵。
 */
export interface BodyGuardNotifyState {
  degradeCount: number
  nearLimit: boolean
}

export function createBodyGuardNotifyState(): BodyGuardNotifyState {
  return { degradeCount: -1, nearLimit: false }
}

/**
 * 降级/逼近上限必须可见（同 issue #94 的剥图教训：wire 层降级静默 = 用户读成
 * 「模型变笨了」）。降级只在「降级集合变化」时上报一次——截断是确定性的，同一段历史
 * 每轮都被同样地截，逐轮上报只会把状态行刷成噪音。
 *
 * openai / anthropic 两个客户端共用本函数：上报节律属于护栏语义，不该在客户端里
 * 各写一份（两份实现迟早会漂移成两种节律）。
 */
export function notifyBodyGuard(
  outcome: BodyGuardOutcome,
  state: BodyGuardNotifyState,
  onNotice?: (info: BodyGuardNotice) => void,
): void {
  if (outcome.degraded.length > 0) {
    if (outcome.degraded.length === state.degradeCount) return
    state.degradeCount = outcome.degraded.length
    onNotice?.({
      kind: 'degraded',
      bytes: outcome.bytes,
      limitBytes: outcome.limitBytes,
      degradedCount: outcome.degraded.length,
      removedBytes: outcome.degraded.reduce((n, d) => n + d.removedBytes, 0),
      imagesEvicted: outcome.evictedImages,
    })
    return
  }
  if (outcome.nearLimit && !state.nearLimit) {
    state.nearLimit = true
    onNotice?.({
      kind: 'near-limit',
      bytes: outcome.nearLimit.bytes,
      limitBytes: outcome.nearLimit.limitBytes,
    })
  }
}

export interface BodyGuardOutcome {
  /** 可能被截断的**新**体（入参未被修改；未截断时返回原引用）。 */
  body: Record<string, unknown>
  /** wire 体字节数；护栏未启用（limitBytes 非有限值）时不做量体，恒为 0。 */
  bytes: number
  /** 实际生效的上限；护栏未启用时为 Infinity。 */
  limitBytes: number
  degraded: BodyDegrade[]
  /** 其中被移除的图片数（degraded 中 kind==='image' 的条数）。 */
  evictedImages: number
  /** 未降级但已逼近上限时给出（降级时不再重复报这条——降级是更强的信号）。 */
  nearLimit?: { bytes: number; limitBytes: number }
}

/** 超限且已无更多可截断内容——附最大来源清单，供错误文案直接点名。 */
export class RequestBodyTooLargeError extends Error {
  readonly bytes: number
  readonly limitBytes: number
  readonly contributors: { messageIndex: number; role: string; bytes: number }[]

  constructor(
    bytes: number,
    limitBytes: number,
    contributors: { messageIndex: number; role: string; bytes: number }[],
    /** 抛错前是否真的降级过历史内容（截工具输出/移图片）；没降过就不许说「已截断」（issue #251）。 */
    info: { truncated?: boolean } = {},
  ) {
    const top = contributors
      .slice(0, 5)
      .map((c) => `messages[${c.messageIndex}] ${c.role} ${formatByteSize(c.bytes)}`)
      .join('、')
    // 单条最大者不足总量一成：清单本身解释不了总量从哪来——把这点写明，
    // 别让用户拿一份无解释的清单去查（issue #251：4.7MB 下列出五个 0.0MB）。
    const max = contributors.reduce((n, c) => Math.max(n, c.bytes), 0)
    const sharePct = bytes > 0 ? (max / bytes) * 100 : 0
    const dispersedNote =
      max > 0 && max * 10 < bytes
        ? `——体积分散在多条消息里（单条最大 ${formatByteSize(max)}，${sharePct < 1 ? '不足 1%' : `${Math.round(sharePct)}%`}）`
        : ''
    super(
      `请求体 ${formatByteSize(bytes)} 超出传输上限 ${formatByteSize(limitBytes)}，` +
      (info.truncated ? '已截断历史工具输出/移除较早图片仍超限。' : '已无可截断的历史内容。') +
      `最大来源：${top || '（无法定位）'}${dispersedNote}。` +
      '处理：用 /compact 压缩本会话，或新开会话继续；若 baseUrl 走第三方中转，中转常有更小的 body 限制；' +
      '也可调大（或清空）该 provider 的 maxBodyBytes 配置。',
    )
    this.name = 'RequestBodyTooLargeError'
    this.bytes = bytes
    this.limitBytes = limitBytes
    this.contributors = contributors
  }
}

/** wire 体字节数（与 fetch 发出的字节一致：JSON.stringify + UTF-8）。 */
export function measureBodyBytes(body: unknown): number {
  try {
    return Buffer.byteLength(JSON.stringify(body) ?? '')
  } catch {
    // 循环引用等异常体不该让护栏本身成为故障点：量不出来就当 0，
    // 让下游 fetch 的 stringify 去抛真正的原因。
    return 0
  }
}

function truncateToolContent(content: string, head: number, tail: number): string {
  const headPart = content.slice(0, head)
  const tailPart = tail > 0 ? content.slice(Math.max(head, content.length - tail)) : ''
  // 标注里的「省略约 NKB」必须按**实际削掉**的字节算（原文 − 保留的头尾），
  // 不能拿原文总字节冒充——8KB 门槛附近的小体上两者差出一个数量级。
  const removedBytes =
    Buffer.byteLength(content) - Buffer.byteLength(headPart) - Buffer.byteLength(tailPart)
  const marker =
    `\n…[已省略约 ${(Math.max(0, removedBytes) / 1024).toFixed(0)}KB：请求体超出传输上限，完整输出仍在会话记录/artifact 里。` +
    '如需完整内容请重新读取该文件或命令]…\n'
  return `${headPart}${marker}${tailPart}`
}

/**
 * 体积清单（错误文案 + 日志用）：**按整条消息的 JSON 字节**计，与 measureBodyBytes
 * 同口径——只数 content/tool_calls 会漏 reasoning_content、name、tool_call_id、数组型
 * content 等字段（issue #251：1MB 思考内容被记成 2B）。按体积降序，同体积按下标升序
 * （稳定）。
 */
function topContributors(
  messages: unknown[],
  count = 5,
): { messageIndex: number; role: string; bytes: number }[] {
  const rows: { messageIndex: number; role: string; bytes: number }[] = []
  for (let i = 0; i < messages.length; i++) {
    const m = messages[i]
    if (!m || typeof m !== 'object') continue
    let bytes = 0
    try {
      bytes = Buffer.byteLength(JSON.stringify(m) ?? '')
    } catch {
      // 循环引用等异常消息：量不出就跳过，别让「报错的报错」盖住真正原因。
      bytes = 0
    }
    if (bytes > 0) rows.push({ messageIndex: i, role: String((m as { role?: unknown }).role ?? '?'), bytes })
  }
  return rows.sort((a, b) => b.bytes - a.bytes || a.messageIndex - b.messageIndex).slice(0, count)
}

/**
 * 量体 + 必要时降级。返回的 body 可直接 JSON.stringify 发出。
 * 未超限时返回**原引用**（零拷贝、字节不变，前缀缓存不受影响）。
 * 未配置上限（默认）时不量体：直接原引用放行。
 */
export function enforceRequestBodyLimit(
  body: Record<string, unknown>,
  opts: BodyGuardOptions = {},
): BodyGuardOutcome {
  const limitBytes = opts.limitBytes ?? Number.POSITIVE_INFINITY
  // 未启用护栏：零开销直通。量体要对整段 body 再 stringify 一次，默认不该付这个成本；
  // 这类会话的体积问题由上游报错文案（400/413）引导用户来配置 maxBodyBytes。
  if (!Number.isFinite(limitBytes) || limitBytes <= 0) {
    return { body, bytes: 0, limitBytes: Number.POSITIVE_INFINITY, degraded: [], evictedImages: 0 }
  }
  const keepRecent = opts.keepRecentMessages ?? GUARD_KEEP_RECENT_MESSAGES
  const minTruncatable = opts.minTruncatableBytes ?? GUARD_MIN_TRUNCATABLE_BYTES
  const headChars = opts.headChars ?? GUARD_HEAD_CHARS
  const tailChars = opts.tailChars ?? GUARD_TAIL_CHARS
  const shape = opts.shape ?? 'openai'

  const warnBytes = opts.warnBytes ?? Math.floor(limitBytes * GUARD_NEAR_LIMIT_RATIO)
  const original = measureBodyBytes(body)
  if (original <= limitBytes) {
    return {
      body,
      bytes: original,
      limitBytes,
      degraded: [],
      evictedImages: 0,
      ...(original >= warnBytes ? { nearLimit: { bytes: original, limitBytes } } : {}),
    }
  }

  const rawMessages = body.messages
  if (!Array.isArray(rawMessages) || rawMessages.length === 0) {
    throw new RequestBodyTooLargeError(original, limitBytes, topContributors([]))
  }

  const messages = [...rawMessages] as unknown[]
  const lastMutable = Math.max(0, messages.length - keepRecent)
  const candidates = collectTextSlots(messages, shape, minTruncatable, lastMutable)
  // 最大的先削：同样削 1MB，「削 3 条各 300KB」比「削 20 条各 50KB」保留的信息多。
  candidates.sort((a, b) => b.bytes - a.bytes || a.messageIndex - b.messageIndex)

  const degraded: BodyDegrade[] = []
  let estimated = original
  for (const c of candidates) {
    if (estimated <= limitBytes) break
    const truncated = truncateToolContent(c.text, headChars, tailChars)
    const removed = c.bytes - Buffer.byteLength(truncated)
    if (removed <= 0) continue
    applyTextSlot(messages, c, truncated)
    estimated -= removed
    degraded.push({ messageIndex: c.messageIndex, removedBytes: removed, kind: 'tool' })
  }

  let nextBody = degraded.length > 0 ? { ...body, messages } : body
  // 估算只用于挑选顺序；最终以实测为准（不信任何算术）。
  let bytes = measureBodyBytes(nextBody)
  let evictedImages = 0

  // ③ 工具输出削完仍超限 → 按「最老优先」移除 user 图片，并留模型可见占位符。
  // 当前轮刚贴的图排在最后，只有历史图片腾不出空间时才会动到它。
  if (bytes > limitBytes) {
    const imageSlots = collectImageSlots(messages, shape)
    if (imageSlots.length > 0) {
      const placeholder = { type: 'text', text: IMAGE_EVICTED_PLACEHOLDER }
      const placeholderBytes = measurePartBytes(placeholder)
      let nextIndex = 0
      // 估算只用于挑选「至少削到哪」；每批之后以实测为准，仍超限就继续下一批
      // （估算与实际在转义/占位符上可能有偏差，不能拿估算当放行条件）。
      while (bytes > limitBytes && nextIndex < imageSlots.length) {
        const startIndex = nextIndex
        let running = bytes
        while (nextIndex < imageSlots.length && running > limitBytes) {
          const slot = imageSlots[nextIndex]!
          nextIndex++
          const msg = messages[slot.messageIndex] as Record<string, unknown> & { content: unknown[] }
          // 克隆消息与 content 数组：绝不 in-place 改入参（调用方的会话历史保持原样）。
          const nextContent = [...msg.content]
          const evicted = nextContent[slot.partIndex] as { cache_control?: unknown } | undefined
          // anthropic 的 cache_control 挂在 block 上（BP3/BP4）——驱逐图片不能顺手
          // 丢掉断点，否则这一刀会把后面的前缀缓存一起切碎。
          nextContent[slot.partIndex] = evicted?.cache_control
            ? { ...placeholder, cache_control: evicted.cache_control }
            : { ...placeholder }
          messages[slot.messageIndex] = { ...msg, content: nextContent }
          const removed = Math.max(0, slot.partBytes - placeholderBytes)
          running -= removed
          degraded.push({ messageIndex: slot.messageIndex, removedBytes: removed, kind: 'image' })
          evictedImages++
        }
        nextBody = { ...body, messages }
        bytes = measureBodyBytes(nextBody)
        if (nextIndex === startIndex) break // 占位符比原图还大：再驱逐也省不出字节
      }
    }
  }

  if (bytes > limitBytes) {
    throw new RequestBodyTooLargeError(bytes, limitBytes, topContributors(messages), {
      truncated: degraded.length > 0,
    })
  }
  return { body: nextBody, bytes, limitBytes, degraded, evictedImages }
}

interface ImageSlot {
  messageIndex: number
  partIndex: number
  partBytes: number
}

/**
 * 一「处」可截断的大块文本：
 * - openai：`role:'tool'` 消息的字符串 content（blockIndex = -1，改消息本身）；
 * - anthropic：user 消息里 `tool_result` block 的字符串 content（blockIndex = 数组下标）。
 */
interface TextSlot {
  messageIndex: number
  blockIndex: number
  text: string
  bytes: number
}

/** 按形态收集可截断的工具输出（只在前 `lastMutable` 条里找——最近的消息不参与截断）。 */
function collectTextSlots(
  messages: unknown[],
  shape: BodyShape,
  minBytes: number,
  lastMutable: number,
): TextSlot[] {
  const slots: TextSlot[] = []
  for (let i = 0; i < lastMutable; i++) {
    const m = messages[i] as { role?: unknown; content?: unknown } | undefined
    if (!m) continue
    if (shape === 'openai') {
      if (m.role !== 'tool' || typeof m.content !== 'string') continue
      const bytes = Buffer.byteLength(m.content)
      if (bytes >= minBytes) slots.push({ messageIndex: i, blockIndex: -1, text: m.content, bytes })
      continue
    }
    if (m.role !== 'user' || !Array.isArray(m.content)) continue
    for (let p = 0; p < m.content.length; p++) {
      const block = m.content[p] as { type?: unknown; content?: unknown } | undefined
      if (!block || block.type !== 'tool_result' || typeof block.content !== 'string') continue
      const bytes = Buffer.byteLength(block.content)
      if (bytes >= minBytes) slots.push({ messageIndex: i, blockIndex: p, text: block.content, bytes })
    }
  }
  return slots
}

/** 把截断后的文本写回（克隆消息与 content 数组：绝不 in-place 改入参）。 */
function applyTextSlot(messages: unknown[], slot: TextSlot, text: string): void {
  const msg = messages[slot.messageIndex] as Record<string, unknown> & { content: unknown }
  if (slot.blockIndex < 0) {
    messages[slot.messageIndex] = { ...msg, content: text }
    return
  }
  const nextContent = [...(msg.content as unknown[])]
  nextContent[slot.blockIndex] = { ...(nextContent[slot.blockIndex] as Record<string, unknown>), content: text }
  messages[slot.messageIndex] = { ...msg, content: nextContent }
}

/** 按消息顺序收集图片（数组序 = 最老优先，与驱逐顺序一致）。 */
function collectImageSlots(messages: unknown[], shape: BodyShape): ImageSlot[] {
  const slots: ImageSlot[] = []
  for (let i = 0; i < messages.length; i++) {
    const m = messages[i] as { role?: unknown; content?: unknown } | undefined
    if (!m || !Array.isArray(m.content)) continue
    // openai 的图只在 user 消息里且是 image_url part；anthropic 的 image block 同理
    // 只在 user 消息里（assistant 侧的图是模型输出，客户端不会构造）。
    if (m.role !== 'user') continue
    for (let p = 0; p < m.content.length; p++) {
      const part = m.content[p] as { type?: unknown } | undefined
      if (!part) continue
      const isImage = shape === 'openai' ? part.type === 'image_url' : part.type === 'image'
      if (!isImage) continue
      slots.push({ messageIndex: i, partIndex: p, partBytes: measurePartBytes(part) })
    }
  }
  return slots
}

/** 降级日志用的一行摘要（无降级返回空串）。 */
export function formatDegradeNotice(degraded: BodyDegrade[]): string {
  if (degraded.length === 0) return ''
  const totalKb = degraded.reduce((n, d) => n + d.removedBytes, 0) / 1024
  const images = degraded.filter((d) => d.kind === 'image').length
  const tools = degraded.length - images
  const parts: string[] = []
  if (tools > 0) parts.push(`截断 ${tools} 条历史工具输出`)
  if (images > 0) parts.push(`移除 ${images} 张较早图片`)
  return `请求体超限降级：${parts.join('、')}（共 ${totalKb.toFixed(0)}KB）`
}
