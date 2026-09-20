/**
 * task-state / trajectory / todo-deps 三模块 oracle 生成器。
 *
 * 生成：
 *   node_modules/.bin/tsx go/testdata/taskstate/gen-oracle.ts
 *
 * 覆盖：
 *   1. `TrajectoryRecorder`（src/agent/trajectory.ts）—— record/截断/汇总
 *   2. `detectDependencies` / `orderPendingByExecutability` / `computeMaxDepth`
 *      / `findExecutable`（src/tools/todo-deps.ts）
 *   3. `extractTaskState` / `taskStateFromTodos`（src/agent/task-state.ts）
 *
 * ## 输出形态：`{input, output}` 数据驱动
 *
 * **首版踩坑**：只存输出，测试要在 Go 侧**手工重建输入**——重建一旦与
 * 生成脚本不一致，就是**假绿**（测的是重建的输入，不是 oracle 的）。
 * 现在每个用例都存 `input` 与 `output`，Go 测试零硬编码输入、直接重放。
 *
 * ## 用例设计的反证考量
 *
 * - `extractTaskState`：空 entries 走 early-return 分支（**最容易漏的路径**）；
 *   正则三组（NEXT_STEP / DECISION / FINDING）各需命中与不命中用例；
 *   截断上限（remaining 3 / decisions 3+5 / completed 5）需**超出**上限才验得出。
 * - `todo-deps`：结构化 id（含字母）vs 裸数字 id 是**两条不同匹配路径**——
 *   裸数字必须带依赖提示词才算边，否则「还剩 1 个测试」会被误判。
 *   循环依赖必须验（computeMaxDepth → Infinity）。
 * - `orderPendingByExecutability`：核心不变量是**不丢任何 pending 项**——
 *   用例须包含「伪依赖」场景（引用不存在的 id）。
 *
 * ## 键序说明（exportJson）
 *
 * 生成脚本用 `entry(over)` 的 spread 构造——`errorClass` 落**末尾**，
 * 与 TS 真实调用点（turn-harness.ts:83，errorClass 在 status 与
 * inputSummary 之间）**不同**。`exportJson` 无生产消费方（grep 确认，
 * 仅测试用），故不追求真实调用点的键序——但要**如实**：oracle 记什么，
 * Go 就对账什么。这条注释就是为了让未来的读者不误以为它反映真实字节。
 */
import { writeFileSync, mkdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import { createHash } from 'node:crypto'
import { TrajectoryRecorder } from '../../../src/agent/trajectory.js'
import type { TrajectoryEntry } from '../../../src/agent/trajectory.js'
import {
  detectDependencies,
  orderPendingByExecutability,
  computeMaxDepth,
  findExecutable,
} from '../../../src/tools/todo-deps.js'
import { extractTaskState, taskStateFromTodos } from '../../../src/agent/task-state.js'
import type { TodoItem } from '../../../src/tools/todo-store.js'

const here = dirname(fileURLToPath(import.meta.url))

// ─────────────────────────────────────────────────────────────
// 1. TrajectoryRecorder
// ─────────────────────────────────────────────────────────────

function entry(over: Partial<TrajectoryEntry> = {}): TrajectoryEntry {
  return {
    turn: 0,
    tool: 'read_file',
    target: 'src/a.ts',
    durationMs: 10,
    status: 'success',
    inputSummary: '{}',
    resultSummary: 'ok',
    ...over,
  }
}

type TrajectoryCase = {
  name: string
  input: { maxEntries?: number; entries: TrajectoryEntry[] }
  /** reset 后是否再验一轮（记录 → reset → 空）。 */
  alsoReset?: boolean
}

const trajectoryCases: TrajectoryCase[] = [
  { name: 'empty', input: { entries: [] } },
  { name: 'single_success', input: { entries: [entry()] } },
  {
    name: 'mixed_statuses',
    input: {
      entries: [
        entry({ turn: 0, tool: 'read_file', status: 'success', durationMs: 10 }),
        entry({ turn: 1, tool: 'bash', status: 'failed', durationMs: 20, errorClass: 'timeout' }),
        entry({ turn: 2, tool: 'bash', status: 'retried-success', durationMs: 30 }),
        entry({ turn: 3, tool: 'run_tests', status: 'retried-failed', durationMs: 40, errorClass: 'assert' }),
      ],
    },
  },
  {
    // 截断：maxEntries=3，塞 5 条 → 保留最后 3 条（首两条被丢弃）
    name: 'truncation_keeps_tail',
    input: {
      maxEntries: 3,
      entries: [
        entry({ turn: 0, tool: 't0' }),
        entry({ turn: 1, tool: 't1' }),
        entry({ turn: 2, tool: 't2' }),
        entry({ turn: 3, tool: 't3' }),
        entry({ turn: 4, tool: 't4' }),
      ],
    },
  },
  {
    // 边界：恰好等于上限 → 不截断
    name: 'truncation_exact_limit',
    input: { maxEntries: 3, entries: [entry({ tool: 't0' }), entry({ tool: 't1' }), entry({ tool: 't2' })] },
  },
  {
    // avgDurationMs 取整：10+21+32 = 63 / 3 = 21
    name: 'avg_rounding',
    input: { entries: [entry({ durationMs: 10 }), entry({ durationMs: 21 }), entry({ durationMs: 32 })] },
  },
  {
    // reset：记录 → reset → 应为空
    name: 'after_reset',
    input: { maxEntries: 10, entries: [entry()] },
    alsoReset: true,
  },
]

const trajectory: Record<string, unknown> = {}
for (const c of trajectoryCases) {
  const rec = new TrajectoryRecorder(c.input.maxEntries)
  for (const e of c.input.entries) rec.record(e)
  const snapshot = () => ({
    entries: rec.getEntries(),
    summarize: rec.summarize(),
    exportJson: rec.exportJson(),
  })
  const output: Record<string, unknown> = snapshot()
  if (c.alsoReset) {
    rec.reset()
    output['afterReset'] = snapshot()
  }
  trajectory[c.name] = { input: c.input, output }
}

// ─────────────────────────────────────────────────────────────
// 2. todo-deps
// ─────────────────────────────────────────────────────────────

function todo(id: string, content: string, status: TodoItem['status'] = 'pending'): TodoItem {
  return { id, content, status }
}

type DepsCase = { name: string; input: { todos: TodoItem[] } }

const depsCases: DepsCase[] = [
  { name: 'empty', input: { todos: [] } },
  {
    // 结构化 id：独立 token 匹配
    name: 'structured_ids',
    input: {
      todos: [todo('T1', '搭建骨架', 'completed'), todo('T2', '基于 T1 实现核心'), todo('T3', '收尾')],
    },
  },
  {
    // **陷阱**：T1 与 T10 —— 前缀匹配必须不误判（\b 边界）
    name: 'structured_id_prefix_trap',
    input: { todos: [todo('T1', '基础', 'completed'), todo('T10', '独立任务'), todo('T2', '基于 T1 的工作')] },
  },
  {
    // 裸数字 id 无提示词 → **不是**依赖边（"还剩 1 个测试"）
    name: 'bare_numeric_no_cue',
    input: { todos: [todo('1', '第一项', 'completed'), todo('2', '还剩 1 个测试要写')] },
  },
  {
    // 裸数字 id 带提示词 → 是依赖边
    name: 'bare_numeric_with_cue',
    input: { todos: [todo('1', '第一项', 'completed'), todo('2', '基于 1 继续')] },
  },
  {
    // 循环依赖 → computeMaxDepth = Infinity
    name: 'cycle',
    input: { todos: [todo('A', '基于 B 做'), todo('B', '基于 A 做')] },
  },
  {
    // 链式深度 3：D 依赖 C 依赖 B 依赖 A
    name: 'chain_depth_3',
    input: {
      todos: [todo('A', '起点', 'completed'), todo('B', '基于 A'), todo('C', '基于 B'), todo('D', '基于 C')],
    },
  },
  {
    // **不变量**：伪依赖（引用不存在的 id）不能丢项
    name: 'phantom_dependency_keeps_all',
    input: { todos: [todo('X', '基于 T99 的伪依赖'), todo('Y', '普通任务'), todo('Z', '另一个普通任务')] },
  },
  {
    // 英文依赖提示词
    name: 'english_cues',
    input: { todos: [todo('T1', 'base', 'completed'), todo('T2', 'depends on T1 work')] },
  },
]

const deps: Record<string, unknown> = {}
for (const c of depsCases) {
  const d = detectDependencies(c.input.todos)
  const maxDepth = computeMaxDepth(d)
  deps[c.name] = {
    input: c.input,
    output: {
      deps: d,
      maxDepth: maxDepth === Infinity ? 'Infinity' : maxDepth,
      findExecutable: findExecutable(c.input.todos, d).map(t => t.id),
      orderPending: orderPendingByExecutability(c.input.todos, d).map(t => t.id),
    },
  }
}

// ─────────────────────────────────────────────────────────────
// 3. task-state
// ─────────────────────────────────────────────────────────────

type TsCase = { name: string; input: { entries: TrajectoryEntry[]; text: string } }

const tsCases: TsCase[] = [
  // **最容易漏的分支**：空 entries early-return
  { name: 'empty_entries', input: { entries: [], text: 'whatever' } },
  {
    name: 'success_only',
    input: {
      entries: [
        entry({ turn: 0, tool: 'read_file', target: 'src/a.ts', status: 'success' }),
        entry({ turn: 1, tool: 'edit_file', target: 'src/b.ts', status: 'success' }),
      ],
      text: '',
    },
  },
  {
    // lastEntry failed → current = "fixing <errorClass> in <basename>"
    name: 'last_failed',
    input: {
      entries: [
        entry({ turn: 0, tool: 'read_file', target: 'src/a.ts', status: 'success' }),
        entry({ turn: 1, tool: 'run_tests', target: 'x/y.ts', status: 'failed', errorClass: 'assert' }),
      ],
      text: '',
    },
  },
  {
    // lastEntry failed 但**无 errorClass** → 回退 'error'
    name: 'last_failed_no_errorclass',
    input: { entries: [entry({ turn: 0, tool: 'bash', target: 'z.ts', status: 'failed' })], text: '' },
  },
  {
    // retried-failed 也走 failed 分支
    name: 'last_retried_failed',
    input: {
      entries: [entry({ turn: 0, tool: 'bash', target: 'q.ts', status: 'retried-failed', errorClass: 'timeout' })],
      text: '',
    },
  },
  {
    // **超出上限**：completed 取最后 5 条
    name: 'completed_capped_at_5',
    input: {
      entries: [
        entry({ turn: 0, tool: 't0', target: 'f0.ts', status: 'success' }),
        entry({ turn: 1, tool: 't1', target: 'f1.ts', status: 'success' }),
        entry({ turn: 2, tool: 't2', target: 'f2.ts', status: 'success' }),
        entry({ turn: 3, tool: 't3', target: 'f3.ts', status: 'success' }),
        entry({ turn: 4, tool: 't4', target: 'f4.ts', status: 'success' }),
        entry({ turn: 5, tool: 't5', target: 'f5.ts', status: 'success' }),
        entry({ turn: 6, tool: 't6', target: 'f6.ts', status: 'success' }),
      ],
      text: '',
    },
  },
  {
    // retried-success 也算 successful
    name: 'retried_success_counts',
    input: {
      entries: [
        entry({ turn: 0, tool: 'a', target: 'p/a.ts', status: 'retried-success' }),
        entry({ turn: 1, tool: 'b', target: 'p/b.ts', status: 'failed', errorClass: 'x' }),
      ],
      text: '',
    },
  },
  {
    // NEXT_STEP 正则命中（中英）
    name: 'next_step_matches',
    input: {
      entries: [entry({ turn: 0, tool: 't', target: 'f.ts', status: 'success' })],
      text: 'First done. 接下来做 A。然后做 B。Next I will do C. after that D.',
    },
  },
  {
    // DECISION 正则命中
    name: 'decision_matches',
    input: {
      entries: [entry({ turn: 0, tool: 't', target: 'f.ts', status: 'success' })],
      text: "I'll use the adapter pattern. decided to skip caching. approach: keep it simple.",
    },
  },
  {
    // FINDING 正则命中
    name: 'finding_matches',
    input: {
      entries: [entry({ turn: 0, tool: 't', target: 'f.ts', status: 'success' })],
      text: 'found that the root cause is X. discovered a race. 发现原因是缓存失效。',
    },
  },
  {
    // 无任何正则命中 → 空数组
    name: 'no_regex_hits',
    input: {
      entries: [entry({ turn: 0, tool: 't', target: 'f.ts', status: 'success' })],
      text: 'Nothing notable here. Just plain text without cues.',
    },
  },
  {
    // target 无斜杠 → split('/').pop() 返回自身
    name: 'target_without_slash',
    input: { entries: [entry({ turn: 0, tool: 'bash', target: 'ls', status: 'success' })], text: '' },
  },
  {
    // **截断边界**：文本超长触发 slice(0,60) / slice(0,80)。
    //
    // TS 的 String.slice 按 **UTF-16 code unit** 计数——BMP 字符（含中文）
    // 算 1 个，而 Go 的字节切片算 3 个。这条用例锁定该语义（否则中文长文本
    // 会截出不同结果，进 handoff 后改变字节）。
    name: 'truncation_boundary_cjk',
    input: {
      entries: [entry({ turn: 0, tool: 't', target: 'f.ts', status: 'success' })],
      // 「接下来」+ 70 个中文字符——确保超过 60 code units。
      text: '接下来' + '很'.repeat(70) + '。',
    },
  },
  {
    // **截断边界（decisions）**：DECISION 超 80 code units。
    name: 'truncation_boundary_decision',
    input: {
      entries: [entry({ turn: 0, tool: 't', target: 'f.ts', status: 'success' })],
      text: "approach: " + 'x'.repeat(100) + ".",
    },
  },
]

const taskState: Record<string, unknown> = {}
for (const c of tsCases) {
  taskState[c.name] = { input: c.input, output: extractTaskState(c.input.entries, c.input.text) }
}

// ── taskStateFromTodos ──
type FromTodosCase = { name: string; input: { todos: TodoItem[]; decisions: string[] } }
const fromTodosCases: FromTodosCase[] = [
  { name: 'empty', input: { todos: [], decisions: [] } },
  {
    name: 'basic',
    input: {
      todos: [
        todo('T1', '已完成项', 'completed'),
        todo('T2', '当前项', 'in_progress'),
        todo('T3', '待办 A'),
        todo('T4', '待办 B'),
      ],
      decisions: ['用 A 方案'],
    },
  },
  {
    // 无 in_progress → current 取 ordered[0]
    name: 'no_in_progress_uses_ordered_first',
    input: { todos: [todo('T1', '已完成', 'completed'), todo('T2', '待办一'), todo('T3', '待办二')], decisions: [] },
  },
  {
    // **核心不变量**：依赖阻塞不得丢项——T2 被 T1 阻塞（T1 pending）
    name: 'blocked_item_retained',
    input: { todos: [todo('T1', '前置任务'), todo('T2', '基于 T1 的后置'), todo('T3', '独立任务')], decisions: [] },
  },
  {
    // 全部 completed → current 回退 'working'
    name: 'all_completed_fallback_working',
    input: { todos: [todo('T1', 'A', 'completed'), todo('T2', 'B', 'completed')], decisions: [] },
  },
  {
    // 只有 pending，无 in_progress/completed
    name: 'pending_only',
    input: { todos: [todo('T1', '唯一待办')], decisions: ['d1', 'd2'] },
  },
]

const fromTodos: Record<string, unknown> = {}
for (const c of fromTodosCases) {
  fromTodos[c.name] = { input: c.input, output: taskStateFromTodos(c.input.todos, c.input.decisions) }
}

// ─────────────────────────────────────────────────────────────

const out = {
  _note: '{input, output} 数据驱动形态——Go 测试直接重放，零硬编码输入',
  trajectory,
  deps,
  taskState,
  fromTodos,
}

mkdirSync(here, { recursive: true })
writeFileSync(join(here, 'oracle.json'), JSON.stringify(out, null, 2) + '\n')

const sha = createHash('sha256').update(JSON.stringify(out)).digest('hex')
console.error(
  `taskstate oracle：trajectory ${trajectoryCases.length}、deps ${depsCases.length}、` +
    `taskState ${tsCases.length}、fromTodos ${fromTodosCases.length} 用例 — sha256 ${sha.slice(0, 16)}`,
)
