/**
 * session-state oracle 生成器。
 *
 * 生成：npx tsx go/testdata/sessionstate/gen-oracle.ts
 *
 * ## 覆盖
 *
 * `SessionStateManager`（src/agent/session-state.ts，326 行，零 import——完全
 * 自包含）。重点：
 * - `renderForVolatile()` 的输出**逐字节**（它是模型可见文本，进 volatile 块）
 * - `extractTaskList()` 的**合并语义**（保留旧 status/turnCreated、新 id 追加）
 * - 容量上限的**截断顺序**（decisions 20 / verifications 30 / facts 15 / taskItems 30）
 * - 500 字符的**扩散感知截断**（先砍 decisions，再整体截断加省略号）
 *
 * ## 时间戳处理
 *
 * `Date.now()` 会进 state 但不进 renderForVolatile 的输出（渲染只看内容），
 * 故 golden 不含时间戳——但 extractTaskList 的返回值含 turn，需固定。
 */
import { writeFileSync, mkdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createHash } from 'node:crypto'
import { SessionStateManager } from '../../../src/agent/session-state.js'

const here = dirname(fileURLToPath(import.meta.url))

type Op =
  | { op: 'trackFileRead'; path: string; artifactId: string }
  | { op: 'trackFileModified'; path: string }
  | { op: 'recordDecision'; decision: string; reason: string; turn: number }
  | { op: 'recordVerification'; target: string; status: 'passed' | 'failed' | 'not-run' }
  | { op: 'recordFact'; fact: string; evidence: string }
  | { op: 'extractTaskList'; text: string; turn: number }
  | { op: 'updateTaskListItem'; id: string; status: 'pending' | 'in_progress' | 'completed' | 'blocked'; turn: number }

type Case = { note: string; ops: Op[] }

const cases: Record<string, Case> = {
  empty: { note: '空状态 → 空串（不是空壳）', ops: [] },

  onlyModifiedFiles: {
    note: '只有改动文件',
    ops: [
      { op: 'trackFileModified', path: 'src/a.ts' },
      { op: 'trackFileModified', path: 'src/b.ts' },
    ],
  },

  readOnlyNotRendered: {
    note: '**只读文件不渲染**（modifiedByMe=false 被过滤）',
    ops: [
      { op: 'trackFileRead', path: 'src/read-only.ts', artifactId: 'art1' },
      { op: 'trackFileModified', path: 'src/changed.ts' },
    ],
  },

  readThenModified: {
    note: '先读后改 → 渲染为 modified',
    ops: [
      { op: 'trackFileRead', path: 'src/x.ts', artifactId: 'art1' },
      { op: 'trackFileModified', path: 'src/x.ts' },
    ],
  },

  decisionsOnly: {
    note: '只有 decisions',
    ops: [{ op: 'recordDecision', decision: '用 Go 重写', reason: '字节等价', turn: 1 }],
  },

  decisionsCappedAt5InRender: {
    note: '**渲染只取最后 5 条 decisions**（存储上限 20）',
    ops: Array.from({ length: 8 }, (_, i) => ({
      op: 'recordDecision' as const,
      decision: '决策' + i,
      reason: 'r' + i,
      turn: i,
    })),
  },

  failedOnly: {
    note: '**只有 failed 的 verification 渲染**（passed 不渲染）',
    ops: [
      { op: 'recordVerification', target: 'go test', status: 'passed' },
      { op: 'recordVerification', target: 'npm test', status: 'failed' },
      { op: 'recordVerification', target: 'lint', status: 'not-run' },
    ],
  },

  verificationDedup: {
    note: '同 target 的 verification 是**替换**（不追加）',
    ops: [
      { op: 'recordVerification', target: 'go test', status: 'failed' },
      { op: 'recordVerification', target: 'go test', status: 'passed' },
    ],
  },

  allThree: {
    note: '三类内容齐上',
    ops: [
      { op: 'trackFileModified', path: 'src/a.ts' },
      { op: 'recordDecision', decision: 'D1', reason: 'r', turn: 1 },
      { op: 'recordVerification', target: 'T1', status: 'failed' },
    ],
  },

  manyModifiedFiles: {
    note: '**改动文件最多渲染 10 个**',
    ops: Array.from({ length: 15 }, (_, i) => ({
      op: 'trackFileModified' as const,
      path: 'src/file' + i + '.ts',
    })),
  },

  // ── 500 字符截断 ──
  longDecisionsTriggerTrim: {
    note: '**超 500 字符先砍 decisions 段**',
    ops: [
      { op: 'trackFileModified', path: 'src/short.ts' },
      ...Array.from({ length: 8 }, (_, i) => ({
        op: 'recordDecision' as const,
        decision: '很长的决策描述'.repeat(8) + i,
        reason: 'r',
        turn: i,
      })),
    ],
  },

  veryLongSingleLine: {
    note: '单行超长 → 整体截断加省略号',
    ops: [{ op: 'trackFileModified', path: 'src/' + 'x'.repeat(600) + '.ts' }],
  },

  // ── extractTaskList ──
  taskListBasic: {
    note: '基础提取（三种列表格式）',
    ops: [{ op: 'extractTaskList', text: '- P1: 修复认证 bug\n- T2: 补充测试\n3. S1: 探索方案设计', turn: 2 }],
  },

  taskListStatusMarkers: {
    note: '**状态标记留存在 content 里**（不剥离）',
    ops: [{ op: 'extractTaskList', text: '- P1: 完成的事情 ✓\n- T2: 卡住的事情 ⊗\n- S1: 正在做的事情 ⏳', turn: 1 }],
  },

  taskListMerge: {
    note: '**合并语义**：第二轮保留旧 status/turnCreated',
    ops: [
      { op: 'extractTaskList', text: '- P1: 这是第一个任务的描述 ✓', turn: 1 },
      { op: 'extractTaskList', text: '- P1: 这是更新后的描述文本\n- T2: 这是新增的任务项', turn: 5 },
    ],
  },

  taskListFilterThreshold: {
    note: '**过滤阈值 length>3**：中文三字被过滤（UTF-16 code unit 语义）',
    ops: [
      { op: 'extractTaskList', text: '- P1: 原始任务描述', turn: 1 },
      { op: 'extractTaskList', text: '- P1: 新描述\n- T2: 新增项', turn: 5 },
    ],
  },

  taskListMergeStatusOverride: {
    note: '**显式状态标记覆盖旧 status**（第二轮带 ✓）',
    ops: [
      { op: 'extractTaskList', text: '- P1: 这是任务的描述文本', turn: 1 },
      { op: 'extractTaskList', text: '- P1: 这是任务的描述文本 ✓', turn: 5 },
    ],
  },

  taskListTooShort: {
    note: '**内容过短被过滤**（去符号后 <= 3 字符）',
    ops: [{ op: 'extractTaskList', text: '- P1: a\n- T2: 这是有效内容', turn: 1 }],
  },

  taskListEmptyResult: {
    note: '**提取不到时返回现有列表的拷贝**（不清空）',
    ops: [
      { op: 'extractTaskList', text: '- P1: 原有的任务项描述', turn: 1 },
      { op: 'extractTaskList', text: '没有任务编号的普通文本', turn: 2 },
    ],
  },

  taskListUpdateItem: {
    note: 'updateTaskListItem 改状态',
    ops: [
      { op: 'extractTaskList', text: '- P1: 这是任务描述', turn: 1 },
      { op: 'updateTaskListItem', id: 'P1', status: 'completed', turn: 3 },
    ],
  },

  taskListUpdateMissing: {
    note: 'updateTaskListItem 对不存在的 id 返回 false',
    ops: [{ op: 'updateTaskListItem', id: 'ZZ', status: 'completed', turn: 1 }],
  },

  taskListMarkerOrder: {
    note: '**顺序敏感**：同行含双标记时 completed 优先于 blocked',
    ops: [{ op: 'extractTaskList', text: '- P1: 完成的事情 ✓ 但被阻塞了', turn: 1 }],
  },

  taskListMarkerOrderWord: {
    note: '**顺序敏感（词边界）**：done 优先于 blocked',
    ops: [{ op: 'extractTaskList', text: '- P1: this task is done and blocked', turn: 1 }],
  },

  taskListBoldFormat: {
    note: '**加粗格式** `**P1**: content`',
    ops: [{ op: 'extractTaskList', text: '**P1**: 加粗格式的任务', turn: 1 }],
  },

  taskListContentTruncated160: {
    note: 'content 截断到 160 字符',
    ops: [{ op: 'extractTaskList', text: '- P1: ' + 'x'.repeat(200), turn: 1 }],
  },
}

const results: Record<string, unknown> = {}

for (const [name, c] of Object.entries(cases)) {
  const m = new SessionStateManager('oracle-session')
  let lastExtract: unknown = null
  let lastUpdate: unknown = null

  for (const op of c.ops) {
    switch (op.op) {
      case 'trackFileRead': m.trackFileRead(op.path, op.artifactId); break
      case 'trackFileModified': m.trackFileModified(op.path); break
      case 'recordDecision': m.recordDecision(op.decision, op.reason, op.turn); break
      case 'recordVerification': m.recordVerification(op.target, op.status); break
      case 'recordFact': m.recordFact(op.fact, op.evidence); break
      case 'extractTaskList': lastExtract = m.extractTaskList(op.text, op.turn); break
      case 'updateTaskListItem': lastUpdate = m.updateTaskListItem(op.id, op.status, op.turn); break
    }
  }

  const snapshot = m.getSnapshot()
  results[name] = {
    note: c.note,
    rendered: m.renderForVolatile(),
    // 快照去掉时间戳字段（不可复现）
    fileIndexKeys: Object.keys(snapshot.fileIndex),
    modifiedFlags: Object.fromEntries(
      Object.entries(snapshot.fileIndex).map(([k, v]) => [k, v.modifiedByMe]),
    ),
    decisionsCount: snapshot.decisions.length,
    verification: snapshot.verification.map(v => ({ target: v.target, status: v.status })),
    taskList: m.getTaskList().map(t => ({
      id: t.id, content: t.content, status: t.status,
      turnCreated: t.turnCreated, turnUpdated: t.turnUpdated,
    })),
    lastExtract,
    lastUpdate,
  }
}

const out = { cases: results }
mkdirSync(here, { recursive: true })
writeFileSync(join(here, 'oracle.json'), JSON.stringify(out, null, 2) + '\n')
const sha = createHash('sha256').update(JSON.stringify(out)).digest('hex')
console.error(`sessionstate oracle：${Object.keys(cases).length} 用例 — sha256 ${sha.slice(0, 16)}`)
