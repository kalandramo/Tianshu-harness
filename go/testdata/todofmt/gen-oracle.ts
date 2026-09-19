/**
 * TodoStore.formatList / formatSummary oracle 生成器。
 *
 * 生成：
 *   npx tsx go/testdata/todofmt/gen-oracle.ts
 *
 * 覆盖 src/tools/todo-store.ts 的两个 static 渲染方法（模型可见的清单文本）。
 *
 * ## 为什么这两个值得对账
 *
 * 它们产出的是**模型直接读到的文本**（todo 工具的返回内容）。三个状态 icon
 * （✓ / ► / ○）与「已更新：N/M 已完成」的计数错了，模型的进度感知就错了。
 * 且它们是 static 纯函数（输入清单 → 输出文本），完全可对账。
 *
 * 纪律：调用真实 static 方法，不手抄。
 */
import { writeFileSync, mkdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import { createHash } from 'node:crypto'
import { TodoStore, TODO_EMPTY_RESULT } from '../../../src/tools/todo-store.js'

const here = dirname(fileURLToPath(import.meta.url))

type Item = { id: string; content: string; status: string; activeForm?: string }

const cases: Record<string, { note?: string; todos: Item[] }> = {
  empty: { note: '空清单 → 固定文案', todos: [] },
  singlePending: { todos: [{ id: '1', content: '修复认证 bug', status: 'pending' }] },
  singleInProgress: { todos: [{ id: '1', content: '修复认证 bug', status: 'in_progress' }] },
  singleCompleted: { todos: [{ id: '1', content: '修复认证 bug', status: 'completed' }] },
  allThreeStatuses: {
    note: '三种状态齐上——验证三个 icon',
    todos: [
      { id: 'a', content: '任务甲', status: 'completed' },
      { id: 'b', content: '任务乙', status: 'in_progress' },
      { id: 'c', content: '任务丙', status: 'pending' },
    ],
  },
  orderPreserved: {
    note: '**顺序敏感**：输出顺序应等于输入顺序（不排序）',
    todos: [
      { id: 'z', content: '最后写的', status: 'pending' },
      { id: 'a', content: '最先写的', status: 'completed' },
    ],
  },
  withActiveForm: {
    note: '带 activeForm 的项——formatList/formatSummary 是否用它？',
    todos: [{ id: '1', content: '修复认证 bug', status: 'in_progress', activeForm: '正在修复认证 bug' }],
  },
  specialChars: {
    note: '内容含特殊字符',
    todos: [
      { id: 'x|y', content: '含 | 与 [方括号]', status: 'pending' },
      { id: 'emoji', content: '😀 表情', status: 'completed' },
      { id: '中文', content: '中文内容', status: 'in_progress' },
    ],
  },
  longContent: {
    note: '长内容（不截断？）',
    todos: [{ id: '1', content: 'x'.repeat(200), status: 'pending' }],
  },
  manyItems: {
    note: '多项——验证 join 与计数',
    todos: Array.from({ length: 10 }, (_, i) => ({
      id: String(i),
      content: `任务${i}`,
      status: i < 3 ? 'completed' : i < 5 ? 'in_progress' : 'pending',
    })),
  },
  allCompleted: {
    note: '全部完成——计数应为 N/N',
    todos: [
      { id: '1', content: '甲', status: 'completed' },
      { id: '2', content: '乙', status: 'completed' },
    ],
  },
}

const results: Record<string, unknown> = {}
for (const [name, c] of Object.entries(cases)) {
  results[name] = {
    note: c.note,
    todos: c.todos,
    formatList: TodoStore.formatList(c.todos as never),
    formatSummary: TodoStore.formatSummary(c.todos as never),
  }
}

const out = {
  emptyResult: TODO_EMPTY_RESULT,
  cases: results,
}
mkdirSync(here, { recursive: true })
writeFileSync(join(here, 'oracle.json'), JSON.stringify(out, null, 2) + '\n')

const sha = createHash('sha256').update(JSON.stringify(out)).digest('hex')
console.error(`todofmt oracle：${Object.keys(cases).length} 用例 — sha256 ${sha.slice(0, 16)}`)
