/**
 * 阶段 2（可靠续跑）— 会话级恢复台账。
 *
 * 持久化分两半：
 *   · 追加日志：沿用既有 `events.jsonl`（seq 单调、Gateway 单一写入顺序）；
 *   · 原子检查点：本模块的 `<root>/<sessionId>/checkpoint.json`（tmp + rename，
 *     永不半写）。`watermark` 是检查点落笔时的已持久化事件 seq。
 *
 * 恢复事实：
 *   · 工具已持久化（onToolResult 终态已 append）→ 恢复时消费，不重跑；
 *   · 工具已发出但无终态（只有 onToolUse）→ 未知，恢复时转「待确认」，
 *     绝不通用重放（写/发送/外部副作用不可逆）。
 *
 * 自动恢复闸门（`mayAutoResume`）：同一运行在**没有新持久化进展**时最多自动
 * 恢复一次，再失败转待处理，阻断「恢复即再次崩溃」的循环。
 *
 * 「新进展」只认**真实进展水位** `progressWatermark`：turn_complete 或已持久化
 * 的 tool_result。像 `watchdog_recovery`/`status`/`done` 这类簿记或收尾事件
 * 也会推进事件 seq，但**不算进展**——否则闸门判断前刚追加的簿记事件会把
 * 自己伪装成「有新进展」，闸门形同虚设（实测踩过）。
 */
import { randomUUID } from 'node:crypto'
import { appendFile, mkdir, open, readFile, rename } from 'node:fs/promises'
import { join } from 'node:path'

/**
 * 工具恢复状态。判据只有一个：`resultRef` 是否存在（= onToolResult 触发过、
 * 依赖已 fsync）。
 *
 * - `persisted`：**终态事实已落盘**——成功与失败都算。名字容易被读成「工具执行
 *   成功」，那是误读；一次失败的调用同样是不可变事实，且模型转录里就有它，所以
 *   自动恢复（重放对话状态、不重放工具）是安全的。
 * - `unknown`：**从未收到终态**（进程死在副作用与结果落盘之间）。只有这一种情形
 *   意味着「模型历史与磁盘现实可能不一致」，必须转人确认、不得自动续跑。
 *
 * 别再用 `isError` 当这两者的代理判据——2026-09-27 修的就是这个：错误结果曾被
 * 记成 unknown，等于永久关掉自动恢复，且主线没有任何 UI 会说明原因。
 */
export type ToolRecoveryStatus = 'persisted' | 'unknown'

export interface ToolOutcome {
  id: string
  name: string
  status: ToolRecoveryStatus
  at: number
  resultRef?: string
  intentRef?: string
}

export interface RecoveryCheckpoint {
  sessionId: string
  runId: string
  attemptId?: string
  snapshotRef?: string
  /** 逻辑运行内的尝试序号（1 起）；看门狗自动续跑会 +1。 */
  attempt: number
  /** 检查点落笔时的已持久化事件 seq（信息性水位）。 */
  watermark: number
  /** 最后一次**真实进展**的 seq（turn_complete / 已持久化 tool_result）。 */
  progressWatermark: number
  /**
   * 转录水位（观测，PLAN §4 恢复设计第 1 步）：落笔时该会话已 append 的 OAI 消息
   * 条数。**当前只写不判**——恢复端尚未消费；第 2 步才会用它做有效性门。
   */
  transcriptWatermark?: number
  model?: string
  domain?: string
  approvalMode?: string
  /** 最近一次 turn_complete 的累计用量快照（不含正文）。 */
  usage?: Record<string, unknown>
  tools: ToolOutcome[]
  state: 'running' | 'settled'
  /** 无新进展时已消耗的自动恢复次数（闸门用）。 */
  autoResumes: number
  /** 授予上次自动恢复时的 progressWatermark——判断「有无新进展」的基线。 */
  autoResumeProgressAt: number
  updatedAt: number
}

export interface BeginRunInput {
  sessionId: string
  /** 调用方先生成 runId 才能同步记账；缺省由本模块生成。 */
  runId?: string
  attemptId?: string
  /** true = 看门狗自动续跑（同一逻辑运行的又一次尝试）：保留 runId、进展水位与闸门计数。 */
  autoResume?: boolean
  model?: string
  domain?: string
  approvalMode?: string
  watermark: number
}

export type RecoveryPatch = Partial<Pick<
  RecoveryCheckpoint,

  'watermark' | 'model' | 'domain' | 'approvalMode' | 'usage' | 'state' | 'snapshotRef' | 'transcriptWatermark'
>>

/** 工具恢复判定：persisted 可消费；unknown 必须转待确认，不能通用重放。 */
export function classifyToolRecovery(cp: RecoveryCheckpoint): {
  consumable: ToolOutcome[]
  needsConfirmation: ToolOutcome[]
} {
  return {
    consumable: cp.tools.filter(t => t.status === 'persisted'),
    needsConfirmation: cp.tools.filter(t => t.status === 'unknown'),
  }
}

/**
 * 自动恢复决策。`allowed=false` 时必须转待处理，不得自行重跑：
 *   · no-checkpoint          —— 没有可靠检查点（台账不可用），退回既有路径由调用方决定；
 *   · unknown-tool-outcome   —— 有工具已发出但结果未知（写/发送/外部副作用不可逆），
 *                              不能通用自动重放，必须先人工/工具专属核实；
 *   · auto-resume-exhausted  —— 同一逻辑运行没有新真实进展，已用掉唯一一次自动恢复。
 */
export interface AutoResumeDecision {
  allowed: boolean
  reason?: 'no-checkpoint' | 'unknown-tool-outcome' | 'auto-resume-exhausted'
  checkpoint?: RecoveryCheckpoint
  /** reason='unknown-tool-outcome' 时：结果未知、需先核实的 tool id 列表。 */
  unknownTools?: string[]
}

/** 只读恢复检查结果（inspect 产物，不产生副作用）。 */
export interface RecoveryInspection {
  checkpoint: RecoveryCheckpoint
  /** 已持久化、恢复时可消费、不重跑的工具结果。 */
  consumable: ToolOutcome[]
  /** 已发出但结果未知、必须先核实的工具。 */
  needsConfirmation: ToolOutcome[]
}

export class RecoveryJournal {
  /** Per-session serialization: read-modify-write must not interleave. */
  private locks = new Map<string, Promise<unknown>>()

  constructor(private root: string) {}

  dir(sessionId: string): string { return join(this.root, sessionId) }

  async beginRun(input: BeginRunInput): Promise<RecoveryCheckpoint> {
    return this.serialize(input.sessionId, async () => {
      // 自动续跑是**同一逻辑运行**的又一次尝试：保留 runId、attempt 递增、继承
      // 进展水位与闸门计数。用户新发 prompt（autoResume 缺省 false）= 新逻辑运行，
      // 一切归零。
      const previous = input.autoResume ? await this.load(input.sessionId) : undefined
      if (input.autoResume && (!previous?.snapshotRef || previous.tools.some(t => t.status === 'unknown'))) {
        throw new Error('Automatic recovery requires a complete checkpoint with no unknown tool results')
      }
      const cp: RecoveryCheckpoint = {
        sessionId: input.sessionId,
        runId: previous?.runId ?? input.runId ?? randomUUID(),
        attemptId: input.attemptId ?? randomUUID(),
        attempt: previous ? previous.attempt + 1 : 1,
        watermark: input.watermark,
        progressWatermark: previous?.progressWatermark ?? 0,
        model: input.model ?? previous?.model,
        domain: input.domain ?? previous?.domain,
        approvalMode: input.approvalMode ?? previous?.approvalMode,
        tools: previous?.tools ?? [],
        state: 'running',
        autoResumes: previous?.autoResumes ?? 0,
        autoResumeProgressAt: previous?.autoResumeProgressAt ?? 0,
        updatedAt: Date.now(),
      }
      await this.write(cp)
      return cp
    })
  }

  /** 工具事实：onToolUse 记 unknown，终态 onToolResult 覆写为 persisted。 */
  async recordTool(
    sessionId: string,
    outcome: { id: string; name: string; status: ToolRecoveryStatus; at?: number; watermark?: number; resultRef?: string; intentRef?: string },
  ): Promise<void> {
    await this.mutate(sessionId, (cp) => {
      const tools = cp.tools.filter(t => t.id !== outcome.id)
      const previous = cp.tools.find(t => t.id === outcome.id)
      tools.push({ ...previous, id: outcome.id, name: outcome.name, status: outcome.status, at: outcome.at ?? Date.now(),
        ...(outcome.resultRef ? { resultRef: outcome.resultRef } : {}),
        ...(outcome.intentRef ? { intentRef: outcome.intentRef } : {}),
      })
      // 已持久化的工具结果 = 真实进展，抬高进展水位。
      const progressWatermark = outcome.status === 'persisted' && outcome.watermark !== undefined
        ? Math.max(cp.progressWatermark, outcome.watermark)
        : cp.progressWatermark
      return { ...cp, tools, progressWatermark }
    })
  }

  /** 真实进展（turn_complete）：抬高进展水位，并顺带记下用量快照。 */
  async recordProgress(sessionId: string, watermark: number, usage?: Record<string, unknown>): Promise<void> {
    await this.mutate(sessionId, (cp) => ({
      ...cp,
      progressWatermark: Math.max(cp.progressWatermark, watermark),
      ...(usage ? { usage } : {}),
    }))
  }

  async checkpoint(sessionId: string, patch: RecoveryPatch): Promise<void> {
    await this.mutate(sessionId, (cp) => ({
      ...cp,
      ...patch,
      // 水位只进不退：并发/迟到的 checkpoint 不得把已落盘的历史降回去。
      watermark: Math.max(cp.watermark, patch.watermark ?? cp.watermark),
    }))
  }

  /** Immutable dependencies are committed before publishing their reference. */
  async saveDependency(sessionId: string, value: unknown): Promise<string> {
    const dir = this.dir(sessionId)
    await mkdir(dir, { recursive: true })
    const ref = `${randomUUID()}.json`
    const file = await open(join(dir, ref), 'wx', 0o600)
    try { await file.writeFile(JSON.stringify(value)); await file.sync() }
    finally { await file.close() }
    return ref
  }

  /**
   * 落一份**完整**检查点：快照 + 把带 resultRef 的工具提升为 persisted。
   * `transcriptWatermark` 是观测字段（PLAN §4 第 1 步：只写不判）——它描述「收尾时
   * 模型转录里有多少条消息」，属于任何一份检查点，所以两个写口都记它。
   */
  async commitSnapshot(sessionId: string, watermark: number, value: unknown, transcriptWatermark?: number): Promise<void> {
    const snapshotRef = await this.saveDependency(sessionId, value)
    await this.serialize(sessionId, async () => {
      const previous = await this.load(sessionId)
      if (!previous) throw new Error('Recovery checkpoint unavailable')
      const cp: RecoveryCheckpoint = { ...previous, snapshotRef, watermark, state: 'settled', updatedAt: Date.now(),
        tools: previous.tools.map(tool => tool.resultRef ? { ...tool, status: 'persisted' as const } : tool),
        ...(transcriptWatermark !== undefined ? { transcriptWatermark } : {}),
      }
      // A new running ledger must never destroy the last complete manifest.
      const manifestRef = await this.saveDependency(sessionId, cp)
      await rename(join(this.dir(sessionId), manifestRef), join(this.dir(sessionId), 'last-complete.json'))
      await this.write(cp)
    })
  }

  async load(sessionId: string): Promise<RecoveryCheckpoint | undefined> {
    try {
      const raw = await readFile(join(this.dir(sessionId), 'checkpoint.json'), 'utf8')
      const parsed = JSON.parse(raw) as RecoveryCheckpoint
      if (!parsed || parsed.sessionId !== sessionId || typeof parsed.runId !== 'string'
        || ![parsed.watermark, parsed.progressWatermark, parsed.autoResumes, parsed.autoResumeProgressAt, parsed.attempt]
          .every(n => Number.isSafeInteger(n) && n >= 0)
        || !Array.isArray(parsed.tools)
        || parsed.tools.some(t => !t || typeof t.id !== 'string' || !['unknown', 'persisted'].includes(t.status))) return undefined
      return parsed
    } catch {
      // 缺失 / 半写 / 非法 → 视为「没有可靠检查点」，绝不抛。
      return undefined
    }
  }

  /**
   * 自动恢复闸门：
   *   1. 有「结果未知」的工具 → 直接阻断（unknown-tool-outcome），不消耗预算，
   *      因为这类副作用不可通用重放；核实后才能再谈恢复。
   *   2. 同一逻辑运行在没有新**真实进展**时最多自动恢复一次。
   * 整体串行：读到的必须是在途写入之后的态（内部只调 write，避免自锁）。
   */
  async recoveryDecision(sessionId: string): Promise<AutoResumeDecision> {
    return this.serialize(sessionId, async () => {
      const cp = await this.load(sessionId)
      if (!cp) return { allowed: false, reason: 'no-checkpoint' }
      // ① 结果未知的工具优先报出：这是最可行动的信号（模型对现实的认知可能已经不对），
      //    而且只要检查点还在就能点名。**必须排在有效性门之前**——被中断的 run 往往还没
      //    落下完整快照，若先判完整性，这类场景会退化成笼统的 no-checkpoint，用户就丢了
      //    「到底哪个工具要核实」这条信息；main 原先的理由推导也是先看 unknown 工具。
      const needsConfirmation = classifyToolRecovery(cp).needsConfirmation
      if (needsConfirmation.length > 0) {
        return {
          allowed: false,
          reason: 'unknown-tool-outcome',
          checkpoint: cp,
          unknownTools: needsConfirmation.map(t => t.id),
        }
      }
      // ② 有效性门（并入 main 时保留）：检查点要「完整」才算数——快照与每个已持久化
      //    工具的终态依赖都必须真实存在且能解析。缺一份就退回 no-checkpoint（既有路径），
      //    不把它当作可用检查点；否则半写的台账会被当成完整检查点去自动续跑。
      const snapshotRef = cp.snapshotRef
      if (!snapshotRef || !/^[a-f0-9-]+\.json$/.test(snapshotRef)) return { allowed: false, reason: 'no-checkpoint' }
      try { JSON.parse(await readFile(join(this.dir(sessionId), snapshotRef), 'utf8')) }
      catch { return { allowed: false, reason: 'no-checkpoint' } }
      for (const tool of cp.tools) {
        if (tool.status !== 'persisted') continue
        if (!tool.resultRef || !/^[a-f0-9-]+\.json$/.test(tool.resultRef)) return { allowed: false, reason: 'no-checkpoint' }
        try { JSON.parse(await readFile(join(this.dir(sessionId), tool.resultRef), 'utf8')) }
        catch { return { allowed: false, reason: 'no-checkpoint' } }
      }
      if (cp.progressWatermark > cp.autoResumeProgressAt) {
        // 上次自动恢复之后有新进展 → 允许一次全新尝试，并以当前进展为新基线。
        await this.write({ ...cp, autoResumes: 1, autoResumeProgressAt: cp.progressWatermark, updatedAt: Date.now() })
        return { allowed: true, checkpoint: cp }
      }
      if (cp.autoResumes >= 1) return { allowed: false, reason: 'auto-resume-exhausted', checkpoint: cp }
      await this.write({ ...cp, autoResumes: 1, autoResumeProgressAt: cp.progressWatermark, updatedAt: Date.now() })
      return { allowed: true, checkpoint: cp }
    })
  }

  /** 便捷布尔面（既有调用方 / 测试）：只有明确允许才 true。 */
  async mayAutoResume(sessionId: string): Promise<boolean> {
    return (await this.recoveryDecision(sessionId)).allowed
  }

  /**
   * 等本会话（或全部）在途写入落定。测试收尾 / 关停前用：台账写入是
   * fire-and-forget，删数据目录前不等它会让 mkdir 撞上已删目录（unhandled
   * rejection）。返回后仍可能被新写入续接，只保证「此刻已入队的」已落定。
   */
  async flush(sessionId?: string): Promise<void> {
    const pending = sessionId ? [this.locks.get(sessionId)] : [...this.locks.values()]
    await Promise.allSettled(pending.filter((p): p is Promise<unknown> => p !== undefined))
  }

  /**
   * 只读检查（不写盘、**不消耗自动恢复预算**）：检查点 + 工具恢复分类。
   * 供 UI / 诊断 / 恢复入口在不触发恢复的前提下展示真实状态。
   */
  async inspect(sessionId: string): Promise<RecoveryInspection | undefined> {
    const checkpoint = await this.load(sessionId)
    if (!checkpoint) return undefined
    const { consumable, needsConfirmation } = classifyToolRecovery(checkpoint)
    return { checkpoint, consumable, needsConfirmation }
  }

  private async mutate(
    sessionId: string,
    fn: (cp: RecoveryCheckpoint) => RecoveryCheckpoint,
  ): Promise<void> {
    await this.serialize(sessionId, async () => {
      const cp = await this.load(sessionId)
      if (!cp) throw new Error('Recovery checkpoint missing or unreadable')
      await this.write({ ...fn(cp), updatedAt: Date.now() })
    })
  }

  private serialize<T>(sessionId: string, op: () => Promise<T>): Promise<T> {
    const previous = this.locks.get(sessionId) ?? Promise.resolve()
    const next = previous.catch(() => {}).then(op)
    this.locks.set(sessionId, next)
    return next.finally(() => {
      if (this.locks.get(sessionId) === next) this.locks.delete(sessionId)
    })
  }

  private async write(cp: RecoveryCheckpoint): Promise<void> {
    const dir = this.dir(cp.sessionId)
    await mkdir(dir, { recursive: true })
    const path = join(dir, 'checkpoint.json')
    const temporary = `${path}.${randomUUID()}.tmp`
    const file = await open(temporary, 'wx', 0o600)
    try {
      await file.writeFile(JSON.stringify(cp))
      await file.sync()
    } finally {
      await file.close()
    }
    await rename(temporary, path)
    // 追加日志（best-effort，仅供取证；原子真源是 checkpoint.json）。
    await appendFile(join(dir, 'checkpoint.jsonl'), `${JSON.stringify(cp)}\n`, { mode: 0o600 }).catch(() => {})
  }
}
