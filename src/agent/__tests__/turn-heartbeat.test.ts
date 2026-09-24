import { describe, it, mock } from 'node:test'
import assert from 'node:assert/strict'
import { TurnHeartbeat } from '../turn-heartbeat.js'
import { wrapCallbacksWithHeartbeat } from '../turn-orchestrator.js'
import { clearActivity, getLastActivity, touchActivity } from '../stall-observer.js'
import type { AgentCallbacks } from '../loop-types.js'

function delay(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms))
}

describe('TurnHeartbeat', () => {
  it('fires after silentMs of silence', () => {
    // 用受控时钟（mock.timers 同时接管 setTimeout 与 Date），而不是 `await delay(80)` 之后
    // 断言墙上时钟的 `elapsed >= 50`。
    //
    // 为什么必须换：`elapsed` 由实现用 `Date.now() - lastTick` 算（turn-heartbeat.ts 的
    // fire()），而它唯一的漂移守卫是 `elapsed < silentMs - 500` —— 在 silentMs=50 时是
    // **负界**，等于零余量。于是平台定时器的任何亚毫秒早触发都会原样传进断言：CI 上
    // 实测过一次 `AssertionError: elapsed should be >= 50ms, got 49`。
    // 受控时钟让 elapsed 精确等于 silentMs，断言反而**更紧**（=== 50，不是放宽到 >=49）。
    mock.timers.enable({ apis: ['setTimeout', 'Date'] })
    const events: Array<{ elapsed: number; activity: string }> = []
    const hb = new TurnHeartbeat({
      silentMs: 50,
      repeatMs: 50,
      onHeartbeat: (elapsed, activity) => events.push({ elapsed, activity }),
    })
    try {
      hb.start()
      mock.timers.tick(49)
      assert.equal(events.length, 0, '不到 silentMs 不该触发')
      mock.timers.tick(1)
      assert.ok(events.length >= 1, `expected at least 1 heartbeat, got ${events.length}`)
      assert.equal(events[0]!.activity, 'starting')
      assert.equal(events[0]!.elapsed, 50, `elapsed 应精确等于 silentMs，实得 ${events[0]!.elapsed}`)
    } finally {
      hb.stop()
      mock.timers.reset()
    }
  })

  it('does not fire if tick happens before silentMs', () => {
    // 受控时钟版本：tick 间的静默窗口由 mock 时钟推进。原写法 `await delay(40)`×3
    // 赌的是「真实 40ms 不会超过 silentMs=100」——共享 runner 上事件循环被挤爆时
    // delay 实际耗时可以远超 100ms，心跳假触发 → 假红。机器速度不再参与判定。
    mock.timers.enable({ apis: ['setTimeout', 'Date'] })
    const events: Array<{ elapsed: number; activity: string }> = []
    const hb = new TurnHeartbeat({
      silentMs: 100,
      repeatMs: 100,
      onHeartbeat: (e, a) => events.push({ elapsed: e, activity: a }),
    })
    try {
      hb.start()
      mock.timers.tick(40)
      hb.tick('reading file')
      mock.timers.tick(40)
      hb.tick('processing')
      mock.timers.tick(40)
      assert.equal(events.length, 0, 'should not fire when ticks reset the clock')
    } finally {
      hb.stop()
      mock.timers.reset()
    }
  })

  it('reports the most recent activity in heartbeat events', async () => {
    const events: Array<{ activity: string }> = []
    const hb = new TurnHeartbeat({
      silentMs: 40,
      repeatMs: 40,
      onHeartbeat: (_, a) => events.push({ activity: a }),
    })
    hb.start()
    hb.tick('compacting messages')
    await delay(70)
    hb.stop()
    assert.ok(events.length >= 1)
    assert.equal(events[0]!.activity, 'compacting messages')
  })

  it('repeats after first fire at repeatMs interval', () => {
    // 受控时钟：首次触发精确落在 silentMs，其后精确按 repeatMs 等间隔。原断言
    // `gap >= 25 && gap <= 60` / `firstDelay >= 45` 是墙上时钟的余量窗——慢机器
    // 挤爆窗口、或定时器早触发，都会误伤。
    mock.timers.enable({ apis: ['setTimeout', 'Date'] })
    const fireTimes: number[] = []
    const hb = new TurnHeartbeat({
      silentMs: 50,
      repeatMs: 30,
      onHeartbeat: () => fireTimes.push(Date.now()),
    })
    try {
      const t0 = Date.now()
      hb.start()
      mock.timers.tick(50) // t0+50：首次触发（silentMs），重排在 t0+80
      mock.timers.tick(30) // t0+80：第二次（repeatMs），重排在 t0+110
      mock.timers.tick(30) // t0+110：第三次
      assert.deepEqual(fireTimes, [t0 + 50, t0 + 80, t0 + 110])
    } finally {
      hb.stop()
      mock.timers.reset()
    }
  })

  it('stops cleanly on stop()', async () => {
    let count = 0
    const hb = new TurnHeartbeat({
      silentMs: 30,
      repeatMs: 30,
      onHeartbeat: () => { count++ },
    })
    hb.start()
    await delay(50)
    hb.stop()
    const afterStop = count
    await delay(80)
    assert.equal(count, afterStop, 'should not fire after stop()')
  })

  it('survives errors in callback', async () => {
    let calls = 0
    const hb = new TurnHeartbeat({
      silentMs: 30,
      repeatMs: 30,
      onHeartbeat: () => {
        calls++
        throw new Error('callback boom')
      },
    })
    hb.start()
    await delay(100)
    hb.stop()
    // Should keep firing despite the throws
    assert.ok(calls >= 2, `expected >=2 calls despite errors, got ${calls}`)
  })

  describe('hard-stall watchdog', () => {
    it('fires onHardStall once when silence exceeds hardStallMs', () => {
      // 同 `fires after silentMs of silence`：原断言 `elapsed >= hardStallMs` 也是墙上时钟的
      // 精确下界、零余量，同样会被平台定时器的亚毫秒差击穿。改受控时钟后精确断言。
      //
      // 关键：`mock.timers.tick(n)` 是**先把时钟推到 now+n，再执行到期的定时器**，回调里
      // 新排的要等下一次 tick（实测：tick(59) 后回调看到的时间就是 59）。TurnHeartbeat
      // 每次 fire 都按 repeatMs 重排，所以这里必须**按代推进**——一次大 tick 只会跑第一代。
      mock.timers.enable({ apis: ['setTimeout', 'Date'] })
      const stalls: Array<{ elapsed: number; activity: string }> = []
      const hb = new TurnHeartbeat({
        silentMs: 20,
        repeatMs: 20,
        hardStallMs: 60,
        onHeartbeat: () => {},
        onHardStall: (elapsed, activity) => stalls.push({ elapsed, activity }),
      })
      try {
        hb.start()
        hb.tick('read_file returned')
        mock.timers.tick(20) // 第 1 代：elapsed 20 < 60，只出心跳
        assert.equal(stalls.length, 0, '第 1 代不该报硬停滞')
        mock.timers.tick(20) // 第 2 代：elapsed 40
        assert.equal(stalls.length, 0, '第 2 代不该报硬停滞')
        mock.timers.tick(20) // 第 3 代：elapsed 60 → 越界
        assert.equal(stalls.length, 1, `onHardStall must fire exactly once, got ${stalls.length}`)
        assert.equal(stalls[0]!.activity, 'read_file returned')
        assert.equal(stalls[0]!.elapsed, 60, `elapsed 应精确等于 hardStallMs，实得 ${stalls[0]!.elapsed}`)
      } finally {
        hb.stop()
        mock.timers.reset()
      }
    })

    it('does not fire onHardStall when a tick resets the clock in time', () => {
      // 受控时钟版本：原用真实 `delay(30)+tick`×5，赌 30ms 静默窗口撑不到
      // hardStallMs=80——慢机器上 delay 挤爆窗口即假红。mock 时钟下逐代推进，
      // 顺带把「每代恰一次心跳」也收紧成精确断言。
      mock.timers.enable({ apis: ['setTimeout', 'Date'] })
      const stalls: number[] = []
      let beats = 0
      const hb = new TurnHeartbeat({
        silentMs: 20,
        repeatMs: 20,
        hardStallMs: 80,
        onHeartbeat: () => { beats++ },
        onHardStall: (elapsed) => stalls.push(elapsed),
      })
      try {
        hb.start()
        // Tick every 30ms — never silent for the full 80ms ceiling.
        for (let i = 0; i < 5; i++) {
          mock.timers.tick(30) // 静默 30ms：心跳触发（≥silentMs）但远未到 80
          hb.tick(`activity ${i}`) // 时钟归零重排——永远到不了 hardStallMs
        }
        assert.equal(stalls.length, 0, 'watchdog must not fire while ticks keep arriving')
        assert.equal(beats, 5, '每代恰一次心跳')
      } finally {
        hb.stop()
        mock.timers.reset()
      }
    })

    it('re-arms the watchdog after a tick (fires again on a second stall)', async () => {
      const stalls: number[] = []
      const hb = new TurnHeartbeat({
        silentMs: 20,
        repeatMs: 20,
        hardStallMs: 50,
        onHeartbeat: () => {},
        onHardStall: (elapsed) => stalls.push(elapsed),
      })
      hb.start()
      await delay(90)        // first stall fires
      hb.tick('recovered')   // re-arm
      await delay(90)        // second stall fires
      hb.stop()
      assert.ok(stalls.length >= 2, `expected watchdog to re-arm and fire twice, got ${stalls.length}`)
    })

    it('disables the watchdog when hardStallMs is 0', async () => {
      const stalls: number[] = []
      const hb = new TurnHeartbeat({
        silentMs: 20,
        repeatMs: 20,
        hardStallMs: 0,
        onHeartbeat: () => {},
        onHardStall: (elapsed) => stalls.push(elapsed),
      })
      hb.start()
      await delay(120)
      hb.stop()
      assert.equal(stalls.length, 0, 'hardStallMs=0 must disable the watchdog')
    })

    it('keeps emitting heartbeats after a hard stall fires', async () => {
      let beats = 0
      let stalls = 0
      const hb = new TurnHeartbeat({
        silentMs: 20,
        repeatMs: 20,
        hardStallMs: 50,
        onHeartbeat: () => { beats++ },
        onHardStall: () => { stalls++ },
      })
      hb.start()
      await delay(140)
      hb.stop()
      assert.equal(stalls, 1, 'hard stall fires once')
      assert.ok(beats >= 3, `heartbeats keep emitting while abort propagates, got ${beats}`)
    })
  })

  describe('pause / resume', () => {
    it('pause prevents heartbeats from firing', async () => {
      let count = 0
      const hb = new TurnHeartbeat({
        silentMs: 30,
        repeatMs: 30,
        onHeartbeat: () => { count++ },
      })
      hb.start()
      hb.pause()
      await delay(120)
      assert.equal(count, 0, 'pause must suppress heartbeats')
      hb.stop()
    })

    it('resume restarts heartbeat after pause', () => {
      // 受控时钟版本：原 `events[0] - t0 >= 35`（silentMs=40 只留 5ms 余量）是
      // 墙上时钟下界；mock 时钟下精确 === 40，且多断一个「39ms 时还没触发」。
      mock.timers.enable({ apis: ['setTimeout', 'Date'] })
      const events: number[] = []
      const hb = new TurnHeartbeat({
        silentMs: 40,
        repeatMs: 40,
        onHeartbeat: () => events.push(Date.now()),
      })
      try {
        hb.start()
        hb.pause()
        mock.timers.tick(80) // 暂停期间定时器已清，时钟空转无事发生
        assert.equal(events.length, 0, 'still paused — no events')
        const t0 = Date.now()
        hb.resume() // 重排在 t0+40
        mock.timers.tick(39)
        assert.equal(events.length, 0, '39ms < silentMs 不触发')
        mock.timers.tick(1) // t0+40：触发
        assert.equal(events.length, 1, `expected at least 1 heartbeat after resume, got ${events.length}`)
        assert.equal(events[0]! - t0, 40, 'first heartbeat after resume should respect silentMs')
      } finally {
        hb.stop()
        mock.timers.reset()
      }
    })

    it('tick exits pause and resets the clock', () => {
      // 同上：`events[0] - t0 >= 35` 收紧为受控时钟精确断言。
      mock.timers.enable({ apis: ['setTimeout', 'Date'] })
      const events: number[] = []
      const hb = new TurnHeartbeat({
        silentMs: 40,
        repeatMs: 40,
        onHeartbeat: () => events.push(Date.now()),
      })
      try {
        hb.start()
        hb.pause()
        mock.timers.tick(80)
        // tick while paused should exit pause and reset clock
        const t0 = Date.now()
        hb.tick('activity after pause') // 退出暂停并重排在 t0+40
        mock.timers.tick(40)
        assert.equal(events.length, 1, `tick should resume and heartbeat fires after silentMs, got ${events.length}`)
        assert.equal(events[0]! - t0, 40)
      } finally {
        hb.stop()
        mock.timers.reset()
      }
    })

    it('hard-stall watchdog does not fire while paused', async () => {
      const stalls: number[] = []
      const hb = new TurnHeartbeat({
        silentMs: 20,
        repeatMs: 20,
        hardStallMs: 50,
        onHeartbeat: () => {},
        onHardStall: (elapsed) => stalls.push(elapsed),
      })
      hb.start()
      hb.pause()
      await delay(120)
      assert.equal(stalls.length, 0, 'watchdog must not fire while paused')
      hb.stop()
    })

    it('hard-stall watchdog re-arms after resume', async () => {
      const stalls: number[] = []
      const hb = new TurnHeartbeat({
        silentMs: 20,
        repeatMs: 20,
        hardStallMs: 50,
        onHeartbeat: () => {},
        onHardStall: (elapsed) => stalls.push(elapsed),
      })
      hb.start()
      hb.pause()
      await delay(80)
      hb.resume()
      await delay(100)
      hb.stop()
      // After resume, the watchdog should fire after hardStallMs of silence
      assert.equal(stalls.length, 1, 'watchdog must re-arm after resume')
    })

    it('double pause is a no-op', async () => {
      let count = 0
      const hb = new TurnHeartbeat({
        silentMs: 30,
        repeatMs: 30,
        onHeartbeat: () => { count++ },
      })
      hb.start()
      hb.pause()
      hb.pause() // second pause — no-op
      await delay(100)
      assert.equal(count, 0, 'double pause should not restart timer')
      hb.stop()
    })

    it('resume without pause is a no-op', async () => {
      let count = 0
      const hb = new TurnHeartbeat({
        silentMs: 30,
        repeatMs: 30,
        onHeartbeat: () => { count++ },
      })
      hb.start()
      hb.resume() // resume without pause — no-op, should not reset clock
      await delay(60)
      hb.stop()
      assert.ok(count >= 1, 'resume without pause should not reset the clock')
    })
  })

  describe('disarm / rearm watchdog (stream-phase cold TTFT)', () => {
    it('suppresses the hard stall but KEEPS informational heartbeats', async () => {
      let beats = 0
      const stalls: number[] = []
      const hb = new TurnHeartbeat({
        silentMs: 20,
        repeatMs: 20,
        hardStallMs: 50,
        onHeartbeat: () => { beats++ },
        onHardStall: (elapsed) => stalls.push(elapsed),
      })
      hb.start()
      hb.disarmWatchdog()
      await delay(140)
      hb.stop()
      assert.equal(stalls.length, 0, 'disarmed watchdog must not abort during a long busy gap')
      assert.ok(beats >= 3, `heartbeats must keep firing while disarmed (got ${beats}) — unlike pause()`)
    })

    it('rearm restores the hard stall', async () => {
      const stalls: number[] = []
      const hb = new TurnHeartbeat({
        silentMs: 20,
        repeatMs: 20,
        hardStallMs: 50,
        onHeartbeat: () => {},
        onHardStall: (elapsed) => stalls.push(elapsed),
      })
      hb.start()
      hb.disarmWatchdog()
      await delay(90)
      assert.equal(stalls.length, 0, 'still disarmed → no stall')
      hb.rearmWatchdog()
      await delay(100)
      hb.stop()
      assert.equal(stalls.length, 1, 'rearm must re-enable the hard stall')
    })

    it('phase-change tick does NOT re-arm a disarmed watchdog', async () => {
      const stalls: number[] = []
      const hb = new TurnHeartbeat({
        silentMs: 20,
        repeatMs: 20,
        hardStallMs: 50,
        onHeartbeat: () => {},
        onHardStall: (elapsed) => stalls.push(elapsed),
      })
      hb.start()
      hb.disarmWatchdog()
      // Simulate onStreamStart's onPhaseChange('working') tick mid-gap — under
      // pause() this would re-arm the timer; disarm must survive it.
      await delay(30)
      hb.tick('waiting for first token')
      await delay(110)
      hb.stop()
      assert.equal(stalls.length, 0, 'a tick must not re-arm the hard stall while disarmed')
    })
  })
})

describe('wrapCallbacksWithHeartbeat', () => {
  /** Minimal callbacks: only onTurnComplete is under test. */
  function makeCallbacks(
    onTurnComplete: (
      usage: unknown,
      turnNumber: number,
      isFinal?: boolean,
      evidenceSummary?: unknown,
      continuationReason?: string,
    ) => void,
  ): AgentCallbacks {
    return {
      onTextDelta: () => {},
      onThinkingDelta: () => {},
      onToolUse: () => {},
      onToolResult: () => {},
      onTurnComplete,
      onError: () => {},
      onAbort: () => {},
      onApprovalRequired: async () => true,
    } as unknown as AgentCallbacks
  }

  it('forwards continuationReason through onTurnComplete', () => {
    // The heartbeat wrapper is the callbacks object the orchestrator actually
    // uses after initializeRun (turn-orchestrator.ts: callbacks =
    // wrappedCallbacks). Dropping an argument here silently disables every
    // downstream consumer of the wire field (sidecar turn_complete event →
    // desktop delivery/self-verification split).
    const hb = new TurnHeartbeat({ silentMs: 10_000, repeatMs: 10_000, onHeartbeat: () => {} })
    const seen: Array<{ turnNumber: number; isFinal?: boolean; continuationReason?: string }> = []
    const wrapped = wrapCallbacksWithHeartbeat(
      makeCallbacks((_usage, turnNumber, isFinal, _evidence, continuationReason) => {
        seen.push({ turnNumber, isFinal, continuationReason })
      }),
      hb,
    )
    wrapped.onTurnComplete({}, 3, false, undefined, 'obligation-verification')
    assert.deepEqual(seen, [{ turnNumber: 3, isFinal: false, continuationReason: 'obligation-verification' }])
    // tick() inside the wrapper arms a timer while !stopped — stop it or the
    // test file never lets the event loop drain.
    hb.stop()
  })

  it('still forwards the pre-existing arguments (no regression)', () => {
    const hb = new TurnHeartbeat({ silentMs: 10_000, repeatMs: 10_000, onHeartbeat: () => {} })
    const seen: Array<{ turnNumber: number; isFinal?: boolean; evidence?: unknown }> = []
    const wrapped = wrapCallbacksWithHeartbeat(
      makeCallbacks((_usage, turnNumber, isFinal, evidenceSummary) => {
        seen.push({ turnNumber, isFinal, evidence: evidenceSummary })
      }),
      hb,
    )
    const evidence = { gate: 'GREEN' }
    wrapped.onTurnComplete({}, 7, true, evidence as never)
    assert.deepEqual(seen, [{ turnNumber: 7, isFinal: true, evidence }])
    hb.stop()
  })

  it('markIdle survives the callback itself touching activity (session-manager append order)', () => {
    // 集成顺序回归：session-manager 的 onTurnComplete 内 append turn_complete
    // 事件并对所有事件无条件 touchActivity——若 markIdle 在 cb 之前打，
    // 同一同步链里会被 touch 覆盖成 idle:false，sidecar/desktop 下
    // 「交付后等用户 >150s 误报」修复永不生效（956564a83 遗留缺口）。
    const key = 'test-idle-order'
    try {
      touchActivity(key, 'test-setup')
      const hb = new TurnHeartbeat({ silentMs: 10_000, repeatMs: 10_000, onHeartbeat: () => {} })
      const wrapped = wrapCallbacksWithHeartbeat(
        makeCallbacks(() => {
          // 模拟 session-manager append('turn_complete')：回调内同步打点
          touchActivity(key, 'evt:turn_complete')
        }),
        hb,
        () => key,
      )
      wrapped.onTurnComplete({}, 1, true)
      assert.equal(getLastActivity(key).idle, true, 'idle 标记必须在回调内 touchActivity 之后仍成立')
      hb.stop()
    } finally {
      clearActivity(key)
    }
  })
})
