/**
 * 定时任务显式审批档位（issue #259 · 收编公开仓 PR #261）——透传链的不变量测试。
 *
 * 背景：定时任务在 win32（无沙箱后端）上，凡是需要审批的工具调用都会撞上
 * `session.unattended` 的 fail-closed 中止——任务定义里此前没有任何审批字段，
 * 用户无处可解。本收编补的是一条**同形透传**（与 reviewPolicy 同形状）：
 *
 *   ScheduledTask.approval
 *     → TaskDueMeta.approvalMode        (cron-scheduler)
 *     → CreateTaskInput.approvalMode    (cron-wiring)
 *     → TaskRecord.approvalMode         (task-store / task-registry)
 *     → RuntimeHandle.execute options   (task-registry)
 *     → createSession({ approvalMode }) (session-runtime-pool)
 *
 * 三条不变量（与 PR 一致，也是最容易在后续迭代里被无声改坏的地方）：
 *  ① 声明必须一路到达 `RuntimeHandle.execute` 的 options;
 *  ② **未声明时零差异**——options / createSession 入参里不得出现该键
 *     （默认 fail-closed 语义不变，否则是安全默认的静默漂移）;
 *  ③ 最后一跳由 `SessionRuntimePool` 交给 `manager.createSession`。
 *
 * 刻意不测端到端行为（真实 runtime + 沙箱 + 真实审批卡片），那需要起 agent；
 * 这里钉的是「声明能不能传到消费点」与「缺省有没有被动过」。
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { TaskRegistry, type RuntimeHandle, type RuntimePool, type RuntimeResult } from '../task-registry.js'
import type { TaskRecord, TaskStore } from '../task-store.js'
import { SessionRuntimePool, type SessionRuntimePoolOptions } from '../session-runtime-pool.js'
import { CronScheduler, createScheduledTask, type TaskDueMeta } from '../cron-scheduler.js'
import { CronWiring } from '../cron-wiring.js'
import type { RuntimeSessionManager } from '../session-manager.js'

const settle = async () => {
  await new Promise((r) => setImmediate(r))
  await new Promise((r) => setTimeout(r, 10))
}

/** 最小 store：只为 createTask 的 find→save 路径服务。 */
function makeStore(): TaskStore {
  const map = new Map<string, TaskRecord>()
  return {
    async save(t: TaskRecord) { map.set(t.id, t) },
    async load(id: string) { return map.get(id) ?? null },
    async list() { return [...map.values()] },
    async delete(id: string) { map.delete(id) },
    async findActiveByIdempotencyKey() { return null },
  } as unknown as TaskStore
}

/** 捕获 handle.execute 收到的 options（第 5 个参数）。 */
function makePool() {
  const captured: Array<Record<string, unknown> | undefined> = []
  const pool = {
    size: 1,
    async acquire(): Promise<RuntimeHandle> {
      return {
        async execute(_prompt, _signal, _tools, _onStart, options) {
          captured.push(options as Record<string, unknown> | undefined)
          return { summary: 'ok', changedFiles: [] } as RuntimeResult
        },
        release() {},
      }
    },
  } as unknown as RuntimePool
  return { pool, captured }
}

// ── ① / ② ────────────────────────────────────────────────────

test('createTask：approvalMode 透传到 handle.execute（① 透传到底）', async () => {
  const { pool, captured } = makePool()
  const reg = new TaskRegistry({ taskStore: makeStore(), runtimePool: pool })

  await reg.createTask({
    prompt: 'p',
    source: 'cron',
    callerId: 'cron-scheduler',
    approvalMode: 'auto-safe',
  })
  await settle()

  assert.equal(captured.length, 1, '应执行一次')
  assert.equal(captured[0]?.['approvalMode'], 'auto-safe')
})

test('createTask：未声明时 options 不含 approvalMode 键（② 缺省零差异）', async () => {
  const { pool, captured } = makePool()
  const reg = new TaskRegistry({ taskStore: makeStore(), runtimePool: pool })

  await reg.createTask({ prompt: 'p', source: 'cron', callerId: 'cron-scheduler' })
  await settle()

  assert.equal(captured.length, 1)
  assert.equal(
    Object.prototype.hasOwnProperty.call(captured[0] ?? {}, 'approvalMode'),
    false,
    '缺省不得注入 approvalMode——否则语义从 fail-closed 漂移',
  )
  // unattended 的既有行为不受影响
  assert.equal(captured[0]?.['unattended'], false)
})

test('createTask：unattended 与 approvalMode 可并存且各自独立', async () => {
  const { pool, captured } = makePool()
  const reg = new TaskRegistry({ taskStore: makeStore(), runtimePool: pool })

  await reg.createTask({
    prompt: 'p',
    source: 'cron',
    callerId: 'cron-scheduler',
    unattended: true,
    approvalMode: 'auto-safe',
  })
  await settle()

  assert.equal(captured[0]?.['unattended'], true)
  assert.equal(captured[0]?.['approvalMode'], 'auto-safe')
})

// ── ③ 最后一跳 ────────────────────────────────────────────────

/** 最小 manager：只记录 createSession 的入参，其余方法不参与本用例。 */
function makeManager() {
  const created: Array<Record<string, unknown>> = []
  const manager = {
    createSession(opts: Record<string, unknown>) {
      created.push(opts)
      return { id: 'sess-1' }
    },
    // runAndWait 挂起：本用例只验 createSession 的入参，不关心 run 的终局
    runAndWait() { return new Promise<void>(() => {}) },
    abort() {},
  } as unknown as RuntimeSessionManager
  return { manager, created }
}

test('SessionRuntimePool：approvalMode 交给 createSession（③ 最后一跳）', async () => {
  const { manager, created } = makeManager()
  const pool = new SessionRuntimePool({
    manager,
    defaultCwd: '/w',
  } as unknown as SessionRuntimePoolOptions)

  const handle = await pool.acquire('task-1')
  void handle.execute('p', new AbortController().signal, undefined, undefined, {
    unattended: true,
    approvalMode: 'auto-safe',
  })
  await settle()

  assert.equal(created.length, 1)
  assert.equal(created[0]?.['approvalMode'], 'auto-safe')
  assert.equal(created[0]?.['unattended'], true, 'unattended 既有行为不变')
  assert.equal(created[0]?.['cwd'], '/w')
})

test('SessionRuntimePool：缺省时不向 createSession 注入 approvalMode（② 零点）', async () => {
  const { manager, created } = makeManager()
  const pool = new SessionRuntimePool({
    manager,
    defaultCwd: '/w',
  } as unknown as SessionRuntimePoolOptions)

  const handle = await pool.acquire('task-2')
  void handle.execute('p', new AbortController().signal, undefined, undefined, { unattended: true })
  await settle()

  assert.equal(created.length, 1)
  assert.equal(
    Object.prototype.hasOwnProperty.call(created[0] ?? {}, 'approvalMode'),
    false,
    '缺省不得注入——createSession 侧应保持既有默认档位',
  )
})

// ── 全链路：任务定义 → 定时的 TaskDueMeta → 执行 options ────────

test('全链路：ScheduledTask.approval 经 scheduler→wiring→registry 到达 execute', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'rivet-sched-approval-'))
  try {
    const scheduler = new CronScheduler({ schedulePath: join(dir, 'sched.json') })
    const { pool, captured } = makePool()
    const registry = new TaskRegistry({ taskStore: makeStore(), runtimePool: pool })
    // 构造即接线：subscribeTaskDue → registry.createTask
    const wiring = new CronWiring({ scheduler, registry })

    const task = createScheduledTask('p', { type: 'interval', spec: '60000' }, [], {
      approval: 'auto-safe',
    })
    scheduler.add(task)
    scheduler.runNow(task.id)
    await settle()

    assert.equal(captured.length, 1, '试跑一次应执行一次')
    assert.equal(captured[0]?.['approvalMode'], 'auto-safe', '任务定义里的档位必须到达消费点')
    wiring.dispose()
  } finally { rmSync(dir, { recursive: true, force: true }) }
})

test('全链路：任务未声明 approval 时，执行 options 里不得出现该键', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'rivet-sched-approval-'))
  try {
    const scheduler = new CronScheduler({ schedulePath: join(dir, 'sched.json') })
    const { pool, captured } = makePool()
    const registry = new TaskRegistry({ taskStore: makeStore(), runtimePool: pool })
    const wiring = new CronWiring({ scheduler, registry })

    const task = createScheduledTask('p', { type: 'interval', spec: '60000' })
    scheduler.add(task)
    scheduler.runNow(task.id)
    await settle()

    assert.equal(captured.length, 1)
    assert.equal(
      Object.prototype.hasOwnProperty.call(captured[0] ?? {}, 'approvalMode'),
      false,
      '缺省路径一个键都不许多——这是「默认零差异」的判据',
    )
    wiring.dispose()
  } finally { rmSync(dir, { recursive: true, force: true }) }
})

test('fireTask：approval 与 unattended 各自独立上报（试跑恒有人值守不改 approval）', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'rivet-sched-approval-'))
  try {
    const scheduler = new CronScheduler({ schedulePath: join(dir, 'sched.json') })
    const seen: TaskDueMeta[] = []
    scheduler.subscribeTaskDue(async (_p, _t, _a, meta) => { seen.push(meta ?? {}) })

    const task = createScheduledTask('p', { type: 'interval', spec: '60000' }, [], {
      approval: 'auto-accept',
      reviewPolicy: 'auto-proceed',
    })
    scheduler.add(task)
    scheduler.runNow(task.id)
    await settle()

    assert.equal(seen.length, 1)
    assert.equal(seen[0]?.approvalMode, 'auto-accept', 'approval 与是否无人值守无关')
    assert.equal(seen[0]?.unattended, false, '试跑恒有人值守（既有语义不变）')
  } finally { rmSync(dir, { recursive: true, force: true }) }
})
