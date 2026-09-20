/**
 * session split 阈值判定 oracle 生成器。
 *
 * 生成：
 *   node_modules/.bin/tsx go/testdata/sessionsplit/gen-oracle.ts
 *
 * 覆盖 src/agent/compaction-controller.ts 的 `trySessionSplit` 的**判定层**：
 *
 *   1. contextWindow < 500_000 → 永不 split
 *   2. ratio = estimatedTokens / contextWindow < 0.86 → 不 split
 *   3. 否则 → split
 *
 * ## 范围说明（有意收窄）
 *
 * `trySessionSplit` 的**执行层**（`replaceWithCheckpoint` /
 * `buildStructuredHandoff` / `extractTaskState` / artifact 归档 /
 * promptEngine.resetAppendixBaseline）依赖 Go 侧**尚未移植**的模块
 * （task-state / trajectory / artifact store）。本刀只移植**判定层**——
 * 它是纯阈值逻辑、确定性、可逐用例对账；执行层需先移植那三个子系统。
 *
 * 故 oracle 只记录**判定结果与阈值边界**，不记录 handoff 文本。
 *
 * ## 桩的来源
 *
 * `CompactionController` 的构造依赖面很大，但 TS 自己的测试
 * （`src/agent/__tests__/compaction-controller.test.ts` 的 `makeController`）
 * 已经建立了最小桩。此处复用同一形态，保证 oracle 走的是**真实代码路径**。
 */
import { writeFileSync, mkdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import { createHash } from 'node:crypto'
import { CompactionController } from '../../../src/agent/compaction-controller.js'
import { SessionContext } from '../../../src/agent/context.js'
import { PromptEngine } from '../../../src/prompt/engine.js'
import { PressureMonitor } from '../../../src/context/pressure-monitor.js'
import type { OaiMessage } from '../../../src/api/oai-types.js'

const here = dirname(fileURLToPath(import.meta.url))

function makeEngine(): PromptEngine {
  return new PromptEngine({
    model: 'test-model',
    maxTokens: 1024,
    staticCtx: { tools: [] },
    volatileCtx: { cwd: '/test' },
  })
}

/** 构造一个恰好 N token 的会话（用 ASCII 4 字符/token 近似）。 */
function sessionWithTokens(tokens: number): SessionContext {
  const session = new SessionContext()
  const charsPerMsg = 4000 // 约 1000 token/条
  const perMsg = Math.ceil((tokens * 4) / Math.max(1, Math.ceil(tokens / 1000)))
  const n = Math.max(1, Math.ceil(tokens / 1000))
  const msgs: OaiMessage[] = []
  for (let i = 0; i < n; i++) {
    msgs.push({ role: 'user', content: 'x'.repeat(perMsg) })
  }
  session.replaceMessages(msgs)
  return session
}

function makeController(session: SessionContext, contextWindow: number): CompactionController {
  return new CompactionController({
    session,
    promptEngine: makeEngine(),
    contextWindow,
    pressureMonitor: new PressureMonitor(contextWindow),
    getTrajectoryEntries: () => [],
    getStreamedText: () => '',
    refreshLedger: () => {},
  })
}

type SplitCase = {
  name: string
  contextWindow: number
  /** 目标会话 token 数（近似——实际由估算器决定）。 */
  sessionTokens: number
}

const cases: SplitCase[] = [
  // 窗口门槛：< 500_000 永不 split
  { name: 'small_window_never_splits', contextWindow: 128_000, sessionTokens: 200_000 },
  { name: 'boundary_window_499999', contextWindow: 499_999, sessionTokens: 499_000 },
  { name: 'boundary_window_500000', contextWindow: 500_000, sessionTokens: 499_000 },
  { name: 'window_1m_under_threshold', contextWindow: 1_000_000, sessionTokens: 500_000 },
  // **补充**：ratio 落在 (0.5, 0.86) 区间——首版用例集只有 0.5 与 0.95 两点，
  // 无法区分门槛 0.5 与 0.86（M2 变异红 0 处的根因）。
  { name: 'window_1m_mid_ratio_0_70', contextWindow: 1_000_000, sessionTokens: 700_000 },
  { name: 'window_1m_mid_ratio_0_85', contextWindow: 1_000_000, sessionTokens: 850_000 },
  { name: 'window_1m_over_threshold', contextWindow: 1_000_000, sessionTokens: 950_000 },
  // 窗口大但会话小 → 不 split
  { name: 'large_window_small_session', contextWindow: 2_000_000, sessionTokens: 100_000 },
]

const split: Record<string, { didSplit: boolean; estimatedTokensBefore: number | null; ratioBefore: number | null }> = {}
for (const c of cases) {
  const session = sessionWithTokens(c.sessionTokens)
  const controller = makeController(session, c.contextWindow)

  // **判定前**取值——`trySessionSplit` 成功后会把历史替换成 handoff，
  // 之后 session.getEstimatedTokens() 反映的是压缩后的状态（首版踩坑：
  // 记录到的是替换后的 3098，而非判定时的真实值）。
  const reachedRatio = c.contextWindow >= 500_000
  const beforeTokens = reachedRatio ? session.getEstimatedTokens() : null

  const didSplit = await controller.trySessionSplit()

  split[c.name] = {
    didSplit,
    estimatedTokensBefore: beforeTokens,
    ratioBefore: beforeTokens !== null ? beforeTokens / c.contextWindow : null,
  }
}

const out = {
  _note: '只记录判定层（didSplit + estimatedTokens + ratio）；执行层未移植',
  threshold: { minWindow: 500_000, minRatio: 0.86 },
  split,
}

mkdirSync(here, { recursive: true })
writeFileSync(join(here, 'oracle.json'), JSON.stringify(out, null, 2) + '\n')

const sha = createHash('sha256').update(JSON.stringify(out)).digest('hex')
console.error(`sessionsplit oracle：${cases.length} 个用例 — sha256 ${sha.slice(0, 16)}`)
