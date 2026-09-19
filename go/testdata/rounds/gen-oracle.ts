// 端到端对账：真实 oracle（src/context/rounds.ts）vs Go 实现。
//
// **不做手写期望值**——一律从真实 TS 代码路径导出。手抄会引入自洽假绿
// （本项目已因手抄 wire 字段序出过一次事故）。
//
// 运行（必须在**仓库根**，node_modules 在根下）：
//   node_modules/.bin/tsx go/testdata/rounds/gen-oracle.ts
// 产出：go/testdata/rounds/oracle.json（Go 侧 rounds_oracle_test.go 消费）
import { writeFileSync } from 'node:fs'
import {
  groupIntoRoundsOai,
  countRoundsOai,
  computeOaiInvariantStatus,
} from '../../../src/context/rounds.js'

// 用例：覆盖全部五种分组形态 + 不变量三态 + token 估算边界。
const cases: Array<[string, unknown[]]> = [
  // ── 形态 1/2：user + assistant 无 tool_calls ──
  ['simple', [
    { role: 'system', content: '你是天枢。' },
    { role: 'user', content: 'hello' },
    { role: 'assistant', content: 'world' },
  ]],

  // ── 形态 3：assistant 带 tool_calls，结果配对完整 → ok ──
  ['tool_ok', [
    { role: 'user', content: '读文件' },
    { role: 'assistant', content: null, tool_calls: [
      { id: 'c1', type: 'function', function: { name: 'read_file', arguments: '{"file_path":"a.ts"}' } },
      { id: 'c2', type: 'function', function: { name: 'bash', arguments: '{"command":"ls"}' } },
    ] },
    { role: 'tool', tool_call_id: 'c1', content: 'line1\nline2' },
    { role: 'tool', tool_call_id: 'c2', content: 'ok' },
  ]],

  // ── 形态 3：有调用无结果 → broken（会打 API）──
  ['tool_broken', [
    { role: 'user', content: 'go' },
    { role: 'assistant', content: null, tool_calls: [
      { id: 'c1', type: 'function', function: { name: 'read_file', arguments: '{}' } },
      { id: 'c2', type: 'function', function: { name: 'bash', arguments: '{}' } },
    ] },
    { role: 'tool', tool_call_id: 'c1', content: 'only one' },
  ]],

  // ── 形态 3：有结果无调用 → repaired ──
  ['tool_repaired', [
    { role: 'user', content: 'go' },
    { role: 'assistant', content: null, tool_calls: [
      { id: 'c1', type: 'function', function: { name: 'read_file', arguments: '{}' } },
    ] },
    { role: 'tool', tool_call_id: 'c1', content: 'ok' },
    { role: 'tool', tool_call_id: 'cX', content: 'extra' },
  ]],

  // ── 形态 4：孤儿 tool 消息 → repaired ──
  ['orphan_tool', [
    { role: 'user', content: 'x' },
    { role: 'tool', tool_call_id: 'ghost', content: 'nothing led me' },
  ]],

  // ── 形态 5：system / 其他 ──
  ['systems_only', [
    { role: 'system', content: 'a' },
    { role: 'system', content: 'b' },
  ]],

  // ── token 估算：CJK 三区间 ──
  ['cjk_tokens', [
    { role: 'user', content: '中文测试' },                    // 0x4E00 区
    { role: 'assistant', content: 'ひらがなカタカナ' },        // 0x3040 区
    { role: 'user', content: '한국어테스트' },                 // 0xAC00 区
    { role: 'assistant', content: 'mixed 中文 and ascii' },
  ]],

  // ── token 估算：assistant 的 reasoning_content + tool_calls ──
  ['assistant_reasoning', [
    { role: 'user', content: 'q' },
    { role: 'assistant', content: 'answer', reasoning_content: '思考过程 with reasoning', tool_calls: [
      { id: 'c1', type: 'function', function: { name: 'f', arguments: '{"a":1}' } },
    ] },
    { role: 'tool', tool_call_id: 'c1', content: 'r' },
  ]],

  // ── tool_calls 带额外字段（验证长度对账是否被键序影响）──
  ['toolcall_extra_fields', [
    { role: 'user', content: 'q' },
    { role: 'assistant', content: null, tool_calls: [
      { id: 'c1', type: 'function', function: { name: 'f', arguments: '{}' }, extra: 'x', index: 0 },
    ] },
    { role: 'tool', tool_call_id: 'c1', content: 'r' },
  ]],

  // ── 带转义的 arguments（JSON 字符串转义边界）──
  ['escapes_in_args', [
    { role: 'user', content: 'q' },
    { role: 'assistant', content: null, tool_calls: [
      { id: 'c1', type: 'function', function: { name: 'f', arguments: '{"s":"a\\"b\\nc\\td"}' } },
    ] },
    { role: 'tool', tool_call_id: 'c1', content: 'r' },
  ]],

  // ── 空 ──
  ['empty', []],

  // ── 连续多轮（turnNumber 推进 + 无 assistant 的连续 user）──
  ['multi_turn', [
    { role: 'user', content: 'u1' },
    { role: 'assistant', content: 'a1' },
    { role: 'user', content: 'u2' },
    { role: 'assistant', content: null, tool_calls: [
      { id: 'c1', type: 'function', function: { name: 'f', arguments: '{}' } },
    ] },
    { role: 'tool', tool_call_id: 'c1', content: 'r1' },
    { role: 'user', content: 'u3' },
  ]],

  // ── tool 消息紧跟但 role 不同即停（贪心边界）──
  ['tool_then_user', [
    { role: 'assistant', content: null, tool_calls: [
      { id: 'c1', type: 'function', function: { name: 'f', arguments: '{}' } },
    ] },
    { role: 'tool', tool_call_id: 'c1', content: 'r' },
    { role: 'user', content: 'next' },
    { role: 'tool', tool_call_id: 'orphan', content: 'late' },
  ]],

  // ── 非 ASCII 转义 / 特殊字符（token 分类）──
  ['special_chars', [
    { role: 'user', content: 'a\tb\nc"d\\e \u0000\u001f' },
    { role: 'assistant', content: '\u2028\u2029 emoji 🎉 中文' },
  ]],
]

interface OracleEntry {
  rounds: unknown
  count: number
  invariant: unknown
}

const out: Record<string, OracleEntry> = {}
for (const [name, msgs] of cases) {
  // tsx 不做类型检查，这里按真实形态构造；rounds.ts 只读字段。
  const messages = msgs as never
  out[name] = {
    rounds: groupIntoRoundsOai(messages),
    count: countRoundsOai(messages),
    invariant: computeOaiInvariantStatus(groupIntoRoundsOai(messages)),
  }
}

writeFileSync(
  new URL('oracle.json', import.meta.url),
  JSON.stringify(out, null, 2) + '\n',
)
// **同时导出输入**——避免「Go 测的是另一组输入」的漂移。
// cases.json 是输入的唯一真源，Go 侧从它读，不再复刻字面量。
writeFileSync(
  new URL('cases.json', import.meta.url),
  JSON.stringify(Object.fromEntries(cases), null, 2) + '\n',
)
console.log(`已写出 ${cases.length} 个用例（oracle.json + cases.json）`)
