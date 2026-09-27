/**
 * bash 工具对「全量类型检查」形态的超时契约。
 *
 * ## 为什么需要这份契约
 * bash 工具会把 typecheck 形态的命令送进跨进程共享闸门
 * （`executeBashMaybeSerialized` → `runAdhocTypecheckShared`）。闸门的所有会话与
 * 隔离 worktree **共用一把锁**（worktree 的 node_modules 是指向主仓的 symlink，
 * 缓存目录物理上是同一把），持锁者满载时等待预算是 10 分钟
 * （`typecheck-cache.ts` 的 `DEFAULT_WAIT_BUDGET_MS = STALE_LOCK_MS`）。
 *
 * 而工具管线给的默认预算是 `DEFAULT_TOOL_TIMEOUT_MS = 120_000`
 * （`src/agent/tool-pipeline.ts`）。**工具声明的预算小于它自己选用的闸门预算**，
 * 于是高负载下必然 `[tool-timeout] bash timed out after 120s`——不是命令慢，
 * 是这两个数字从来没对齐过。2026-09-14 在并行审查 worker 里实测复现。
 *
 * ## 2026-09-25：两侧都得对齐，只修一侧不生效
 *
 * 上面那段修的是**声明侧**（`BASH_TOOL.timeoutMs`）。执行侧当时仍按
 * `params.input.timeout` 设 SIGTERM 定时器（`executeBashOnce`），于是模型传
 * 420000 时——实测 21 次 typecheck 调用里 8 次这么传——比闸门预算早 6 分钟落刀：
 * exit=-1，且 `| tail` 会把已有输出一并吞掉（lines=1、零结果）→ ledger 记
 * 「验证超时」→ `deliver_task` 拒绝提交（isError）→ turn-harness 再重试两轮。
 * 交付门 31 次调用 20 次 error，其中 RED 为 0：没有一次是真失败。
 * 修法是 `resolveBashTimeout`（只抬不压）。下面的执行侧用例与声明侧用例
 * 必须一起通过，任一单侧退化都等于这个 bug 复现。
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { BASH_TOOL } from '../bash.js'
import { isTypecheckCommand, resolveCallerTimeoutBudget, TYPECHECK_CALLER_BUDGET_MS } from '../../lsp/typecheck-cache.js'
import type { ToolCallParams } from '../types.js'

/** 闸门等待上限（typecheck-cache.ts 的 STALE_LOCK_MS = 10 分钟）。 */
const GATE_WAIT_BUDGET_MS = 10 * 60_000

/** bash 工具的默认预算固定传 120s——本文件只关心 typecheck 那一支的抬升。 */
const budgetFor = (command: string, requested: number): number =>
  resolveCallerTimeoutBudget(command, requested, 120_000)

function timeoutFor(command: string): number {
  return BASH_TOOL.timeoutMs?.({ input: { command } } as unknown as ToolCallParams) ?? 0
}

/** 声明侧（外层看门狗）预算——可带模型显式传的 timeout，验证「只抬不压」在两侧一致。 */
function declaredFor(command: string, requested?: number): number {
  const input = requested === undefined ? { command } : { command, timeout: requested }
  return BASH_TOOL.timeoutMs?.({ input } as unknown as ToolCallParams) ?? 0
}

test('typecheck 形态拿到的工具级预算必须覆盖闸门等待上限', () => {
  // 这些形态会被 isTypecheckCommand 判为 YES 并因此走闸门——预算小一毫秒，
  // 高负载下就是必然超时。
  const commands = [
    'npm run typecheck',
    'npm run typecheck 2>&1 | tail -25',
    'npx tsc --noEmit',
    'tsc --noEmit',
    'npm exec -- tsc --noEmit',
  ]
  for (const cmd of commands) {
    assert.ok(isTypecheckCommand(cmd), `前提：${cmd} 应被判为 typecheck 形态`)
    assert.ok(
      timeoutFor(cmd) >= GATE_WAIT_BUDGET_MS,
      `${cmd} 的工具预算 ${timeoutFor(cmd)}ms 小于闸门等待上限 ${GATE_WAIT_BUDGET_MS}ms——会必然超时`,
    )
  }
})

test('普通命令保持默认预算，不因这次修复被放大', () => {
  for (const cmd of ['ls -la', 'npm run build', 'npm test', 'git status']) {
    assert.equal(timeoutFor(cmd), 120_000, `${cmd} 不该拿到 typecheck 的长预算`)
  }
})

test('tsc --watch 是长跑形态，不算 typecheck 收口对象', () => {
  // isTypecheckCommand 的注释声明「--watch 等长跑形态不匹配（走后台 job 通道）」，
  // 但 2026-09-14 探针实测它匹配。watch 进程永不退出：送进闸门等于占着锁不放，
  // 真正的 typecheck 反而被它挡住；前台跑还会等到工具超时。
  assert.equal(isTypecheckCommand('tsc --noEmit --watch'), false, 'watch 形态不该进闸门')
  assert.equal(timeoutFor('tsc --noEmit --watch'), 120_000, 'watch 形态保持默认预算')
})

// ─── 执行侧：模型传的小预算不能突破闸门等待（2026-09-25 补齐） ──────────────

test('执行侧预算被抬到闸门上限：模型传的 420s 不再提前落刀', () => {
  // 生产事故形态：复合命令 + 管道（`| tail` 超时时会把已有输出一并吞掉）。
  const incident = 'cd /Users/banxia/app/tianshu-3.15 && npm run typecheck 2>&1 | tail -40'
  assert.equal(budgetFor(incident, 420_000), TYPECHECK_CALLER_BUDGET_MS)
  assert.equal(budgetFor('npm run typecheck', 60_000), TYPECHECK_CALLER_BUDGET_MS)
  assert.equal(budgetFor('npx tsc --noEmit', 5_000), TYPECHECK_CALLER_BUDGET_MS)
})

test('声明侧与执行侧同源：两侧都覆盖闸门等待，且都不压低更大的调用方预算', () => {
  const incident = 'npm run typecheck 2>&1 | tail -40'
  const declared = BASH_TOOL.timeoutMs?.({ input: { command: incident } } as unknown as ToolCallParams) ?? 0
  assert.ok(declared >= GATE_WAIT_BUDGET_MS, '声明侧须覆盖闸门等待（2026-09-14 的修复）')
  assert.ok(budgetFor(incident, 1) >= GATE_WAIT_BUDGET_MS, '执行侧须覆盖闸门等待（2026-09-25 的修复）')
  assert.equal(budgetFor(incident, 900_000), 900_000, '调用方给的更大预算不被压低')
})

test('执行侧不误伤普通命令：非 typecheck 形态照旧用默认或传入预算', () => {
  assert.equal(budgetFor('npm test', 5_000), 5_000)
  assert.equal(budgetFor('ls -la', NaN), 120_000, '非正数/NaN 走默认预算')
  assert.equal(budgetFor('tsc --noEmit --watch', 5_000), 5_000, 'watch 形态不进闸门，也不该被抬升')
})

test('RIVET_TYPECHECK_SHARE=0 逃生口：闸门关闭时执行侧不抬升', () => {
  const prev = process.env.RIVET_TYPECHECK_SHARE
  process.env.RIVET_TYPECHECK_SHARE = '0'
  try {
    assert.equal(budgetFor('npm run typecheck', 60_000), 60_000)
  } finally {
    if (prev === undefined) delete process.env.RIVET_TYPECHECK_SHARE
    else process.env.RIVET_TYPECHECK_SHARE = prev
  }
})

// ─── 两侧的相对关系：声明侧必须严格大于执行侧（2026-09-25 二修） ─────────────

test('声明侧必须严格大于执行侧——否则外层看门狗先落刀，专用文案到不了模型', () => {
  // 机制：外层看门狗在 tool-pipeline 读 toolDef.timeoutMs 武装（execute 之前），
  // 内层 SIGTERM 定时器要等子进程起来才武装。两侧预算**相等**时外层必先 reject，
  // 于是 bash.ts:765 的 TYPECHECK_TIMEOUT_HINT（专门教模型改走 run_in_background
  // 的那段引导）永远投递不出去，模型只拿到通用超时文案——4a6380d5c 想改善的场景
  // 没生效。余量给了内层杀进程 + 整理输出的时间。
  const incident = 'npm run typecheck 2>&1 | tail -40'
  assert.ok(
    declaredFor(incident) > budgetFor(incident, NaN),
    `声明侧 ${declaredFor(incident)}ms 必须 > 执行侧 ${budgetFor(incident, NaN)}ms`,
  )
  // 模型显式传更大预算时两侧都要跟随，且声明侧仍保持严格更大（否则大预算下外层又会先落刀）。
  for (const requested of [60_000, 420_000, 900_000]) {
    const declared = declaredFor(incident, requested)
    const exec = budgetFor(incident, requested)
    assert.ok(
      declared > exec,
      `调用方传 ${requested}ms 时：声明侧 ${declared}ms 仍须 > 执行侧 ${exec}ms`,
    )
  }
})

test('非 typecheck 形态的声明侧预算不变（仍是工具默认 120s）', () => {
  for (const cmd of ['ls -la', 'npm run build', 'git status']) {
    assert.equal(declaredFor(cmd), 120_000, `${cmd} 不该被这次修复放大`)
    assert.equal(declaredFor(cmd, 300_000), 120_000, `${cmd} 的模型预算不参与声明侧抬升`)
  }
})

test('闸门关闭（RIVET_TYPECHECK_SHARE=0）时声明侧仍严格大于执行侧', () => {
  const prev = process.env.RIVET_TYPECHECK_SHARE
  process.env.RIVET_TYPECHECK_SHARE = '0'
  try {
    const cmd = 'npm run typecheck'
    assert.ok(
      declaredFor(cmd, 60_000) > budgetFor(cmd, 60_000),
      '闸门关闭时执行侧不抬升，声明侧也必须跟着退回「执行侧 + 余量」，不能反过来先落刀',
    )
  } finally {
    if (prev === undefined) delete process.env.RIVET_TYPECHECK_SHARE
    else process.env.RIVET_TYPECHECK_SHARE = prev
  }
})
