// 关键修正探针：真实发送路径用的是 JSON.stringify(effectiveBody)，不是 stableStringify。
// 必须对账真实序列化器，否则 Go/No-Go 判决锚错对象。
//
// 运行：npx tsx go/testdata/wire/gen-oracle.ts
import { writeFileSync } from 'node:fs'

// 复现 src/api/openai-client.ts:430+ 的 body 构造顺序（逐字段赋值 = 插入顺序固定）
function buildBody(request: Record<string, unknown>, config: Record<string, unknown>): Record<string, unknown> {
  const body: Record<string, unknown> = {}
  // 顺序严格照抄 openai-client.ts 的赋值序
  body.messages = request.messages
  body.model = request.model || config.model
  body.stream = true
  if (config.maxCompletionTokens) {
    body.max_completion_tokens = request.max_tokens ?? config.maxTokens
  } else {
    body.max_tokens = request.max_tokens ?? config.maxTokens
  }
  if (config.streamOptions) {
    body.stream_options = { include_usage: true }
  }
  if (Array.isArray(request.tools) && request.tools.length > 0) {
    body.tools = request.tools
    if (request.tool_choice) body.tool_choice = request.tool_choice
  }
  if (request.response_format) {
    body.response_format = request.response_format
  } else if (config.supportsResponseFormat) {
    body.response_format = { type: 'json_object' }
  }
  if (request.temperature !== undefined) {
    body.temperature = request.temperature
  } else if (config.temperature !== undefined) {
    body.temperature = config.temperature
  }
  return body
}

const request = {
  model: 'deepseek-v4-pro',
  messages: [
    { role: 'system', content: '你是天枢。证据先行。' },
    { role: 'user', content: 'refactor this function' },
    {
      role: 'assistant',
      content: 'ok',
      tool_calls: [{ id: 'c1', type: 'function', function: { name: 'read_file', arguments: '{"path":"a.ts"}' } }],
    },
    { role: 'tool', tool_call_id: 'c1', content: 'line1\nline2' },
  ],
  tools: [
    {
      type: 'function',
      function: {
        name: 'read_file',
        description: 'Read a file',
        parameters: { type: 'object', properties: { path: { type: 'string' } }, required: ['path'] },
      },
    },
    {
      type: 'function',
      function: {
        name: 'bash',
        description: 'Run a command <careful>',
        parameters: { type: 'object', properties: { command: { type: 'string' } }, required: ['command'] },
      },
    },
  ],
  temperature: 0.7,
  max_tokens: 8192,
}

const config = {
  model: 'deepseek-v4-pro',
  maxTokens: 8192,
  maxCompletionTokens: false,
  streamOptions: true,
  supportsResponseFormat: false,
  temperature: 0.7,
}

const body = buildBody(request, config)

const out: Record<string, string> = {
  // 真实发送路径
  wire_json_stringify: JSON.stringify(body),
  // 对照：若用 stableStringify 会是什么样（键序不同）
  _contrast_stableStringify_keyOrder: JSON.stringify(Object.keys(body)),
}

writeFileSync(new URL('oracle.json', import.meta.url), JSON.stringify(out, null, 2) + '\n')
