// SSE 解析 oracle：喂真实 OpenAIClient 一段构造的 SSE 流，捕获回调序列。
//
// 与手抄事件格式的区别：手抄会编码「我以为的」协议形状；真实客户端跑一遍
// 才能捕获实际语义（通道重归类、两阶段 flush、续块归属、usage 校准）。
//
// 运行：node_modules/.bin/tsx go/testdata/sse/gen-oracle.ts
// 产出：go/testdata/sse/oracle.json
import { writeFileSync } from 'node:fs'
import { OpenAIClient, type OpenAIClientConfig } from '../../../src/api/openai-client.js'
import type { OaiChatRequest } from '../../../src/api/oai-types.js'
import type { StreamCallbacks } from '../../../src/api/stream-client.js'

/** 一条 SSE 事件记录（捕获回调调用）。 */
type Event =
  | { kind: 'text'; value: string }
  | { kind: 'thinking'; value: string }
  | { kind: 'block'; blockType: string; text?: string; id?: string; name?: string; input?: unknown; argsTruncated?: boolean }
  | { kind: 'stop'; reason: string; usage?: Record<string, unknown> }

/**
 * 用给定 SSE 分片喂真实客户端，捕获回调序列。
 *
 * chunks 是**原始 SSE 文本分片**（会被逐片 enqueue），用于验证跨片缓冲。
 */
async function captureEvents(
  config: OpenAIClientConfig,
  request: OaiChatRequest,
  chunks: string[],
): Promise<Event[]> {
  const orig = globalThis.fetch
  const events: Event[] = []
  globalThis.fetch = (async () => {
    const encoder = new TextEncoder()
    const stream = new ReadableStream({
      start(controller) {
        for (const c of chunks) controller.enqueue(encoder.encode(c))
        controller.close()
      },
    })
    return new Response(stream, { status: 200, headers: { 'content-type': 'text/event-stream' } })
  }) as unknown as typeof fetch

  try {
    const client = new OpenAIClient(config)
    const callbacks: StreamCallbacks = {
      onTextDelta: (t) => events.push({ kind: 'text', value: t }),
      onThinkingDelta: (t) => events.push({ kind: 'thinking', value: t }),
      onContentBlock: (b) => {
        if (b.type === 'text') events.push({ kind: 'block', blockType: 'text', text: b.text })
        else if (b.type === 'thinking') events.push({ kind: 'block', blockType: 'thinking', text: b.thinking })
        else if (b.type === 'tool_use') {
          events.push({
            kind: 'block', blockType: 'tool_use', id: b.id, name: b.name,
            input: b.input, argsTruncated: (b as { argsTruncated?: boolean }).argsTruncated,
          })
        }
      },
      onStopReason: (reason, usage) => {
        events.push({ kind: 'stop', reason, usage: usage as Record<string, unknown> })
      },
      onError: () => {},
    }
    await client.stream(request, callbacks)
  } finally {
    globalThis.fetch = orig
  }
  return events
}

/** 构造一条 SSE data 行。 */
function sse(obj: unknown): string {
  return `data: ${JSON.stringify(obj)}\n\n`
}

const CONFIG: OpenAIClientConfig = {
  baseUrl: 'https://api.deepseek.com',
  apiKey: 'sk-test',
  model: 'deepseek-v4-pro',
  maxTokens: 8192,
  providerName: 'deepseek',
  thinking: 'disabled',
  thinkingBlockType: 'enabled',
  effortFormat: 'reasoning_effort',
}

const REQUEST: OaiChatRequest = {
  model: 'deepseek-v4-pro',
  messages: [{ role: 'user', content: 'hi' }] as OaiChatRequest['messages'],
  max_tokens: 100,
}

// ── 场景定义 ──

/** 1. 纯文本回复 + 独立 usage 块 */
const textOnly = [
  sse({ choices: [{ delta: { content: 'Hello' } }] }),
  sse({ choices: [{ delta: { content: ' world' } }] }),
  sse({ choices: [{ delta: {}, finish_reason: 'stop' }] }),
  sse({ choices: [], usage: { prompt_tokens: 100, completion_tokens: 5, prompt_cache_hit_tokens: 80, prompt_cache_miss_tokens: 20 } }),
  'data: [DONE]\n\n',
]

/** 2. reasoning_content 通道 + content（通道单调性） */
const reasoningThenText = [
  sse({ choices: [{ delta: { reasoning_content: '思考中' } }] }),
  sse({ choices: [{ delta: { reasoning_content: '...' } }] }),
  sse({ choices: [{ delta: { content: '答案' } }] }),
  sse({ choices: [{ delta: {}, finish_reason: 'stop' }] }),
  'data: [DONE]\n\n',
]

/** 3. content 之后又来 reasoning_content → 应重归类为 text */
const reasoningAfterContent = [
  sse({ choices: [{ delta: { content: '先说话' } }] }),
  sse({ choices: [{ delta: { reasoning_content: '后到的推理' } }] }),
  sse({ choices: [{ delta: {}, finish_reason: 'stop' }] }),
  'data: [DONE]\n\n',
]

/** 4. 单工具调用，参数分片 */
const singleToolCall = [
  sse({ choices: [{ delta: { tool_calls: [{ index: 0, id: 'call_1', type: 'function', function: { name: 'read_file', arguments: '' } }] } }] }),
  sse({ choices: [{ delta: { tool_calls: [{ index: 0, function: { arguments: '{"path"' } }] } }] }),
  sse({ choices: [{ delta: { tool_calls: [{ index: 0, function: { arguments: ':"a.ts"}' } }] } }] }),
  sse({ choices: [{ delta: {}, finish_reason: 'tool_calls' }] }),
  'data: [DONE]\n\n',
]

/** 5. 并行工具调用（两个 index） */
const parallelToolCalls = [
  sse({ choices: [{ delta: { tool_calls: [{ index: 0, id: 'c0', type: 'function', function: { name: 'read_file', arguments: '{"path":"a"}' } }] } }] }),
  sse({ choices: [{ delta: { tool_calls: [{ index: 1, id: 'c1', type: 'function', function: { name: 'grep', arguments: '{"pattern":"x"}' } }] } }] }),
  sse({ choices: [{ delta: {}, finish_reason: 'tool_calls' }] }),
  'data: [DONE]\n\n',
]

/**
 * 6. finish_reason 之后的无 index 续块 → 应 reattach 到唯一打开的缓冲。
 * 这是跨工具污染事故（oh-my-pi/384919c7）的相邻场景：单个打开时按唯一性归属。
 */
const trailingArgsAfterFinish = [
  sse({ choices: [{ delta: { tool_calls: [{ index: 0, id: 'c0', type: 'function', function: { name: 'read_file', arguments: '{"path":"a' } }] } }] }),
  sse({ choices: [{ delta: {}, finish_reason: 'tool_calls' }] }),
  // finish_reason 之后的续块，无 index 无 id
  sse({ choices: [{ delta: { tool_calls: [{ function: { arguments: '.ts"}' } }] } }] }),
  'data: [DONE]\n\n',
]

/**
 * 7. 两个缓冲打开 + 无 index 无 id 的续块 → 应 DROP（fail-safe），不误嫁。
 * 这是跨工具污染事故的核心场景。
 */
const ambiguousContinuation = [
  sse({ choices: [{ delta: { tool_calls: [{ index: 0, id: 'c0', type: 'function', function: { name: 'read_file', arguments: '{"path":"a' } }] } }] }),
  sse({ choices: [{ delta: { tool_calls: [{ index: 1, id: 'c1', type: 'function', function: { name: 'grep', arguments: '{"pattern":"x' } }] } }] }),
  sse({ choices: [{ delta: {}, finish_reason: 'tool_calls' }] }),
  // 无 index 无 id，两个缓冲打开 → 必须丢弃
  sse({ choices: [{ delta: { tool_calls: [{ function: { arguments: '"}' } }] } }] }),
  'data: [DONE]\n\n',
]

/** 8. 参数截断（流中断，参数永远不完整）→ 应产出 argsTruncated:true 的空 input */
const truncatedArgs = [
  sse({ choices: [{ delta: { tool_calls: [{ index: 0, id: 'c0', type: 'function', function: { name: 'bash', arguments: '{"command":"rm -rf ' } }] } }] }),
  sse({ choices: [{ delta: {}, finish_reason: 'tool_calls' }] }),
  'data: [DONE]\n\n',
]

/** 9. finish_reason 与 usage 同块（DeepSeek 式） */
const usageWithFinish = [
  sse({ choices: [{ delta: { content: 'ok' } }] }),
  sse({
    choices: [{ delta: {}, finish_reason: 'stop' }],
    usage: { prompt_tokens: 50, completion_tokens: 3, prompt_cache_hit_tokens: 40 },
  }),
  'data: [DONE]\n\n',
]

/** 10. 跨片分割（SSE 行被切在中间）——验证缓冲 */
const splitAcrossChunks = [
  'data: {"choices":[{"delta":{"cont',
  'ent":"分割"}}]}\n\ndata: {"choices":[{"delta":{},',
  '"finish_reason":"stop"}]}\n\n',
  'data: [DONE]\n\n',
]

/** 11. 心跳注释行（`:` 开头）与空行——不应被当作数据事件 */
const heartbeatLines = [
  ': keepalive\n\n',
  sse({ choices: [{ delta: { content: 'hi' } }] }),
  ':\n\n',
  sse({ choices: [{ delta: {}, finish_reason: 'stop' }] }),
  'data: [DONE]\n\n',
]

/** 12. reasoning_tokens 透传 */
const reasoningTokens = [
  sse({ choices: [{ delta: { content: 'x' } }] }),
  sse({
    choices: [{ delta: {}, finish_reason: 'stop' }],
    usage: { prompt_tokens: 10, completion_tokens: 20, completion_tokens_details: { reasoning_tokens: 15 } },
  }),
  'data: [DONE]\n\n',
]

const scenarios: Array<{ name: string; chunks: string[]; config?: Partial<OpenAIClientConfig> }> = [
  { name: 'text_only', chunks: textOnly },
  { name: 'reasoning_then_text', chunks: reasoningThenText },
  { name: 'reasoning_after_content', chunks: reasoningAfterContent },
  { name: 'single_tool_call', chunks: singleToolCall },
  { name: 'parallel_tool_calls', chunks: parallelToolCalls },
  { name: 'trailing_args_after_finish', chunks: trailingArgsAfterFinish },
  { name: 'ambiguous_continuation', chunks: ambiguousContinuation },
  { name: 'truncated_args', chunks: truncatedArgs },
  { name: 'usage_with_finish', chunks: usageWithFinish },
  { name: 'split_across_chunks', chunks: splitAcrossChunks },
  { name: 'heartbeat_lines', chunks: heartbeatLines },
  { name: 'reasoning_tokens', chunks: reasoningTokens },
]

const out: Record<string, Event[]> = {}
for (const sc of scenarios) {
  out[sc.name] = await captureEvents({ ...CONFIG, ...sc.config }, REQUEST, sc.chunks)
}

writeFileSync(new URL('oracle.json', import.meta.url), JSON.stringify(out, null, 2) + '\n')
