// 端到端 wire oracle：用**真实 OpenAIClient** 捕获实际发送的请求体字节。
//
// 为什么不用手抄字段序：曾因手抄顺序写错（写成 messages→model→stream，
// 真实为 model→messages→stream），导致 golden 与 Go 实现自洽的假绿——
// 两边都错，测试照绿。真实客户端捕获是唯一可靠的 oracle。
//
// 运行：node_modules/.bin/tsx go/testdata/wire/gen-oracle.ts
// 产出：go/testdata/wire/oracle.json
import { writeFileSync } from 'node:fs'
import { OpenAIClient, type OpenAIClientConfig } from '../../../src/api/openai-client.js'
import type { OaiChatRequest } from '../../../src/api/oai-types.js'
import type { StreamCallbacks } from '../../../src/api/stream-client.js'

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

// ── 基础配置（thinking 关闭，聚焦基础字段序） ──
const BASE_CONFIG: OpenAIClientConfig = {
  baseUrl: 'https://api.deepseek.com',
  apiKey: 'sk-test',
  model: 'deepseek-v4-pro',
  maxTokens: 8192,
  providerName: 'deepseek',
  thinking: 'disabled',
  thinkingBlockType: 'enabled',
  effortFormat: 'reasoning_effort',
  preservedThinkingProtocol: true,
}

const BASE_REQUEST: OaiChatRequest = {
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

const cases: Array<{ name: string; config: OpenAIClientConfig; request: OaiChatRequest }> = [
  // 1. 基础：model→messages→stream→max_tokens→stream_options→tools→temperature
  { name: 'base', config: BASE_CONFIG, request: BASE_REQUEST },

  // 2. 无 tools（验证 tools 字段缺席，不是空数组）
  {
    name: 'no_tools',
    config: BASE_CONFIG,
    request: { ...BASE_REQUEST, tools: undefined },
  },

  // 3. thinking=enabled + effort：验证 thinking 块与 reasoning_effort 并存，
  //    且 config.temperature 不被注入（request.temperature 也给 undefined）
  {
    name: 'thinking_enabled_with_effort',
    config: {
      ...BASE_CONFIG,
      thinking: 'enabled',
      reasoningEffort: 'max',
      temperature: 0.9, // 应被抑制
    },
    request: { ...BASE_REQUEST, temperature: undefined },
  },

  // 4. thinking=enabled 但 blockType=none（OpenAI 式：只有 reasoning_effort）
  {
    name: 'thinking_enabled_no_block',
    config: {
      ...BASE_CONFIG,
      providerName: 'openai',
      thinking: 'enabled',
      thinkingBlockType: 'none',
      reasoningEffort: 'high',
    },
    request: { ...BASE_REQUEST, temperature: undefined },
  },

  // 5. effortCap 钳制（Codex: max→xhigh）
  {
    name: 'effort_cap_clamp',
    config: {
      ...BASE_CONFIG,
      providerName: 'codex',
      thinking: 'enabled',
      thinkingBlockType: 'none',
      reasoningEffort: 'max',
      effortCap: { max: 'xhigh' },
    },
    request: { ...BASE_REQUEST, temperature: undefined },
  },

  // 6. max_completion_tokens 分支（MiMo）
  {
    name: 'max_completion_tokens',
    config: { ...BASE_CONFIG, useMaxCompletionTokens: true },
    request: BASE_REQUEST,
  },

  // 7. stream_options 被提供商拒绝
  {
    name: 'no_stream_options',
    config: { ...BASE_CONFIG, unsupported: ['stream_options'] },
    request: BASE_REQUEST,
  },

  // 8. jsonMode 回退
  {
    name: 'json_mode',
    config: { ...BASE_CONFIG, jsonMode: true },
    request: { ...BASE_REQUEST, response_format: undefined },
  },

  // 9. request.response_format 优先于 jsonMode
  {
    name: 'request_response_format_wins',
    config: { ...BASE_CONFIG, jsonMode: true },
    request: { ...BASE_REQUEST, response_format: { type: 'json_schema', name: 'x' } },
  },

  // 10. 非保留式提供商 + thinking=disabled → 不注入系统后缀
  {
    name: 'system_suffix',
    config: { ...BASE_CONFIG, preservedThinkingProtocol: false },
    request: BASE_REQUEST,
  },

  // 11. tool_choice
  {
    name: 'tool_choice',
    config: BASE_CONFIG,
    request: { ...BASE_REQUEST, tool_choice: 'required' },
  },

  // 12. reasoning_split（MiniMax）
  {
    name: 'reasoning_split',
    config: { ...BASE_CONFIG, providerName: 'minimax', reasoningSplit: true },
    request: BASE_REQUEST,
  },

  // 13. 保留式思考 + tool_calls 且 reasoning_content 缺失 → 应补空串
  {
    name: 'preserved_thinking_missing_reasoning',
    config: { ...BASE_CONFIG, thinking: 'enabled' },
    request: {
      ...BASE_REQUEST,
      messages: [
        { role: 'user', content: 'hi' },
        {
          role: 'assistant',
          content: '',
          tool_calls: [
            { id: 'c1', type: 'function', function: { name: 'read_file', arguments: '{}' } },
          ],
        },
      ] as OaiChatRequest['messages'],
      temperature: undefined,
    },
  },

  // 14. 非保留式提供商 + reasoning_content 存在 → 应剥离
  {
    name: 'strip_reasoning_non_preserved',
    config: { ...BASE_CONFIG, providerName: 'glm', preservedThinkingProtocol: false },
    request: {
      ...BASE_REQUEST,
      messages: [
        { role: 'user', content: 'hi' },
        {
          role: 'assistant',
          content: 'answer',
          reasoning_content: 'thinking...',
        },
      ] as OaiChatRequest['messages'],
    },
  },

  // 15. budget_tokens（Claude 式块内档位编码）
  {
    name: 'budget_tokens',
    config: {
      ...BASE_CONFIG,
      providerName: 'claude',
      thinking: 'enabled',
      thinkingBlockType: 'enabled',
      thinkingBudgetField: 'budget_tokens',
      reasoningEffort: 'high',
    },
    request: { ...BASE_REQUEST, temperature: undefined },
  },
]

const out: Record<string, unknown> = {}
for (const c of cases) {
  const raw = await captureRawBody(c.config, c.request)
  out[c.name] = { raw, key_order: Object.keys(JSON.parse(raw) as Record<string, unknown>) }
}

writeFileSync(new URL('oracle.json', import.meta.url), JSON.stringify(out, null, 2) + '\n')
