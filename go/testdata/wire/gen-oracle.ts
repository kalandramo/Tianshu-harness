// 端到端 wire oracle：用**真实 OpenAIClient** 捕获实际发送的请求体字节。
//
// 为什么不用手抄字段序：曾因手抄顺序写错（写成 messages→model→stream，
// 真实为 model→messages→stream），导致 golden 与 Go 实现自洽的假绿——
// 两边都错，测试照绿。真实客户端捕获是唯一可靠的 oracle。
//
// 运行：npx tsx go/testdata/wire/gen-oracle.ts
// 产出：go/testdata/wire/oracle.json
import { writeFileSync } from 'node:fs'
import { OpenAIClient, type OpenAIClientConfig } from '../../../src/api/openai-client.js'
import type { OaiChatRequest } from '../../../src/api/oai-types.js'
import type { StreamCallbacks } from '../../../src/api/stream-client.js'

const CONFIG: OpenAIClientConfig = {
  baseUrl: 'https://api.deepseek.com',
  apiKey: 'sk-test',
  model: 'deepseek-v4-pro',
  maxTokens: 8192,
  providerName: 'deepseek',
  thinking: 'disabled', // 关闭 thinking，聚焦基础字段序
  thinkingBlockType: 'enabled',
  effortFormat: 'reasoning_effort',
  preservedThinkingProtocol: true,
}

const REQUEST: OaiChatRequest = {
  model: 'deepseek-v4-pro',
  messages: [
    { role: 'system', content: '你是天枢。证据先行。' },
    { role: 'user', content: 'refactor this function' },
    {
      role: 'assistant',
      content: 'ok',
      tool_calls: [
        { id: 'c1', type: 'function', function: { name: 'read_file', arguments: '{"path":"a.ts"}' } },
      ],
    },
    { role: 'tool', tool_call_id: 'c1', content: 'line1\nline2' },
  ] as OaiChatRequest['messages'],
  tools: [
    {
      type: 'function',
      function: {
        name: 'read_file',
        description: 'Read a file',
        parameters: {
          type: 'object',
          properties: { path: { type: 'string' } },
          required: ['path'],
        },
      },
    },
    {
      type: 'function',
      function: {
        name: 'bash',
        description: 'Run a command <careful>',
        parameters: {
          type: 'object',
          properties: { command: { type: 'string' } },
          required: ['command'],
        },
      },
    },
  ],
  temperature: 0.7,
  max_tokens: 8192,
}

/** mock fetch，捕获客户端实际发送的原始 body 字符串（不经解析——保留字节序）。 */
async function captureRawBody(config: OpenAIClientConfig, request: OaiChatRequest): Promise<string> {
  const orig = globalThis.fetch
  let raw = ''
  globalThis.fetch = (async (_url: string, init: RequestInit) => {
    raw = init.body as string
    const stream = new ReadableStream({
      start(controller) {
        controller.enqueue(new TextEncoder().encode('data: [DONE]\n\n'))
        controller.close()
      },
    })
    return new Response(stream, { status: 200, headers: { 'content-type': 'text/event-stream' } })
  }) as unknown as typeof fetch
  try {
    const client = new OpenAIClient(config)
    const noop: StreamCallbacks = {
      onTextDelta: () => {},
      onThinkingDelta: () => {},
      onContentBlock: () => {},
      onStopReason: () => {},
      onError: () => {},
    }
    await client.stream(request, noop)
  } finally {
    globalThis.fetch = orig
  }
  return raw
}

const rawBody = await captureRawBody(CONFIG, REQUEST)
const parsed = JSON.parse(rawBody) as Record<string, unknown>

// 记录顶层键序（从原始字节解析顺序，用于诊断）
const keyOrder = Object.keys(parsed)

// 显式覆盖边界：thinking enabled + temperature 不注入
const configWithThinking: OpenAIClientConfig = { ...CONFIG, thinking: 'enabled' }
const rawWithThinking = await captureRawBody(configWithThinking, { ...REQUEST, temperature: undefined })

const out = {
  /** 真实客户端发送的请求体原始字节（本 oracle 的核心） */
  wire_raw: rawBody,
  /** 顶层键序（诊断用） */
  key_order: keyOrder,
  /** thinking=enabled 且 request.temperature 未设时的字节（验证 config.temperature 不被注入） */
  wire_thinking_enabled_no_temp: rawWithThinking,
}

writeFileSync(new URL('oracle.json', import.meta.url), JSON.stringify(out, null, 2) + '\n')
