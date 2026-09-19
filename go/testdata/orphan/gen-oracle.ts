/**
 * repairOrphanToolCalls oracle 生成器。
 *
 * 生成：npx tsx go/testdata/orphan/gen-oracle.ts
 *
 * ## 为什么走 SessionPersist.loadOai
 *
 * `repairOrphanToolCalls` 是 **private** 方法，`normalizeOaiMessage` 虽导出
 * 但归一化效果只有在修复链路里才完整。故 oracle 造**真实会话文件**
 * （带校验和的行），调 `loadOai()` 观察真实产出——包括插入的 system-reminder。
 *
 * ## 覆盖
 *
 * - assistant tool_call 无对应 tool 结果（孤儿 tool_use）
 * - tool 结果无对应 tool_call（孤儿 tool_result）
 * - 混合：部分有效 + 部分孤儿
 * - **写类工具**被剔除 → 非破坏性警告文案
 * - 非写类工具被剔除 → 通用警告文案
 * - 空 tool_calls 数组（normalizeOaiMessage 移除）
 * - 无孤儿（不插警告）
 */
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createHash } from 'node:crypto'
import { SessionPersist, getSessionDir } from '../../../src/agent/session-persist.js'
import { appendChecksum } from '../../../src/agent/checksum.js'

const here = dirname(fileURLToPath(import.meta.url))

type Row = Record<string, unknown>

type Case = { note: string; rows: Row[] }

function tc(id: string, name: string, args = '{}'): Row {
  return { id, type: 'function', function: { name, arguments: args } }
}

const cases: Record<string, Case> = {
  noOrphans: {
    note: '无孤儿——不插警告',
    rows: [
      { role: 'user', content: 'hi' },
      { role: 'assistant', content: '', tool_calls: [tc('c1', 'read_file')] },
      { role: 'tool', tool_call_id: 'c1', content: 'file content' },
    ],
  },

  orphanToolUseEmptyContent: {
    note: '**孤儿 tool_use + 空 content → 整条 assistant 丢弃**',
    rows: [
      { role: 'user', content: 'hi' },
      { role: 'assistant', content: '', tool_calls: [tc('c1', 'read_file')] },
      { role: 'user', content: 'next' },
    ],
  },

  orphanToolUseWithContent: {
    note: '孤儿 tool_use 但 assistant 有文本 content → 保留消息、剔除 tool_calls',
    rows: [
      { role: 'user', content: 'hi' },
      { role: 'assistant', content: 'let me read that', tool_calls: [tc('c1', 'read_file')] },
      { role: 'user', content: 'next' },
    ],
  },

  orphanToolResult: {
    note: '**孤儿 tool 结果（无对应 tool_call）被丢弃**',
    rows: [
      { role: 'user', content: 'hi' },
      { role: 'tool', tool_call_id: 'ghost', content: 'stale result' },
      { role: 'user', content: 'next' },
    ],
  },

  mixedPartialOrphan: {
    note: '部分有效 + 部分孤儿：保留有效的',
    rows: [
      { role: 'user', content: 'hi' },
      { role: 'assistant', content: '', tool_calls: [tc('c1', 'read_file'), tc('c2', 'grep')] },
      { role: 'tool', tool_call_id: 'c1', content: 'result for c1' },
      { role: 'user', content: 'next' },
    ],
  },

  orphanWriteTool: {
    note: '**写类工具被剔除 → 非破坏性警告文案**',
    rows: [
      { role: 'user', content: 'hi' },
      { role: 'assistant', content: '', tool_calls: [tc('w1', 'write_file')] },
      { role: 'user', content: 'next' },
    ],
  },

  orphanHashEditTool: {
    note: 'hash_edit 也是写类工具',
    rows: [
      { role: 'user', content: 'hi' },
      { role: 'assistant', content: '', tool_calls: [tc('h1', 'hash_edit')] },
      { role: 'user', content: 'next' },
    ],
  },

  orphanApplyPatchTool: {
    note: 'apply_patch 也是写类工具',
    rows: [
      { role: 'user', content: 'hi' },
      { role: 'assistant', content: '', tool_calls: [tc('a1', 'apply_patch')] },
      { role: 'user', content: 'next' },
    ],
  },

  orphanNonWriteTool: {
    note: '非写类工具 → 通用警告文案',
    rows: [
      { role: 'user', content: 'hi' },
      { role: 'assistant', content: '', tool_calls: [tc('r1', 'read_file')] },
      { role: 'user', content: 'next' },
    ],
  },

  emptyToolCallsArray: {
    note: '**空 tool_calls 数组被移除**（normalizeOaiMessage）',
    rows: [
      { role: 'user', content: 'hi' },
      { role: 'assistant', content: 'text only', tool_calls: [] },
      { role: 'user', content: 'next' },
    ],
  },

  emptyToolCallsNullContent: {
    note: '空 tool_calls + content 缺失 → 补空串',
    rows: [
      { role: 'user', content: 'hi' },
      { role: 'assistant', tool_calls: [] },
      { role: 'user', content: 'next' },
    ],
  },

  multipleOrphanBatches: {
    note: '多批孤儿',
    rows: [
      { role: 'assistant', content: '', tool_calls: [tc('o1', 'read_file')] },
      { role: 'user', content: 'mid' },
      { role: 'assistant', content: '', tool_calls: [tc('o2', 'grep')] },
    ],
  },
}

const results: Record<string, unknown> = {}

for (const [name, c] of Object.entries(cases)) {
  const dir = mkdtempSync(join(tmpdir(), 'orph-'))
  const sid = 'orphan-' + name
  const sessionDir = getSessionDir(dir)
  mkdirSync(sessionDir, { recursive: true })
  const content = c.rows.map(r => appendChecksum(JSON.stringify(r))).join('\n') + '\n'
  writeFileSync(join(sessionDir, `${sid}.jsonl`), content)

  const sp = new SessionPersist(sid, dir)
  const msgs = sp.loadOai()

  results[name] = {
    note: c.note,
    rows: c.rows,
    // 只记录**语义**（role / content / tool_call ids / tool_call_id），
    // 不记录对象键序（不可复现且与 Go 结构无关）
    loaded: msgs.map(m => ({
      role: m.role,
      content: m.content ?? null,
      toolCallIds: Array.isArray(m.tool_calls) ? m.tool_calls.map(t => t.id) : null,
      toolCallNames: Array.isArray(m.tool_calls)
        ? m.tool_calls.map(t => t.function?.name ?? null)
        : null,
      toolCallId: m.tool_call_id ?? null,
    })),
    // 首条是否是 system-reminder（孤儿警告）
    hasReminder: msgs.length > 0 && msgs[0]?.role === 'system'
      && typeof msgs[0]?.content === 'string'
      && msgs[0].content.startsWith('<system-reminder>'),
    reminder: msgs.length > 0 && msgs[0]?.role === 'system' ? msgs[0].content : null,
  }

  rmSync(dir, { recursive: true, force: true })
}

const out = { cases: results }
mkdirSync(here, { recursive: true })
writeFileSync(join(here, 'oracle.json'), JSON.stringify(out, null, 2) + '\n')
const sha = createHash('sha256').update(JSON.stringify(out)).digest('hex')
console.error(`orphan oracle：${Object.keys(cases).length} 用例 — sha256 ${sha.slice(0, 16)}`)
