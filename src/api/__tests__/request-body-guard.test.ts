import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  createBodyGuardNotifyState,
  enforceRequestBodyLimit,
  formatBodyGuardNotice,
  measureBodyBytes,
  formatDegradeNotice,
  notifyBodyGuard,
  RequestBodyTooLargeError,
  formatByteSize,
  GUARD_KEEP_RECENT_MESSAGES,
  GUARD_MIN_TRUNCATABLE_BYTES,
} from '../request-body-guard.js'
import type { BodyGuardNotice } from '../request-body-guard.js'

// 请求体体积护栏（4MB）——反证表（每条对着一个偷懒实现）：
//   #1「没超限也顺手改一改」    → 断言返回原引用（字节不变，前缀缓存不受影响）
//   #2「截断把当前任务也削了」  → 断言最近 KEEP_RECENT 条与 system 一字不动
//   #3「截完不标注 / 头尾全丢」 → 断言保头保尾 + 标注里点名"超出传输上限"
//   #4「就地改入参」            → 断言原 messages 未被修改（重入安全）
//   #5「每次截得不一样」        → 两次调用产出逐字节相同的体（缓存安全）
//   #6「削不动也硬发」          → 抛 RequestBodyTooLargeError，中文可行动
//   #7「把 user/assistant 也削」→ 断言只动 tool 角色
//   #6b「30KB 显示成 0.0MB」     → 体积分档：KB 档不得出现 0.0MB（issue #251）
//   #6c「没削过也说已截断」      → 无可截内容时文案不得自相矛盾（issue #251）
//   #6d「只数 content/tool_calls」→ 最大来源按整条消息 JSON 计（issue #251）
//   #6e「清单解释不了总量」      → 单条占比过低时明说体积分散（issue #251）
//   #12「默认替用户猜一个上限」  → 未配置 maxBodyBytes 时不量体/不截断（#251 后续）
//   #13「图片多到装不下只抛错」  → 工具输出削完仍超限 → 按最老优先移除图片（Grok image_budget 同款）
//   #14「先丢图不削工具」        → 有可截 tool 时不动图片（工具输出可再取回，图片不可）
//   #15「移完图还硬说没降级」    → 移完仍超限 → 抛错并如实说「移除较早图片仍超限」
//   #16「丢图无痕 / 就地改入参」 → wire 上留模型可见占位符，原 messages 一字不动
//   #17「纯图轮次被掏空」        → 图片 part 换成文本占位符，角色交替与非空 user 轮次保持合法
//   #19「anthropic 形态只能抛错」→ tool_result / image block 走同一套降级（形状分叉、纪律不漂移）
//   #20「上报把状态行刷成噪音」  → notifyBodyGuard：降级只在集合变化时报一次、逼近上限每会话一次

// 测试用小上限（逻辑与 4MB 同一路径）：16KB 上限 + 40KB 内容，保证「确实超限」
// 且候选大于 GUARD_MIN_TRUNCATABLE_BYTES（默认 8KB）。
const LIMIT = 16 * 1024

function toolMsg(content: string): Record<string, unknown> {
  return { role: 'tool', tool_call_id: 't', content }
}

function baseBody(toolContents: string[], tail: Record<string, unknown>[] = []): Record<string, unknown> {
  return {
    model: 'm',
    messages: [
      { role: 'system', content: 'sys' },
      ...toolContents.map(toolMsg),
      ...tail,
    ],
  }
}

test('#1 未超限：返回原引用，字节与降级清单都不动', () => {
  const body = baseBody(['small output'])
  const out = enforceRequestBodyLimit(body, { limitBytes: LIMIT })
  assert.equal(out.body, body, '未超限必须返回原引用（零拷贝、字节不变）')
  assert.deepEqual(out.degraded, [])
  assert.equal(out.bytes, measureBodyBytes(body))
  assert.ok(out.bytes <= LIMIT)
})

test('#2 超限：只截历史 tool 结果，system 与最近 K 条一字不动', () => {
  const big = 'A'.repeat(40 * 1024)
  const recentSmall = 'B'.repeat(1024)
  const tail: Record<string, unknown>[] = [
    { role: 'user', content: 'current task' },
    toolMsg(recentSmall), // 落在最近 KEEP_RECENT 窗口内 → 不许动
  ]
  const body = baseBody([big], tail)
  assert.ok(measureBodyBytes(body) > LIMIT, '前提：该体确实超限')

  // 4 条消息 + 默认窗口 6 会让整段都不可动——这里显式把窗口收窄到 2，
  // 让"历史"确实是历史（默认窗口的语义由 #2b 单独钉）。
  const out = enforceRequestBodyLimit(body, { limitBytes: LIMIT, keepRecentMessages: 2 })
  assert.ok(out.bytes <= LIMIT, `截断后必须落到上限内，实际 ${out.bytes}`)
  assert.equal(out.degraded.length, 1)
  assert.equal(out.degraded[0]!.messageIndex, 1, '被截的是第 1 条（历史 tool）')

  const msgs = out.body.messages as Record<string, unknown>[]
  assert.equal(msgs[0]!.content, 'sys', 'system 不许动')
  assert.equal(msgs[msgs.length - 1]!.content, recentSmall, `最近 ${GUARD_KEEP_RECENT_MESSAGES} 条内的 tool 结果不许动`)
  assert.equal(msgs[msgs.length - 2]!.content, 'current task', '当前任务文本不许动')
})

test('#2b 最近窗口内的巨大 tool 不可动：宁可抛错也不削当前任务的上下文', () => {
  // 会话只有 2 条（system + 一条刚产生的巨大 tool 输出）：默认窗口把它们全划进
  // "最近"，护栏因此无候选可削 → 抛可行动错误。削它才是真正的坏结果：用户正在
  // 处理的那一步被静默抹掉，模型下一轮就答非所问。
  const body = baseBody(['Z'.repeat(40 * 1024)])
  assert.throws(
    () => enforceRequestBodyLimit(body, { limitBytes: LIMIT }),
    (err: unknown) => err instanceof RequestBodyTooLargeError,
  )
  assert.ok(measureBodyBytes(body) > LIMIT)
})

test('#3 截断保头保尾并在体里留可见标注（不是静默降级）', () => {
  const big = `HEAD-MARKER${'x'.repeat(40 * 1024)}TAIL-MARKER`
  const body = baseBody([big])
  const out = enforceRequestBodyLimit(body, { limitBytes: LIMIT, keepRecentMessages: 0 })
  const content = (out.body.messages as Record<string, unknown>[])[1]!.content as string
  assert.ok(content.startsWith('HEAD-MARKER'))
  assert.ok(content.endsWith('TAIL-MARKER'))
  assert.match(content, /已省略约 \d+KB/)
  assert.match(content, /完整输出仍在会话记录\/artifact 里/, '标注要告诉模型/用户内容去哪了')
  assert.match(formatDegradeNotice(out.degraded), /请求体超限降级：截断 1 条/)
})

test('#4 就地改入参是禁区：原 body 与消息对象都不许被改', () => {
  const big = 'C'.repeat(40 * 1024)
  const body = baseBody([big])
  const originalContent = (body.messages as Record<string, unknown>[])[1]!.content
  enforceRequestBodyLimit(body, { limitBytes: LIMIT, keepRecentMessages: 0 })
  assert.equal((body.messages as Record<string, unknown>[])[1]!.content, originalContent, '入参消息必须原样')
  assert.ok(measureBodyBytes(body) > LIMIT, '原体仍是超限的原体')
})

test('#5 确定性：同一输入两次截断逐字节相同（前缀缓存硬要求）', () => {
  const body = baseBody(['D'.repeat(40 * 1024), 'E'.repeat(20 * 1024)])
  const a = JSON.stringify(enforceRequestBodyLimit(body, { limitBytes: LIMIT, keepRecentMessages: 0 }).body)
  const b = JSON.stringify(enforceRequestBodyLimit(body, { limitBytes: LIMIT, keepRecentMessages: 0 }).body)
  assert.equal(a, b)
})

test('#6 削完仍超限：抛可行动的中文错误并点名最大来源', () => {
  // 一条巨大的 user 消息（不许削）+ 一条可削的 tool → 削完依然超限。
  const body = baseBody(['F'.repeat(40 * 1024)], [{ role: 'user', content: 'G'.repeat(120 * 1024) }])
  assert.throws(
    () => enforceRequestBodyLimit(body, { limitBytes: LIMIT }),
    (err: unknown) => {
      assert.ok(err instanceof RequestBodyTooLargeError)
      assert.match(err.message, /超出传输上限/)
      assert.match(err.message, /\/compact/)
      assert.match(err.message, /中转/)
      assert.ok(err.contributors.length > 0, '要附最大来源清单')
      assert.equal(err.contributors[0]!.role, 'user', '最大来源应是那条不许削的 user 消息')
      return true
    },
  )
})

test('#6b 体积分档：KB 档不再四舍五入成 0.0MB（issue #251）', () => {
  assert.equal(formatByteSize(0), '0B')
  assert.equal(formatByteSize(512), '512B')
  assert.equal(formatByteSize(19_456), '19.0KB')
  assert.equal(formatByteSize(30 * 1024), '30.0KB')
  assert.equal(formatByteSize(4.7 * 1024 * 1024), '4.7MB')
})

test('#6c 无可截内容时不许声称「已截断历史工具输出」（issue #251）', () => {
  // 全是 user（无 tool 可削、也无图可移）→ 必须说「无可截断」，不许说「已截断」。
  const noCandidate = baseBody([], [{ role: 'user', content: 'U'.repeat(120 * 1024) }])
  assert.throws(
    () => enforceRequestBodyLimit(noCandidate, { limitBytes: LIMIT }),
    (err: unknown) => {
      assert.ok(err instanceof RequestBodyTooLargeError)
      assert.match(err.message, /已无可截断的历史内容/)
      assert.doesNotMatch(err.message, /已截断历史内容/)
      return true
    },
  )

  // 真削过历史 tool 还说超限，才允许说「已截断」。
  const truncated = baseBody(['F'.repeat(40 * 1024)], [{ role: 'user', content: 'G'.repeat(120 * 1024) }])
  assert.throws(
    () => enforceRequestBodyLimit(truncated, { limitBytes: LIMIT, keepRecentMessages: 0 }),
    (err: unknown) => {
      assert.ok(err instanceof RequestBodyTooLargeError)
      assert.match(err.message, /已截断历史工具输出\/移除较早图片仍超限/)
      return true
    },
  )
})

test('#6d 最大来源按整条消息字节计：reasoning_content 不再漏账（issue #251）', () => {
  const body = baseBody([], [
    { role: 'assistant', content: 'ok', reasoning_content: 'R'.repeat(120 * 1024) },
    { role: 'user', content: 'U'.repeat(60 * 1024) },
  ])
  assert.throws(
    () => enforceRequestBodyLimit(body, { limitBytes: LIMIT }),
    (err: unknown) => {
      assert.ok(err instanceof RequestBodyTooLargeError)
      const [top] = err.contributors
      assert.equal(top!.messageIndex, 1, '带 120KB reasoning_content 的 assistant 必须是最大来源')
      assert.ok(top!.bytes > 120 * 1024, `整条消息（含 reasoning_content）应 >120KB，实际 ${top!.bytes}`)
      assert.match(err.message, /messages\[1\] assistant 12\d\.\dKB/)
      assert.doesNotMatch(err.message, /0\.0MB/)
      return true
    },
  )
})

test('#6e 单条最大不足总量一成：明说「体积分散」而不是给一份解释不了的清单（issue #251）', () => {
  // 复刻 #251 的形状：230+ 条 19KB 消息共 >4MB，单条最大不足 1%。
  const messages: Record<string, unknown>[] = [{ role: 'system', content: 'sys' }]
  for (let i = 0; i < 230; i++) {
    messages.push({ role: i % 2 ? 'user' : 'assistant', content: 'M'.repeat(19 * 1024) })
  }
  assert.throws(
    () => enforceRequestBodyLimit({ model: 'm', messages }, { limitBytes: 4 * 1024 * 1024 }),
    (err: unknown) => {
      assert.ok(err instanceof RequestBodyTooLargeError)
      assert.match(err.message, /请求体 4\.\dMB 超出传输上限 4\.0MB/)
      assert.match(err.message, /体积分散在多条消息里/)
      assert.match(err.message, /不足 1%/)
      assert.doesNotMatch(err.message, /0\.0MB/)
      return true
    },
  )
})

test('#7 只削 tool：user / assistant 文本一律不动（图片另走 #13 驱逐）', () => {
  const body = baseBody(
    ['H'.repeat(40 * 1024)],
    [{ role: 'user', content: 'I'.repeat(20 * 1024) }, { role: 'assistant', content: 'J'.repeat(20 * 1024) }],
  )
  // 上限取 56KB：原体 80KB 超限，削掉历史 tool 的 37KB 后 ≈43KB 恰好落进来——
  // 而"不许削的 user+assistant"（40KB）必须原样留下，正是这条要钉的语义。
  const out = enforceRequestBodyLimit(body, { limitBytes: 56 * 1024, keepRecentMessages: 2 })
  const msgs = out.body.messages as Record<string, unknown>[]
  assert.equal(msgs[2]!.content, 'I'.repeat(20 * 1024))
  assert.equal(msgs[3]!.content, 'J'.repeat(20 * 1024))
  assert.ok(out.degraded.every((d) => d.messageIndex === 1))
})

test('#8 小于最小可截体积的 tool 结果不动（省不出多少，徒增缓存扰动）', () => {
  const small = 'K'.repeat(Math.floor(GUARD_MIN_TRUNCATABLE_BYTES / 2))
  // 用极小上限逼出"需要截断"的状态，但候选都太小 → 只能抛错
  assert.throws(() => enforceRequestBodyLimit(baseBody([small]), { limitBytes: 1024 }))
  const out = enforceRequestBodyLimit(baseBody([small]), { limitBytes: LIMIT })
  assert.deepEqual(out.degraded, [])
})

test('#9 逼近上限（未超限）：给出 nearLimit 警示而不是沉默', () => {
  // 1.2MB 体 / 2MB 上限（>50%）：还没到截断线，但第三方中转常有更小的上限——提前提醒。
  const big = 'N'.repeat(1_200_000)
  const body = baseBody([big])
  const out = enforceRequestBodyLimit(body, { limitBytes: 2 * 1024 * 1024 })
  assert.deepEqual(out.degraded, [], '未超限不许截断')
  assert.ok(out.nearLimit, '超过一半上限要给出 nearLimit')
  assert.equal(out.nearLimit!.bytes, out.bytes)
})

test('#10 远低于上限：既无降级也无 nearLimit（不许打扰用户）', () => {
  const out = enforceRequestBodyLimit(baseBody(['tiny']), { limitBytes: 2 * 1024 * 1024 })
  assert.equal(out.nearLimit, undefined)
  assert.deepEqual(out.degraded, [])
})

test('#11 降级通知文案：两种形态都点名下一步动作', () => {
  const degraded = formatBodyGuardNotice({ kind: 'degraded', bytes: 4_400_000, limitBytes: 4_194_304, degradedCount: 3, removedBytes: 120 * 1024 })
  assert.match(degraded, /已截断 3 条历史工具输出/)
  assert.match(degraded, /\/compact/)
  assert.match(degraded, /模型看不到被移除的内容/)

  // 图片驱逐：文案必须点名「图片」，不能拿工具输出的口径冒充。
  const imageOnly = formatBodyGuardNotice({ kind: 'degraded', bytes: 4_400_000, limitBytes: 4_194_304, degradedCount: 2, imagesEvicted: 2, removedBytes: 3_000_000 })
  assert.match(imageOnly, /已移除 2 张较早图片/)
  assert.doesNotMatch(imageOnly, /工具输出/)

  const mixed = formatBodyGuardNotice({ kind: 'degraded', bytes: 4_400_000, limitBytes: 4_194_304, degradedCount: 3, imagesEvicted: 1, removedBytes: 3_000_000 })
  assert.match(mixed, /已截断 2 条历史工具输出、移除 1 张较早图片/)

  const near = formatBodyGuardNotice({ kind: 'near-limit', bytes: 2_200_000, limitBytes: 4_194_304 })
  assert.match(near, /接近传输上限/)
  assert.match(near, /中转/)
  assert.match(near, /\/compact/)
})

test('#12 默认不启用护栏：不量体、不截断、原引用直通（issue #251 后续）', () => {
  // 5MB 的体 + 未配置 maxBodyBytes：必须零成本放行，而不是替用户猜一个上限。
  const body = baseBody(['M'.repeat(5 * 1024 * 1024)])
  const out = enforceRequestBodyLimit(body)
  assert.equal(out.body, body, '未配置上限：原引用直通')
  assert.equal(out.bytes, 0, '未启用时不做量体（量体要对整段 body 再 stringify 一遍）')
  assert.equal(out.limitBytes, Number.POSITIVE_INFINITY)
  assert.deepEqual(out.degraded, [])
  assert.equal(out.nearLimit, undefined, '未启用护栏不得产生 near-limit 预警')
})

// ── 图片驱逐（Grok Build `image_budget.rs` 同款：最老优先 + 模型可见占位符） ──

function userImageMsg(url: string, text = '看看这张图'): Record<string, unknown> {
  return { role: 'user', content: [{ type: 'text', text }, { type: 'image_url', image_url: { url } }] }
}

function pngDataUrl(payloadChars: number): string {
  return `data:image/png;base64,${'A'.repeat(payloadChars)}`
}

test('#13 工具削完仍超限：最老图片先移除，最新一轮图片保留', () => {
  const body = {
    model: 'm',
    messages: [
      { role: 'system', content: 'sys' },
      userImageMsg(pngDataUrl(20 * 1024), 'older'),
      { role: 'assistant', content: 'ok' },
      userImageMsg(pngDataUrl(20 * 1024), 'current'),
    ],
  }
  const limit = 26 * 1024
  const out = enforceRequestBodyLimit(body, { limitBytes: limit })
  const msgs = out.body.messages as Record<string, unknown>[]
  assert.equal(out.evictedImages, 1, '只应驱逐最老那张')
  const oldContent = msgs[1]!.content as Array<Record<string, unknown>>
  assert.equal(oldContent[1]!.type, 'text', '被驱逐的 image part 必须换成文本占位符')
  assert.match(String(oldContent[1]!.text), /no longer visible/)
  const newContent = msgs[3]!.content as Array<Record<string, unknown>>
  assert.equal(newContent[1]!.type, 'image_url', '最新一轮图片必须保留（最老优先驱逐）')
  assert.ok(out.bytes <= limit, '驱逐后必须落回上限内')
  assert.deepEqual(
    out.degraded.filter((d) => d.kind === 'image').map((d) => d.messageIndex),
    [1],
    '驱逐顺序 = 消息序（最老优先）',
  )
})

test('#14 有可截工具输出时不先动图片（可再取回 > 不可再取回）', () => {
  const body = {
    model: 'm',
    messages: [
      { role: 'system', content: 'sys' },
      { role: 'tool', tool_call_id: 't', content: 'T'.repeat(40 * 1024) },
      userImageMsg(pngDataUrl(20 * 1024), 'current'),
    ],
  }
  // 60KB → 削 tool 到 ~3KB 后落进 30KB；图片一张都不该动。
  const out = enforceRequestBodyLimit(body, { limitBytes: 30 * 1024, keepRecentMessages: 0 })
  assert.equal(out.evictedImages, 0, '工具输出可再取回，必须先削它')
  assert.ok(out.degraded.some((d) => d.kind === 'tool'))
  const msgs = out.body.messages as Record<string, unknown>[]
  const imageContent = msgs[2]!.content as Array<Record<string, unknown>>
  assert.equal(imageContent[1]!.type, 'image_url')
  assert.ok(out.bytes <= 30 * 1024)
})

test('#15 图片移完仍超限：抛错且如实说「移除较早图片仍超限」', () => {
  const body = {
    model: 'm',
    messages: [
      { role: 'system', content: 'sys' },
      userImageMsg(pngDataUrl(8 * 1024), 'u'),
      { role: 'user', content: 'X'.repeat(120 * 1024) },
    ],
  }
  assert.throws(
    () => enforceRequestBodyLimit(body, { limitBytes: LIMIT }),
    (err: unknown) => {
      assert.ok(err instanceof RequestBodyTooLargeError)
      assert.match(err.message, /已截断历史工具输出\/移除较早图片仍超限/)
      return true
    },
  )
})

test('#16 驱逐只改 wire 副本：原 body 的图片一字不动', () => {
  const body = {
    model: 'm',
    messages: [userImageMsg(pngDataUrl(30 * 1024), 'u')],
  }
  const before = JSON.stringify(body)
  const out = enforceRequestBodyLimit(body, { limitBytes: 24 * 1024 })
  assert.notEqual(out.body, body, '降级必须返回新体')
  assert.equal(JSON.stringify(body), before, '入参必须零改动（重入安全）')
})

test('#17 纯图 user 轮次被驱逐后仍是合法非空轮次（占位文本）', () => {
  const body = {
    model: 'm',
    messages: [{ role: 'user', content: [{ type: 'image_url', image_url: { url: pngDataUrl(30 * 1024) } }] }],
  }
  const out = enforceRequestBodyLimit(body, { limitBytes: 24 * 1024 })
  const content = (out.body.messages as Array<Record<string, unknown>>)[0]!.content as Array<Record<string, unknown>>
  assert.equal(content.length, 1)
  assert.equal(content[0]!.type, 'text')
  assert.ok(String(content[0]!.text).length > 0, '占位符不能是空串——空 user 轮次不合法')
  assert.ok(out.bytes <= 24 * 1024)
})

test('#18 图片驱逐同样确定：两次调用逐字节相同（前缀缓存）', () => {
  const make = () => ({ model: 'm', messages: [userImageMsg(pngDataUrl(30 * 1024), 'u')] })
  const a = enforceRequestBodyLimit(make(), { limitBytes: 24 * 1024 })
  const b = enforceRequestBodyLimit(make(), { limitBytes: 24 * 1024 })
  assert.equal(JSON.stringify(a.body), JSON.stringify(b.body), '同输入必须同字节，否则每轮碎缓存')
})

// ── #19 Anthropic Messages 形态：形态分叉，纪律不漂移 ─────────────────────────

/** anthropic 形态的历史工具结果：user 消息里的 tool_result block。 */
function anthropicToolResultMsg(content: string, id = 't1'): Record<string, unknown> {
  return { role: 'user', content: [{ type: 'tool_result', tool_use_id: id, content }] }
}

function anthropicImageMsg(dataUrl: string, extra: Record<string, unknown> = {}): Record<string, unknown> {
  const data = dataUrl.split(',')[1]!
  return {
    role: 'user',
    content: [{ type: 'image', source: { type: 'base64', media_type: 'image/png', data }, ...extra }],
  }
}

/** 尾部 6 条（= GUARD_KEEP_RECENT_MESSAGES）不可动消息，保证前面的候选落在可截断区。 */
function anthropicTail(): Record<string, unknown>[] {
  return Array.from({ length: GUARD_KEEP_RECENT_MESSAGES }, (_, i) => ({
    role: i % 2 === 0 ? 'user' : 'assistant',
    content: [{ type: 'text', text: `tail ${i}` }],
  }))
}

test('#19a anthropic：超限截断历史 tool_result（保头保尾 + 标注），产物可发', () => {
  const big = 'A'.repeat(60 * 1024)
  const body = { model: 'm', messages: [anthropicToolResultMsg(big), ...anthropicTail()] }
  const original = JSON.stringify(body)
  const out = enforceRequestBodyLimit(body, { limitBytes: LIMIT, shape: 'anthropic' })

  assert.ok(out.bytes <= LIMIT)
  assert.equal(out.degraded.length, 1)
  assert.equal(out.degraded[0]!.kind, 'tool')
  const block = ((out.body.messages as Array<Record<string, unknown>>)[0]!.content as Array<Record<string, unknown>>)[0]!
  assert.equal(block.type, 'tool_result', 'block 类型不能变（Anthropic 只认 tool_result）')
  const text = String(block.content)
  assert.ok(text.startsWith('A'.repeat(100)), '保头')
  assert.ok(text.includes('已省略约'), '标注点名省略量')
  assert.equal(JSON.stringify(body), original, '入参一字不动（wire 副本降级）')
})

test('#19b anthropic：tool_result 削完仍超限 → 驱逐 image block，占位符保 cache_control', () => {
  const body = {
    model: 'm',
    messages: [
      anthropicImageMsg(pngDataUrl(40 * 1024), { cache_control: { type: 'ephemeral', ttl: '1h' } }),
      ...anthropicTail(),
    ],
  }
  const out = enforceRequestBodyLimit(body, { limitBytes: LIMIT, shape: 'anthropic' })

  assert.ok(out.bytes <= LIMIT)
  assert.equal(out.evictedImages, 1)
  assert.equal(out.degraded[0]!.kind, 'image')
  const block = ((out.body.messages as Array<Record<string, unknown>>)[0]!.content as Array<Record<string, unknown>>)[0]!
  assert.equal(block.type, 'text', '图片换成文本占位符（模型要知道图没了）')
  assert.ok(String(block.text).includes('no longer visible'))
  // cache_control 挂在 block 上（BP3/BP4）：驱逐图片不能顺手丢掉断点。
  assert.deepEqual(block.cache_control, { type: 'ephemeral', ttl: '1h' })
})

test('#19c anthropic：无图可驱逐时如实抛错（不得谎称"已截断"）', () => {
  const body = {
    model: 'm',
    messages: [anthropicToolResultMsg('tiny'), ...anthropicTail()],
  }
  assert.throws(
    () => enforceRequestBodyLimit(body, { limitBytes: 200, shape: 'anthropic' }),
    (err: unknown) => {
      assert.ok(err instanceof RequestBodyTooLargeError)
      assert.ok(!/已截断/.test(err.message), '没降级过就不许说已截断')
      assert.ok(/messages\[\d+\]/.test(err.message), '要给出最大来源清单')
      return true
    },
  )
})

test('#19d anthropic：降级确定（两次调用逐字节相同）', () => {
  const make = () => ({
    model: 'm',
    messages: [anthropicToolResultMsg('B'.repeat(60 * 1024)), ...anthropicTail()],
  })
  const a = enforceRequestBodyLimit(make(), { limitBytes: LIMIT, shape: 'anthropic' })
  const b = enforceRequestBodyLimit(make(), { limitBytes: LIMIT, shape: 'anthropic' })
  assert.equal(JSON.stringify(a.body), JSON.stringify(b.body))
})

test('#19e 形态不混用：openai 形态看不到 anthropic block（不得误截）', () => {
  const body = { model: 'm', messages: [anthropicToolResultMsg('C'.repeat(60 * 1024)), ...anthropicTail()] }
  // 默认 openai 形态：tool_result 不是它认识的工具输出 → 只能抛错，绝不能瞎截到别的字段
  assert.throws(() => enforceRequestBodyLimit(body, { limitBytes: LIMIT }), RequestBodyTooLargeError)
})

// ── #20 上报节律（客户端共用） ───────────────────────────────────────────────

test('#20 notifyBodyGuard：降级只在集合变化时报一次，逼近上限每会话一次', () => {
  const notices: BodyGuardNotice[] = []
  const state = createBodyGuardNotifyState()
  const emit = (n: BodyGuardNotice) => notices.push(n)
  const degraded = {
    body: {},
    bytes: 100,
    limitBytes: 50,
    degraded: [{ messageIndex: 0, removedBytes: 10, kind: 'tool' as const }],
    evictedImages: 0,
  }

  notifyBodyGuard(degraded, state, emit)
  notifyBodyGuard(degraded, state, emit)
  assert.equal(notices.length, 1, '同一降级集合重复上报会把状态行刷成噪音')

  notifyBodyGuard(
    { ...degraded, degraded: [...degraded.degraded, { messageIndex: 1, removedBytes: 5, kind: 'tool' as const }] },
    state,
    emit,
  )
  assert.equal(notices.length, 2, '降级集合变化要再报一次')

  const nearLimit = { body: {}, bytes: 40, limitBytes: 50, degraded: [], evictedImages: 0, nearLimit: { bytes: 40, limitBytes: 50 } }
  notifyBodyGuard(nearLimit, state, emit)
  notifyBodyGuard(nearLimit, state, emit)
  assert.equal(notices.length, 3)
  assert.equal(notices[2]!.kind, 'near-limit')
})
