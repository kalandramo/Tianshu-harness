/**
 * ctrl+t 回看的触发条件契约：只在**真空闲**时重印。
 *
 * 背景（审查发现）：thinking 段结束后 isThinking 已为 false，但工具可能仍在跑
 * （agentBusy / phase !== 'idle'）——此时重印会把最多 400 逻辑行一次性写进
 * scrollback，而 take() 已把回看仓清空，误触不可撤回。同文件的 ctrl_r 分支用
 * isAgentActive() 守卫，本用例把同一不变量钉在 ctrl_t 上。
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import type { ReadStream, WriteStream } from 'node:tty'
import { TuiApp } from '../app.js'

class MockOut {
  columns = 80
  rows = 24
  chunks: string[] = []
  write = (s: string): boolean => { this.chunks.push(s); return true }
  on(): this { return this }
  removeListener(): this { return this }
  clear() { this.chunks = [] }
}
class MockIn {
  isTTY = true
  dataHandler: ((d: string) => void) | null = null
  setRawMode(): this { return this }
  resume(): this { return this }
  setEncoding(): this { return this }
  on(ev: string, h: (d: string) => void): this { if (ev === 'data') this.dataHandler = h; return this }
  removeAllListeners(): this { return this }
  pause(): this { return this }
}

function makeApp() {
  const out = new MockOut()
  const stdin = new MockIn()
  const app = new TuiApp({
    stdout: out as unknown as WriteStream,
    stdin: stdin as unknown as ReadStream,
    cols: 80, rows: 24, modelName: 'test',
  })
  return { app, out, stdin }
}

const tick = () => new Promise(r => setTimeout(r, 10))
/** 终端把 Ctrl+T 送达为单控制字节 DC4(0x14)，走真实解析器。 */
const CTRL_T = '\x14'

interface Internals {
  thinkingReview: { save(r: { text: string; elapsedMs: number }): void; peek(): unknown }
  agentBusy: boolean
}

test('agent 忙时 ctrl+t 不重印回看（工具跑动中误触不刷屏）', async () => {
  const { app, stdin } = makeApp()
  const internals = app as unknown as Internals
  internals.thinkingReview.save({ text: '一段思考正文', elapsedMs: 3_000 })
  internals.agentBusy = true

  stdin.dataHandler!(CTRL_T)
  await tick()

  assert.notEqual(internals.thinkingReview.peek(), null, '忙时不得取走回看仓（重印不可撤回）')
})

test('空闲时 ctrl+t 重印并清空回看仓（take 语义不变）', async () => {
  const { app, stdin } = makeApp()
  const internals = app as unknown as Internals
  internals.thinkingReview.save({ text: '一段思考正文', elapsedMs: 3_000 })
  internals.agentBusy = false

  stdin.dataHandler!(CTRL_T)
  await tick()

  assert.equal(internals.thinkingReview.peek(), null, '空闲时取走并重印进 scrollback')
})
