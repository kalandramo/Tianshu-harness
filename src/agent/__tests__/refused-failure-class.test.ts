/**
 * refused（设计性拒绝）失败类别的反证测试。
 *
 * 背景：browser_debug 的 fail-closed 允许名单 / 协议白名单拒绝此前落 unknown，
 * 被按「工具坏了」全额罚 vigor 并喂免疫/收敛。本文件钉死三件事：
 *   1. 文案兜底与结构化 errorKind 都能归到 refused，且不可自动重试；
 *   2. 免疫与收敛豁免判据只对 refused（及环境缺失）生效，不误伤真实失败；
 *   3. vigor 减罚档位 = permission_denied 档（0.2× unknown）。
 */
import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { classifyFailure, classifyToolFailure, isTransient } from '../failure-classifier.js'
import { isConvergenceTransient, isImmunityNeutralized } from '../tool-history-recorder.js'
import { createVigorState, updateVigor } from '../vigor.js'
import type { Sensorium } from '../sensorium.js'

function makeSensorium(overrides: Partial<Sensorium> = {}): Sensorium {
  return {
    momentum: 0.5,
    pressure: 0.3,
    confidence: 0.7,
    complexity: 0.3,
    freshness: 0.5,
    stability: 0.8,
    ...overrides,
  }
}

describe('refused（设计性拒绝）失败分类', () => {
  it('允许名单拦截文案 → refused（即使忘了打 errorKind）', () => {
    const result = classifyFailure(
      'browser_debug 已拦截：主机 "atomgit.com" 不是回环地址且不在许可名单中（fail-closed）。'
      + '当前仅 localhost 可访问——其他主机请设置 RIVET_BROWSER_ALLOWLIST。',
    )
    assert.equal(result.class, 'refused')
    assert.equal(result.retryable, false)
  })

  it('协议白名单文案 → refused', () => {
    const result = classifyFailure('不支持的协议：about:。仅允许 http/https。')
    assert.equal(result.class, 'refused')
    assert.equal(result.retryable, false)
  })

  it('结构化 errorKind=refused 优先级最高且不可重试', () => {
    const result = classifyToolFailure({ errorKind: 'refused' }, '任意文案')
    assert.equal(result.class, 'refused')
    assert.equal(result.retryable, false)
    assert.equal(result.confidence, 1)
  })

  it('refused 不是 transient——不得进入自动重试通道', () => {
    assert.equal(isTransient('refused'), false)
  })

  it('refused 不吞掉真实语义失败：TypeError 仍归 syntax/type 系', () => {
    const result = classifyFailure('page.evaluate: TypeError: Cannot read properties of null (reading "match")')
    assert.notEqual(result.class, 'refused')
  })
})

describe('refused 的免疫/收敛豁免', () => {
  it('免疫豁免：refused 与 environment 生效，其余失败不豁免', () => {
    assert.equal(isImmunityNeutralized(undefined, 'refused'), true)
    assert.equal(isImmunityNeutralized('environment', undefined), true)
    assert.equal(isImmunityNeutralized(undefined, 'timeout'), false)
    assert.equal(isImmunityNeutralized(undefined, 'type_error'), false)
  })

  it('收敛豁免：refused 不计 errorPenalty，普通失败照常计', () => {
    assert.equal(isConvergenceTransient('refused'), true)
    assert.equal(isConvergenceTransient('syntax_error'), true)
    assert.equal(isConvergenceTransient('unknown'), false)
    assert.equal(isConvergenceTransient('assertion'), false)
  })
})

describe('refused 的 vigor 减罚', () => {
  it('refused phasic = unknown 的 0.2 倍（permission_denied 档）', () => {
    const prev = createVigorState()
    const unknownNext = updateVigor(prev, { toolSuccess: false, failureClass: 'unknown', sensorium: makeSensorium() })
    const refusedNext = updateVigor(prev, { toolSuccess: false, failureClass: 'refused', sensorium: makeSensorium() })

    assert.ok(refusedNext.vigor > unknownNext.vigor, '设计性拒绝的惩罚必须小于未知失败')
    assert.ok(Math.abs(refusedNext.phasic - 0.2 * unknownNext.phasic) < 1e-9,
      `refused phasic 应为 unknown 的 0.2 倍，got ${refusedNext.phasic} vs ${unknownNext.phasic}`)
  })
})
