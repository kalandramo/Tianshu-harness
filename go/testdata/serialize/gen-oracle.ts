/**
 * 会话消息序列化 oracle 生成器。
 *
 * 生成：npx tsx go/testdata/serialize/gen-oracle.ts
 *
 * ## 覆盖三层截断
 *
 * `serializeSessionJsonValue` 有三条路径，**必须都能触发**（否则测不出差异）：
 *  1. 原样 JSON ≤ maxChars → 直接返回
 *  2. 用 `max(1000, floor(maxChars*0.8))` cap 后重序列化 ≤ maxChars → 返回
 *  3. 仍超长 → fallback（整条消息 JSON 截断塞进 content）
 *
 * 构造用例时**用小 maxChars** 让各层可达（真实 100_000 太大）。
 */
import { writeFileSync, mkdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createHash } from 'node:crypto'
import {
  serializeSessionMessage,
  serializeOaiSessionMessage,
  MAX_SESSION_MESSAGE_JSON_CHARS,
} from '../../../src/agent/session-persist.js'

const here = dirname(fileURLToPath(import.meta.url))

type Case = {
  note: string
  kind: 'session' | 'oai'
  message: unknown
  maxChars: number
}

const cases: Record<string, Case> = {
  // ── 第 1 层：原样 ──
  shortSession: {
    note: '短消息 → 原样 JSON',
    kind: 'session',
    message: { role: 'user', content: 'hello' },
    maxChars: 100_000,
  },
  shortOai: {
    note: '短 OAI 消息 → 原样 JSON',
    kind: 'oai',
    message: { role: 'user', content: 'hello' },
    maxChars: 100_000,
  },
  oaiWithToolCalls: {
    note: 'OAI 带 tool_calls → 原样 JSON（键序保留）',
    kind: 'oai',
    message: {
      role: 'assistant',
      content: '',
      tool_calls: [{ id: 'c1', type: 'function', function: { name: 'read_file', arguments: '{"path":"a"}' } }],
    },
    maxChars: 100_000,
  },
  oaiToolRole: {
    note: 'tool 角色 → 带 tool_call_id',
    kind: 'oai',
    message: { role: 'tool', tool_call_id: 'c1', content: 'result' },
    maxChars: 100_000,
  },

  // **键序敏感性**：JSON.stringify 保插入序，而插入序由对象构造决定。
  // 下面三个用例的字段集相同、书写序不同——输出应各自保持自己的序。
  keyOrderRoleContent: {
    note: '键序 role → content（最常见）',
    kind: 'oai',
    message: { role: 'user', content: 'hi' },
    maxChars: 100_000,
  },
  keyOrderContentRole: {
    note: '**键序 content → role**（逆序也应保持）',
    kind: 'oai',
    message: { content: 'hi', role: 'user' },
    maxChars: 100_000,
  },
  keyOrderToolCallIdFirst: {
    note: '**键序 tool_call_id → role → content**',
    kind: 'oai',
    message: { tool_call_id: 'c1', role: 'tool', content: 'r' },
    maxChars: 100_000,
  },
  keyOrderAssistant: {
    note: 'assistant 的 role → content → tool_calls 序',
    kind: 'oai',
    message: {
      role: 'assistant',
      content: '',
      tool_calls: [{ id: 'c1', type: 'function', function: { name: 'read_file', arguments: '{}' } }],
    },
    maxChars: 100_000,
  },

  // ── 第 2 层：cap ──
  longStringCap: {
    note: '**第 2 层**：长字符串被 cap 到 maxChars*0.8',
    kind: 'session',
    message: { role: 'user', content: 'x'.repeat(2000) },
    maxChars: 1500,
  },
  nestedCap: {
    note: '嵌套结构的字符串都被 cap',
    kind: 'session',
    message: { role: 'user', content: 'y'.repeat(3000), extra: { deep: 'z'.repeat(3000) } },
    maxChars: 1200,
  },
  arrayCap: {
    note: '数组元素被逐个 cap',
    kind: 'session',
    message: { role: 'user', content: ['a'.repeat(2000), 'b'.repeat(2000)] },
    maxChars: 1300,
  },

  // ── 第 3 层：fallback ──
  fallbackTriggered: {
    note: '**第 3 层**：cap 后仍超长 → fallback',
    kind: 'session',
    message: { role: 'user', content: 'x'.repeat(200), tag: 'y'.repeat(200) },
    maxChars: 30,
  },
  fallbackOai: {
    note: 'OAI 的 fallback（tool 角色带 tool_call_id）',
    kind: 'oai',
    message: { role: 'tool', tool_call_id: 'c9', content: 'x'.repeat(300) },
    maxChars: 30,
  },
  fallbackNonTool: {
    note: 'OAI 的 fallback（非 tool 角色不带 tool_call_id）',
    kind: 'oai',
    message: { role: 'user', content: 'x'.repeat(300) },
    maxChars: 30,
  },

  // ── 边界 ──
  exactlyAtLimit: {
    note: '恰好等于 maxChars → 不截断',
    kind: 'session',
    message: { role: 'user', content: 'abc' },
    maxChars: JSON.stringify({ role: 'user', content: 'abc' }).length,
  },
  oneOverLimit: {
    note: '超出 1 字符 → 触发截断',
    kind: 'session',
    message: { role: 'user', content: 'abcd' },
    maxChars: JSON.stringify({ role: 'user', content: 'abcd' }).length - 1,
  },
  tinyBudget: {
    note: '极小 maxChars → marker 比额度还长（keep 归 0）',
    kind: 'session',
    message: { role: 'user', content: 'x'.repeat(100) },
    maxChars: 10,
  },
  emojiLength: {
    note: '**UTF-16 语义**：emoji 计 2',
    kind: 'session',
    message: { role: 'user', content: '😀'.repeat(50) },
    maxChars: 100,
  },
  emptyToolCallsNormalized: {
    note: '空 tool_calls 数组被归一化移除后再序列化',
    kind: 'oai',
    message: { role: 'assistant', content: 'text', tool_calls: [] },
    maxChars: 100_000,
  },
  nullContent: {
    note: 'content 为 null → 归一化补空串',
    kind: 'oai',
    message: { role: 'assistant', content: null, tool_calls: [] },
    maxChars: 100_000,
  },
}

const results: Record<string, unknown> = {}

for (const [name, c] of Object.entries(cases)) {
  let out: string
  try {
    out = c.kind === 'session'
      ? serializeSessionMessage(c.message as never, c.maxChars)
      : serializeOaiSessionMessage(c.message as never, c.maxChars)
  } catch (e) {
    out = 'THREW: ' + String(e)
  }
  results[name] = {
    note: c.note,
    kind: c.kind,
    maxChars: c.maxChars,
    output: out,
  }
}

const out = { defaultMaxChars: MAX_SESSION_MESSAGE_JSON_CHARS, cases: results }
mkdirSync(here, { recursive: true })
writeFileSync(join(here, 'oracle.json'), JSON.stringify(out, null, 2) + '\n')
const sha = createHash('sha256').update(JSON.stringify(out)).digest('hex')
console.error(`serialize oracle：${Object.keys(cases).length} 用例 — sha256 ${sha.slice(0, 16)}`)
