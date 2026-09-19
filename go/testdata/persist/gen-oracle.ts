/**
 * SessionPersist 编排层 oracle 生成器。
 *
 * 生成：npx tsx go/testdata/persist/gen-oracle.ts
 *
 * ## 覆盖
 *
 * 对账 src/agent/session-persist.ts 的**编排核心**：
 * - `loadOai` 的完整链路（读 → checksum 过滤 → 逐行解析 → 归一化 → 孤儿修复）
 * - 审计行跳过（compact_start / compact_end / model_switch）
 * - **legacy 迁移**（传统 Message 的块数组 → OAI 消息）
 * - 损坏行 / 无效校验和行的容错
 *
 * ## 可复现性
 *
 * 会话文件内容由本脚本生成（确定性），产出只记语义结构。
 */
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createHash } from 'node:crypto'
import { SessionPersist, getSessionDir } from '../../../src/agent/session-persist.js'
import { appendChecksum } from '../../../src/agent/checksum.js'

const here = dirname(fileURLToPath(import.meta.url))

type Case = {
  note: string
  /** 会话文件的行（已 JSON 化的对象；生成器会加校验和）*/
  rows: string[]
  /** 是否写入无效校验和（测容错）*/
  corruptChecksum?: boolean
}

const cases: Record<string, Case> = {
  plainOai: {
    note: '普通 OAI 消息往返',
    rows: [
      JSON.stringify({ role: 'user', content: 'hi' }),
      JSON.stringify({ role: 'assistant', content: 'hello' }),
    ],
  },

  auditLinesSkipped: {
    note: '**审计行被跳过**（compact_start/end、model_switch）',
    rows: [
      JSON.stringify({ role: 'user', content: 'hi' }),
      JSON.stringify({ type: 'compact_start', t: 1 }),
      JSON.stringify({ role: 'assistant', content: 'ok' }),
      JSON.stringify({ type: 'compact_end', t: 2 }),
      JSON.stringify({ type: 'model_switch', from: 'a', to: 'b' }),
    ],
  },

  legacyUserBlocks: {
    note: '**legacy 迁移**：user 的块数组 → 文本 + tool 结果',
    rows: [
      JSON.stringify({
        role: 'user',
        content: [
          { type: 'text', text: 'question' },
          { type: 'tool_result', tool_use_id: 'c1', content: 'result' },
        ],
      }),
    ],
  },

  legacyAssistantBlocks: {
    note: '**legacy 迁移**：assistant 的文本 + tool_use 块',
    rows: [
      JSON.stringify({
        role: 'assistant',
        content: [
          { type: 'text', text: 'let me read' },
          { type: 'tool_use', id: 'c1', name: 'read_file', input: { path: 'a.txt' } },
        ],
      }),
    ],
  },

  legacyAssistantThinking: {
    note: '**legacy 迁移**：assistant 带 thinking 块 → reasoning_content',
    rows: [
      JSON.stringify({
        role: 'assistant',
        content: [
          { type: 'thinking', thinking: 'reasoning here' },
          { type: 'text', text: 'answer' },
        ],
      }),
    ],
  },

  legacyAssistantToolOnly: {
    note: '**legacy 迁移**：只有 tool_use、无文本 → content 为 null',
    rows: [
      JSON.stringify({
        role: 'assistant',
        content: [{ type: 'tool_use', id: 'c1', name: 'grep', input: {} }],
      }),
    ],
  },

  malformedRow: {
    note: '损坏的 JSON 行被跳过',
    rows: [
      JSON.stringify({ role: 'user', content: 'hi' }),
      '{not json',
      JSON.stringify({ role: 'assistant', content: 'ok' }),
    ],
  },

  toolPair: {
    note: '完整的 tool_call / tool_result 配对',
    rows: [
      JSON.stringify({
        role: 'assistant', content: '',
        tool_calls: [{ id: 'c1', type: 'function', function: { name: 'read_file', arguments: '{}' } }],
      }),
      JSON.stringify({ role: 'tool', tool_call_id: 'c1', content: 'file content' }),
    ],
  },

  emptyFile: {
    note: '空文件 → 空数组',
    rows: [],
  },
}

const results: Record<string, unknown> = {}

for (const [name, c] of Object.entries(cases)) {
  const dir = mkdtempSync(join(tmpdir(), 'persist-'))
  const sid = 'p-' + name
  const sessionDir = getSessionDir(dir)
  mkdirSync(sessionDir, { recursive: true })

  let body = ''
  if (c.rows.length > 0) {
    body = c.rows.map(r => appendChecksum(r)).join('\n') + '\n'
  }
  writeFileSync(join(sessionDir, `${sid}.jsonl`), body)

  const sp = new SessionPersist(sid, dir)
  const msgs = sp.loadOai()

  results[name] = {
    note: c.note,
    rows: c.rows,
    loaded: msgs.map(m => ({
      role: m.role,
      content: m.content ?? null,
      toolCallIds: Array.isArray(m.tool_calls) ? m.tool_calls.map(t => t.id) : null,
      toolCallNames: Array.isArray(m.tool_calls) ? m.tool_calls.map(t => t.function?.name ?? null) : null,
      toolCallArgs: Array.isArray(m.tool_calls) ? m.tool_calls.map(t => t.function?.arguments ?? null) : null,
      toolCallId: m.tool_call_id ?? null,
      reasoning: (m as { reasoning_content?: string }).reasoning_content ?? null,
    })),
    count: msgs.length,
  }
  rmSync(dir, { recursive: true, force: true })
}

const out = { cases: results }
mkdirSync(here, { recursive: true })
writeFileSync(join(here, 'oracle.json'), JSON.stringify(out, null, 2) + '\n')
const sha = createHash('sha256').update(JSON.stringify(out)).digest('hex')
console.error(`persist oracle：${Object.keys(cases).length} 用例 — sha256 ${sha.slice(0, 16)}`)
