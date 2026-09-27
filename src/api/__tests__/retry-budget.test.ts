/**
 * PLAN §3——共享重试预算：provider 与 agent 两层 `take()` 同一份，防相乘。
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { RetryBudget, createRunRetryBudget } from '../retry-budget.js'

test('两层共享同一个预算：合计不超上限', () => {
  const budget = createRunRetryBudget(3)
  assert.equal(budget.take(), true)   // provider 重试 1
  assert.equal(budget.take(), true)   // provider 重试 2
  assert.equal(budget.take(), true)   // agent 重连 1
  assert.equal(budget.take(), false, '两层各自无限重试会相乘，共享预算必须封顶')
  assert.equal(budget.remaining(), 0)
})

test('滑动窗口到期后额度回收', () => {
  const budget = new RetryBudget({ maxAttempts: 2, windowMs: 1_000 })
  assert.equal(budget.take(0), true)
  assert.equal(budget.take(500), true)
  assert.equal(budget.remaining(900), 0)
  assert.equal(budget.take(900), false)
  assert.equal(budget.remaining(1_100), 1, '第一条已滑出窗口')
  assert.equal(budget.take(1_100), true)
})

test('reset 清零（用户取消 → 放弃已耗额度）', () => {
  const budget = createRunRetryBudget(1)
  assert.equal(budget.take(), true)
  assert.equal(budget.take(), false)
  budget.reset()
  assert.equal(budget.take(), true, '新任务/新窗口重新计数')
})

test('非法 maxAttempts 直接拒绝', () => {
  assert.throws(() => new RetryBudget({ maxAttempts: -1 }))
  assert.throws(() => new RetryBudget({ maxAttempts: 1.5 }))
})
