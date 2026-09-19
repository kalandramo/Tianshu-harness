// 端到端对账：真实 oracle（src/agent/advisory-readback.ts）vs Go 实现。
//
// **范围**：核心 readback——track / observeTool / evaluate / 谓词求值 / 查询。
// **不含**：跨会话 lift 持久化（读文件）、seedPriors（依赖跨会话存储）。
//
// 运行（必须在**仓库根**）：
//   node_modules/.bin/tsx go/testdata/readback/gen-oracle.ts
import { writeFileSync } from 'node:fs'
import { AdvisoryReadback } from '../../../src/agent/advisory-readback.js'
import type { DeliveredAdvisory } from '../../../src/agent/advisory-bus.js'
import type { AdvisoryExpectation } from '../../../src/agent/advisory-bus.js'

type Op =
  | { op: 'track'; key: string; expect?: unknown; shadow?: boolean; turn: number }
  | { op: 'tool'; turn: number; name: string; target: string; isError?: boolean }
  | { op: 'evaluate'; turn: number }

const cases: Record<string, Op[]> = {}

// ── 基础：送达 + 窗口内满足 → adopted ──
cases.adopt_verify_attempted = [
  { op: 'track', key: 'k1', expect: { kind: 'verify_attempted' }, turn: 0 },
  { op: 'tool', turn: 0, name: 'run_tests', target: '' },
  { op: 'evaluate', turn: 0 },
]

// ── 窗口到期未满足 → ignored ──
cases.ignore_verify_timeout = [
  { op: 'track', key: 'k1', expect: { kind: 'verify_attempted' }, turn: 0 },
  { op: 'tool', turn: 0, name: 'read_file', target: 'a.ts' },
  { op: 'evaluate', turn: 0 },
  { op: 'tool', turn: 1, name: 'read_file', target: 'b.ts' },
  { op: 'evaluate', turn: 1 },
]

// ── 未到期 → 保持 pending（不判 ignored）──
cases.pending_not_yet = [
  { op: 'track', key: 'k1', expect: { kind: 'verify_attempted' }, turn: 0 },
  { op: 'evaluate', turn: 0 },
]

// ── tool_appears（窗口 1 轮）──
cases.tool_appears_hit = [
  { op: 'track', key: 'k1', expect: { kind: 'tool_appears', tools: ['bash'] }, turn: 0 },
  { op: 'tool', turn: 0, name: 'bash', target: 'ls' },
  { op: 'evaluate', turn: 0 },
]

cases.tool_appears_miss = [
  { op: 'track', key: 'k1', expect: { kind: 'tool_appears', tools: ['bash'] }, turn: 0 },
  { op: 'tool', turn: 0, name: 'read_file', target: 'a.ts' },
  { op: 'evaluate', turn: 0 },
]

// ── tool_appears 带 targetIncludes ──
cases.tool_appears_target_includes = [
  { op: 'track', key: 'k1', expect: { kind: 'tool_appears', tools: ['read_file'], targetIncludes: 'src/' }, turn: 0 },
  { op: 'tool', turn: 0, name: 'read_file', target: 'src/a.ts' },
  { op: 'evaluate', turn: 0 },
]

cases.tool_appears_target_miss = [
  { op: 'track', key: 'k1', expect: { kind: 'tool_appears', tools: ['read_file'], targetIncludes: 'src/' }, turn: 0 },
  { op: 'tool', turn: 0, name: 'read_file', target: 'test/a.ts' },
  { op: 'evaluate', turn: 0 },
]

// ── verify_attempted 的 bash 命令识别 ──
cases.verify_bash_test = [
  { op: 'track', key: 'k1', expect: { kind: 'verify_attempted' }, turn: 0 },
  { op: 'tool', turn: 0, name: 'bash', target: 'npm test' },
  { op: 'evaluate', turn: 0 },
]

cases.verify_bash_non_test = [
  { op: 'track', key: 'k1', expect: { kind: 'verify_attempted' }, turn: 0 },
  { op: 'tool', turn: 0, name: 'bash', target: 'ls -la' },
  { op: 'evaluate', turn: 0 },
]

// ── verify_attempted 的 typecheck 工具 ──
cases.verify_typecheck_tool = [
  { op: 'track', key: 'k1', expect: { kind: 'verify_attempted' }, turn: 0 },
  { op: 'tool', turn: 0, name: 'typecheck', target: '' },
  { op: 'evaluate', turn: 0 },
]

// ── file_touched ──
cases.file_touched_hit = [
  { op: 'track', key: 'k1', expect: { kind: 'file_touched', paths: ['src/a.ts'] }, turn: 0 },
  { op: 'tool', turn: 0, name: 'edit_file', target: 'src/a.ts' },
  { op: 'evaluate', turn: 0 },
]

cases.file_touched_miss = [
  { op: 'track', key: 'k1', expect: { kind: 'file_touched', paths: ['src/a.ts'] }, turn: 0 },
  { op: 'tool', turn: 0, name: 'edit_file', target: 'src/b.ts' },
  { op: 'evaluate', turn: 0 },
]

// ── pattern_absent（负向，只在到期时判）──
// 注：checkPatternAbsent 读文件，这里用不存在的路径——readFile 返回 null
// → 视为"模式消失"（adopted）。
cases.pattern_absent_expired = [
  { op: 'track', key: 'k1', expect: { kind: 'pattern_absent', path: '/nonexistent/x.ts', needles: ['console.log'] }, turn: 0 },
  { op: 'evaluate', turn: 0 },
  { op: 'evaluate', turn: 1 },
  { op: 'evaluate', turn: 2 },
  { op: 'evaluate', turn: 3 },
]

// ── course_changed（前置窗空 → 任意工具即改道）──
cases.course_changed_no_pre = [
  { op: 'track', key: 'k1', expect: { kind: 'course_changed' }, turn: 0 },
  { op: 'tool', turn: 0, name: 'bash', target: 'ls' },
  { op: 'evaluate', turn: 0 },
]

// ── 连续 ignored → ignoredStreak 累加 ──
cases.ignored_streak = [
  { op: 'track', key: 'k1', expect: { kind: 'verify_attempted' }, turn: 0 },
  { op: 'evaluate', turn: 0 },
  { op: 'evaluate', turn: 1 },
  { op: 'track', key: 'k1', expect: { kind: 'verify_attempted' }, turn: 2 },
  { op: 'evaluate', turn: 2 },
  { op: 'evaluate', turn: 3 },
]

// ── adopted 清零 streak ──
cases.adopt_resets_streak = [
  { op: 'track', key: 'k1', expect: { kind: 'verify_attempted' }, turn: 0 },
  { op: 'evaluate', turn: 0 },
  { op: 'evaluate', turn: 1 },
  { op: 'track', key: 'k1', expect: { kind: 'verify_attempted' }, turn: 2 },
  { op: 'tool', turn: 2, name: 'run_tests', target: '' },
  { op: 'evaluate', turn: 2 },
]

// ── shadow：只进 shadow 桶，不动 adopted/ignored/streak ──
cases.shadow_adopted = [
  { op: 'track', key: 'k1', expect: { kind: 'verify_attempted' }, shadow: true, turn: 0 },
  { op: 'tool', turn: 0, name: 'run_tests', target: '' },
  { op: 'evaluate', turn: 0 },
]

cases.shadow_ignored = [
  { op: 'track', key: 'k1', expect: { kind: 'verify_attempted' }, shadow: true, turn: 0 },
  { op: 'evaluate', turn: 0 },
  { op: 'evaluate', turn: 1 },
]

// ── 同 key 重复送达刷新窗口（不叠加 pending）──
cases.repeat_track_refresh = [
  { op: 'track', key: 'k1', expect: { kind: 'verify_attempted' }, turn: 0 },
  { op: 'track', key: 'k1', expect: { kind: 'verify_attempted' }, turn: 1 },
  { op: 'evaluate', turn: 1 },
  { op: 'evaluate', turn: 2 },
]

// ── 无 expect → 只计 delivered，无 pending ──
cases.no_expect = [
  { op: 'track', key: 'k1', turn: 0 },
  { op: 'evaluate', turn: 0 },
  { op: 'evaluate', turn: 5 },
]

// ── 多 key 独立追踪 ──
cases.multi_key = [
  { op: 'track', key: 'k1', expect: { kind: 'verify_attempted' }, turn: 0 },
  { op: 'track', key: 'k2', expect: { kind: 'tool_appears', tools: ['bash'] }, turn: 0 },
  { op: 'tool', turn: 0, name: 'bash', target: 'ls' },
  { op: 'evaluate', turn: 0 },
]

// ── 事件按轮修剪（EVENT_RETENTION_TURNS=8）──
cases.event_trim = [
  { op: 'tool', turn: 0, name: 'read_file', target: 'a.ts' },
  { op: 'tool', turn: 10, name: 'read_file', target: 'b.ts' },
  { op: 'track', key: 'k1', expect: { kind: 'file_touched', paths: ['a.ts'] }, turn: 10 },
  { op: 'evaluate', turn: 10 },
]

// **判别修剪是否生效**：用**宽窗口**让旧事件落进观察窗。
//
// file_touched 的 withinTurns 显式设为 12——窗口 [0, 11]。turn 0 的事件
// 若未被裁掉则在窗口内 → 命中；若被裁掉则不在 → 未命中。
cases.event_trim_affects_wide_window = [
  { op: 'tool', turn: 0, name: 'read_file', target: 'a.ts' },
  { op: 'tool', turn: 10, name: 'read_file', target: 'b.ts' },
  { op: 'track', key: 'k1', expect: { kind: 'file_touched', paths: ['a.ts'], withinTurns: 12 }, turn: 0 },
  { op: 'evaluate', turn: 10 },
]

// **修剪后仍保留窗口内事件**（不该过度裁剪）
cases.event_trim_keeps_recent = [
  { op: 'tool', turn: 0, name: 'read_file', target: 'old.ts' },
  { op: 'tool', turn: 5, name: 'read_file', target: 'a.ts' },
  { op: 'tool', turn: 10, name: 'read_file', target: 'b.ts' },
  { op: 'track', key: 'k1', expect: { kind: 'file_touched', paths: ['a.ts'] }, turn: 5 },
  { op: 'evaluate', turn: 5 },
]

interface OracleEntry {
  stats: Record<string, unknown>
  outcomes: unknown[]
  adoptedRate: Record<string, unknown>
  lift: Record<string, unknown>
  decided: Record<string, unknown>
  ignoredStreak: Record<string, unknown>
}

const KEYS = ['k1', 'k2', 'k3']

const out: Record<string, OracleEntry> = {}
for (const [name, ops] of Object.entries(cases)) {
  const rb = new AdvisoryReadback()
  for (const op of ops) {
    if (op.op === 'track') {
      const d: DeliveredAdvisory = {
        key: op.key,
        category: 'discipline' as never,
        expect: op.expect as AdvisoryExpectation | undefined,
        ...(op.shadow ? { shadow: true } : {}),
      }
      rb.track([d], op.turn)
    } else if (op.op === 'tool') {
      rb.observeTool({ turn: op.turn, name: op.name, target: op.target, isError: op.isError ?? false })
    } else {
      rb.evaluate(op.turn)
    }
  }

  const stats: Record<string, unknown> = {}
  for (const [k, v] of rb.getStats()) stats[k] = v

  const adoptedRate: Record<string, unknown> = {}
  const lift: Record<string, unknown> = {}
  const decided: Record<string, unknown> = {}
  const ignoredStreak: Record<string, unknown> = {}
  for (const k of KEYS) {
    adoptedRate[k] = rb.getAdoptionRate(k)
    lift[k] = rb.getLift(k)
    decided[k] = rb.getDecidedCount(k)
    ignoredStreak[k] = rb.getIgnoredStreak(k)
  }

  out[name] = { stats, outcomes: rb.drainOutcomes(), adoptedRate, lift, decided, ignoredStreak }
}

writeFileSync(new URL('oracle.json', import.meta.url), JSON.stringify(out, null, 2) + '\n')
writeFileSync(new URL('cases.json', import.meta.url), JSON.stringify(cases, null, 2) + '\n')
