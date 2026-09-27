import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { OpenAIClient } from '../openai-client.js'
import { classifyApiError, errorRecoveryGuidance } from '../error-classifier.js'
import type { StreamCallbacks } from '../stream-client.js'

// A custom ID verifies provider-level protection without a model-name allowlist.
const model = 'deepseek-custom-flash'
const config = {
  apiKey: 'test', baseUrl: 'https://api.deepseek.com', model,
  maxTokens: 4096, providerName: 'deepseek', thinking: 'enabled' as const,
}
const frame = (delta: Record<string, unknown>, finish_reason?: string) => `data: ${JSON.stringify({ choices: [{ delta, finish_reason }] })}\n\n`

async function parse(deltas: Record<string, unknown>[], options: { residual?: boolean; provider?: string; prefix?: string; finishAt?: number; onThinking?: (s: string) => void } = {}) {
  const thinking: string[] = []
  const blocks: unknown[] = []
  const aborted: unknown[] = []
  let cancelled = false
  const stream = new ReadableStream<Uint8Array>({
    start(c) {
      const wire = (options.prefix ?? '') + deltas.map((delta, i) => frame(delta, i === options.finishAt ? 'tool_calls' : undefined)).join('')
      c.enqueue(new TextEncoder().encode(options.residual ? wire.trimEnd() : wire + 'data: [DONE]\n\n'))
      // Residual parsing runs on EOF; other tests leave the transport open to
      // verify that stopping a degenerate response releases its reader.
      if (options.residual) c.close()
    },
    cancel() { cancelled = true },
  })
  const client = new OpenAIClient({ ...config, providerName: options.provider ?? 'deepseek' })
  let error: unknown
  try {
    await client.parseStreamFromReader(stream.getReader(), {
      onTextDelta() {}, onThinkingDelta(s) { thinking.push(s); options.onThinking?.(s) },
      onContentBlock(b) { blocks.push(b) }, onStopReason() {},
      onStreamAttemptAborted(info) { aborted.push(info) },
    })
  } catch (e) { error = e }
  return { error, thinking: thinking.join(''), blocks, aborted, cancelled }
}

describe('DeepSeek streaming reasoning repetition', () => {
  it('stops hundreds of short repeated reasoning lines before committing a completed turn', async () => {
    const result = await parse(Array.from({ length: 300 }, () => ({ reasoning_content: '好。\n' })))
    assert.ok(result.error instanceof Error)
    assert.equal(result.error.name, 'ReasoningRepetitionError')
    assert.ok(result.thinking.length < 900)
    assert.equal(result.blocks.length, 0)
    assert.equal(result.aborted.length, 1)
    assert.equal(result.cancelled, true)
    // 2026-09-24 起：复读命中不再一次即终态——分类器放行**一次**纠正重试
    // （丢掉退化推理 + 尾部纠正指令，见下一个 describe）；第二次仍命中才冒泡。
    const classified = classifyApiError(result.error)
    assert.equal(classified.retryable, true, '给一次纠正重试的机会')
    assert.equal(classified.maxRetries, 1, '只给一次——纠正无效就别再无限重试')
    assert.equal(classified.reasoningRepeatCorrect, true, '重试要走「丢退化推理 + 纠正」而不是纯重发')
    assert.equal(classified.shouldReconnect, false)
    assert.match(errorRecoveryGuidance(result.error), /重复/)
  })

  it('recognizes a short-phrase cycle across individual character deltas', async () => {
    const text = '好。\n好，我发送。\n（发送）\n'.repeat(80)
    const result = await parse([...text].map(reasoning_content => ({ reasoning_content })))
    assert.equal((result.error as Error)?.name, 'ReasoningRepetitionError')
  })

  it('also checks the final SSE event without a newline', async () => {
    const result = await parse([{ reasoning_content: '好。\n'.repeat(300) }], { residual: true })
    assert.equal((result.error as Error)?.name, 'ReasoningRepetitionError')
  })

  it('preserves healthy long reasoning and occasional repeated phrases byte-for-byte', async () => {
    const text = Array.from({ length: 300 }, (_, i) => `步骤 ${i}：核对不同证据，继续分析。\n好。\n`).join('')
    const result = await parse([{ reasoning_content: text }, { content: '完成' }])
    assert.equal(result.error, undefined)
    assert.equal(result.thinking, text)
    assert.deepEqual(result.blocks[0], { type: 'thinking', thinking: text })
  })

  it('leaves short acknowledgements, other providers and answer text alone', async () => {
    assert.equal((await parse([{ reasoning_content: '好。\n'.repeat(12) }])).error, undefined)
    assert.equal((await parse([{ reasoning_content: '好。\n'.repeat(300) }], { provider: 'mimo' })).error, undefined)
    assert.equal((await parse([{ content: '好。\n'.repeat(300) }])).error, undefined)
  })

  // ── 阈值放宽（2026-09-24）──────────────────────────────────────────────
  // 4.1 flash 线在长对话里会成段输出「重复短句」式推理（别的 agent 也遇到过），
  // 那是这个模型的文风而非「卡死」，硬停等于把整轮工作丢掉。判据从
  // 「≤4 个短句占 ≥90%」放宽到「≤3 个短句占 ≥95%」——下面两条钉住被放过的形态，
  // 上面那两条钉住仍然要拦的形态（1 句与 3 句原地打转、EOF 残余帧）。

  it('tolerates a rotating chant of four or more short phrases (no longer a stop signal)', async () => {
    const cycle = '再看一下。\n马上就好。\n检查完毕。\n继续推进。\n'.repeat(80)
    const result = await parse([{ reasoning_content: cycle }, { content: '完成' }])
    assert.equal(result.error, undefined)
    assert.equal(result.thinking, cycle, '整段推理原样保留，不被截断')
  })

  it('tolerates a three-phrase chant with a few novel short lines mixed in', async () => {
    // 每 16 行夹一句新短句（8/128 ≈ 6%），覆盖率跌破 95% 即不再中止。
    const line = (i: number) => (i % 16 === 0
      ? `另外还有第 ${i} 点要确认。\n`
      : ['再看一下。', '马上就好。', '检查完毕。'][i % 3] + '\n')
    const text = Array.from({ length: 300 }, (_, i) => line(i)).join('')
    const result = await parse([{ reasoning_content: text }, { content: '完成' }])
    assert.equal(result.error, undefined)
    assert.equal(result.thinking, text)
  })

  it('does not classify late reasoning after tool-call progress as a thinking-only loop', async () => {
    const result = await parse([
      { tool_calls: [{ index: 0, id: 'call_1', type: 'function', function: { name: 'read_file', arguments: '{"path":"a"}' } }] },
      { reasoning_content: '好。\n'.repeat(300) },
    ])
    assert.equal(result.error, undefined)
  })

  it('does not re-arm the guard after finish_reason flushes a completed tool call', async () => {
    const result = await parse([
      { reasoning_content: '好。\n'.repeat(127) },
      { tool_calls: [{ index: 0, id: 'call_1', type: 'function', function: { name: 'read_file', arguments: '{"path":"a"}' } }] },
      { reasoning_content: '好。\n' },
    ], { finishAt: 1 })
    assert.equal(result.error, undefined)
    assert.ok(result.blocks.some(b => (b as { type: string }).type === 'tool_use'))
    assert.ok(result.blocks.some(b => (b as { type: string }).type === 'thinking'))
  })

  it('skips malformed JSON and structurally empty events', async () => {
    const result = await parse([{ reasoning_content: 'normal' }], {
      prefix: 'data: {bad json\n\ndata: null\n\ndata: {"choices":[{}]}\n\n',
    })
    assert.equal(result.error, undefined)
    assert.equal(result.thinking, 'normal')
  })

  it('preserves reasoning arriving together with the first content delta', async () => {
    const result = await parse([{ reasoning_content: '思考完成', content: '完成' }])
    assert.equal(result.error, undefined)
    assert.deepEqual(result.blocks[0], { type: 'thinking', thinking: '思考完成' })
  })

  it('propagates consumer errors instead of swallowing them', async () => {
    const expected = new Error('consumer failed')
    const result = await parse([{ reasoning_content: 'normal' }], { onThinking() { throw expected } })
    assert.equal(result.error, expected)
  })

  // 2026-09-24 改判（issue #260）：这条原本钉的是「命中即终态、一次都不重试」。
  // 现在改成「**先纠正重试一次**，第二次仍命中才终态」——不变的那一半是
  // 「绝不回放退化推理」（回放会强化循环），本条把它钉得更死了。
  it('retries once with a corrective tail, and never re-injects the degenerate prefix', async () => {
    const original = globalThis.fetch
    const bodies: Array<Record<string, unknown>> = []
    globalThis.fetch = (async (_url: string, init: RequestInit) => {
      bodies.push(JSON.parse(String(init.body)) as Record<string, unknown>)
      return new Response(frame({ reasoning_content: '好。\n'.repeat(300) }) + 'data: [DONE]\n\n', {
        headers: { 'content-type': 'text/event-stream' },
      })
    }) as unknown as typeof fetch
    try {
      const noop = () => {}
      const cb: StreamCallbacks = { onTextDelta: noop, onThinkingDelta: noop, onContentBlock: noop, onStopReason: noop, onError: noop }
      await assert.rejects(new OpenAIClient(config).stream({ model, messages: [{ role: 'user', content: 'hi' }], max_tokens: 4096 }, cb), { name: 'ReasoningRepetitionError' })
      assert.equal(bodies.length, 2, '第一次命中 + 恰好一次纠正重试；第二次仍命中即终态，不无限重试')
      const second = JSON.stringify(bodies[1])
      assert.match(second, /reasoning-repeat/, '重发必须带上纠正指令')
      assert.doesNotMatch(second, /好。\\n/, '重发绝不能回放那段退化推理')
    } finally { globalThis.fetch = original }
  })

  it('a corrective retry that comes back healthy keeps the turn alive', async () => {
    const original = globalThis.fetch
    let attempt = 0
    globalThis.fetch = (async () => {
      attempt++
      const payload = attempt === 1
        ? frame({ reasoning_content: '好。\n'.repeat(300) }) + 'data: [DONE]\n\n'
        : frame({ reasoning_content: '换一条思路核对。\n' }) + frame({ content: '完成' }, 'stop') + 'data: [DONE]\n\n'
      return new Response(payload, { headers: { 'content-type': 'text/event-stream' } })
    }) as unknown as typeof fetch
    try {
      const text: string[] = []
      await new OpenAIClient(config).stream(
        { model, messages: [{ role: 'user', content: 'hi' }], max_tokens: 4096 },
        {
          onTextDelta: (s) => text.push(s), onThinkingDelta: () => {}, onContentBlock: () => {},
          onStopReason: () => {}, onError: () => {},
        },
      )
      assert.equal(attempt, 2, '命中一次 → 纠正重发一次')
      assert.equal(text.join(''), '完成', '纠正后这一轮照常产出，不再整轮丢掉')
    } finally { globalThis.fetch = original }
  })
})
