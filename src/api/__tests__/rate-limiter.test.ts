import { describe, it, beforeEach, mock } from 'node:test'
import assert from 'node:assert/strict'
import { TokenBucket, getProviderRateLimiter, resetRateLimiterRegistry } from '../rate-limiter.js'

// 时间敏感的断言用受控时钟（mock.timers 接管 setTimeout 与 Date）——等待量是
// 实现属性而非机器速度，断言可以精确到 ===，不再需要为 CI 抖动放宽余量。

describe('TokenBucket', () => {
  it('lets the initial burst through without waiting', async () => {
    // 原 `elapsed < 25` 是赌事件循环不被挤爆的上界：突发额度内的 acquire 不排
    // 任何定时器，受控时钟零推进才是这个性质的诚实形状。
    mock.timers.enable({ apis: ['setTimeout', 'Date'] })
    try {
      const bucket = new TokenBucket({ requestsPerSecond: 20, burst: 3 }) // interval 50ms
      const started = Date.now()
      await bucket.acquire()
      await bucket.acquire()
      await bucket.acquire()
      assert.equal(Date.now() - started, 0, `burst of 3 should not wait, mock clock advanced ${Date.now() - started}ms`)
    } finally {
      mock.timers.reset()
    }
  })

  it('shapes the rate after the burst is spent', async () => {
    // 原 `elapsed >= 15 && < 200` 双侧余量窗在共享 runner 上两头都可能误伤
    // （慢机器超上界、定时器早触发啃下界）。受控时钟下逐槽放行，总等待精确
    // 等于两个 interval。
    mock.timers.enable({ apis: ['setTimeout', 'Date'] })
    try {
      const bucket = new TokenBucket({ requestsPerSecond: 100, burst: 1 }) // interval 10ms
      const started = Date.now()
      await bucket.acquire() // burst 内，立即可用
      const second = bucket.acquire() // 排在 started+10
      mock.timers.tick(10)
      await second
      const third = bucket.acquire() // 排在 started+20
      mock.timers.tick(10)
      await third
      assert.equal(Date.now() - started, 20, `two follow-up acquires must wait exactly two intervals, got ${Date.now() - started}ms`)
    } finally {
      mock.timers.reset()
    }
  })

  it('rejects immediately when the signal is already aborted', async () => {
    const bucket = new TokenBucket({ requestsPerSecond: 1, burst: 1 })
    const controller = new AbortController()
    controller.abort()
    await assert.rejects(
      () => bucket.acquire(controller.signal),
      (err: unknown) => {
        assert.ok(err instanceof DOMException)
        assert.equal(err.name, 'AbortError')
        return true
      },
    )
  })
})

describe('getProviderRateLimiter', () => {
  beforeEach(() => resetRateLimiterRegistry())

  it('returns undefined when no config is given (feature off by default)', () => {
    assert.equal(getProviderRateLimiter('p1'), undefined)
  })

  it('shares one bucket per provider key', () => {
    const a = getProviderRateLimiter('p1', { requestsPerSecond: 5 })
    const b = getProviderRateLimiter('p1', { requestsPerSecond: 5 })
    assert.ok(a !== undefined && b !== undefined)
    assert.equal(a, b, 'same key + same config must reuse the bucket (cross-instance sharing)')
  })

  it('rebuilds the bucket when the config fingerprint changes', () => {
    const a = getProviderRateLimiter('p1', { requestsPerSecond: 5 })
    const b = getProviderRateLimiter('p1', { requestsPerSecond: 9 })
    assert.ok(a !== undefined && b !== undefined)
    assert.notEqual(a, b)
  })

  it('keeps different providers isolated', () => {
    const a = getProviderRateLimiter('p1', { requestsPerSecond: 5 })
    const b = getProviderRateLimiter('p2', { requestsPerSecond: 5 })
    assert.ok(a !== undefined && b !== undefined)
    assert.notEqual(a, b)
  })
})
