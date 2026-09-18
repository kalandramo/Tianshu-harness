// 端到端字节对账：真实 oracle（src/api/stable-json.ts）vs Go 实现。
// 与单测的区别——单测对账的是我手写的期望值，这里对账的是真实 TS 代码的输出。
// 用真实请求体形态的载荷，覆盖 ASCII 键（生产实际）与混合键（边界）。
//
// 运行：npx tsx go/testdata/stablejson/gen-oracle.ts
// 产出：go/testdata/stablejson/oracle.json（Go 侧 golden_test.go 消费）
import { writeFileSync } from 'node:fs'
import { stableStringify } from '../../../src/api/stable-json.js'

const payloads: Array<[string, unknown]> = [
  ['real_chat_request', {
    model: 'deepseek-v4-pro',
    messages: [
      { role: 'system', content: '你是天枢。证据先行。' },
      { role: 'user', content: 'refactor this function' },
      { role: 'assistant', content: 'ok', tool_calls: [{ id: 'c1', type: 'function', function: { name: 'read_file', arguments: '{"path":"a.ts"}' } }] },
      { role: 'tool', tool_call_id: 'c1', content: 'line1\nline2' },
    ],
    tools: [
      { type: 'function', function: { name: 'read_file', description: 'Read a file', parameters: { type: 'object', properties: { path: { type: 'string' } }, required: ['path'] } } },
      { type: 'function', function: { name: 'bash', description: 'Run a command', parameters: { type: 'object', properties: { command: { type: 'string' } }, required: ['command'] } } },
    ],
    temperature: 0.7,
    max_tokens: 8192,
    stream: true,
  }],
  ['html_in_content', { content: '<div class="x">&amp;</div>', nested: { a: '<b>' } }],
  ['numbers', { a: 1, b: 0.1, c: 1e21, d: 1e-7, e: 0.3333333333333333, f: -0, g: 100, h: 1e20 }],
  ['mixed_keys', { '中文键': 1, ascii: 2, '🎉': 3, Z: 4, '\uE000': 5, '\u{10000}': 6 }],
  ['deep_nesting', { a: { b: { c: { d: { e: [1, [2, [3]]] } } } } }],
  ['empty_containers', { o: {}, a: [], n: null, s: '', z: 0, f: false }],
  ['unicode_escapes', { t: 'a\tb\nc"d\\e', u: '\u2028\u2029', c: '\u0000\u001f' }],
  ['tool_schema_like', { type: 'object', properties: { pattern: { type: 'string', description: 'Regex, e.g. <a>&</a>' } }, required: [], additionalProperties: false }],
]

const out: Record<string, string> = {}
for (const [name, val] of payloads) {
  out[name] = stableStringify(val)
}
// 直接写盘：本脚本是夹具生成器（非调试探针），输出目标是 golden 文件本身。
// 用 JSON.stringify 缩进保证 golden 可读、可 diff。
writeFileSync(
  new URL('oracle.json', import.meta.url),
  JSON.stringify(out, null, 2) + '\n',
)
