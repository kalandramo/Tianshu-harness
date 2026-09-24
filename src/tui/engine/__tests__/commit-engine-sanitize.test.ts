/**
 * entry.text 契约兜底回归（2026-09-17 审计 UI 族）：不可信文本里的 OSC 与
 * 非 SGR 的 CSI 序列必须在唯一汇聚点剥除——OSC 52 可覆写系统剪贴板，CSI 可
 * 清屏/踢出 alt-screen。SGR（纯样式）放行：生产侧经 text 通道传输自产样式
 * （formatMarkdown / color() 高亮），剥 SGR 等于 scrollback 全褪色（#181 回归）。
 * 自产序列走 entry.ansi，不经过兜底。
 */
import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { CommitEngine } from '../commit-engine.js'

function mockEngine() {
  const writes: string[] = []
  const stdout = { write: (chunk: string) => { writes.push(chunk); return true } } as unknown as import('node:tty').WriteStream
  const engine = new CommitEngine({ stdout })
  return { engine, writes }
}

describe('CommitEngine enforces the no-ANSI contract on entry.text', () => {
  it('strips OSC 52 clipboard overwrite, CSI and title sequences from text', () => {
    const { engine, writes } = mockEngine()
    engine.write({ text: 'a\x1B]52;c;cGduZWQ=\x07b\x1B[2J\x1B[H\x1B]0;evil\x07c' })
    const out = writes.join('')
    assert.ok(!out.includes('\x1B'), 'no ESC byte may reach stdout via entry.text')
    assert.ok(out.includes('abc'), 'plain text content must survive')
  })

  it('keeps newlines and surrounding text intact', () => {
    const { engine, writes } = mockEngine()
    engine.write({ text: 'line1\x1B[?1049l\nline2' })
    assert.ok(writes.join('').includes('line1\nline2'))
  })

  it('does not touch entry.ansi (engine-built sequences stay intact)', () => {
    const { engine, writes } = mockEngine()
    engine.write({ text: 'ignored', ansi: '\x1B[31mred\x1B[0m' })
    assert.ok(writes.join('').includes('\x1B[31m'))
  })

  // 回归锚（#181 移植回归）：text 通道的生产侧长期混入自产样式（StreamRenderer
  // 的 formatMarkdown、commitStatic 的 color() 高亮）——SGR 必须存活，否则
  // scrollback 里 markdown/告警全部褪色。SGR 无机动能力（清屏/剪贴板/光标），
  // 不在原威胁模型内。
  it('keeps SGR styling in entry.text (only机动序列被剥)', () => {
    const { engine, writes } = mockEngine()
    engine.write({ text: '\x1B[1m重要\x1B[22m \x1B[31m红色\x1B[39m 普通' })
    const out = writes.join('')
    assert.ok(out.includes('\x1B[1m重要\x1B[22m'), 'SGR 粗体必须存活')
    assert.ok(out.includes('\x1B[31m红色\x1B[39m'), 'SGR 颜色必须存活')
  })

  it('strips non-SGR CSI while keeping SGR in the same string', () => {
    const { engine, writes } = mockEngine()
    engine.write({ text: '\x1B[31m红\x1B[2J\x1B[H色\x1B[39m' })
    const out = writes.join('')
    assert.ok(!out.includes('\x1B[2J'), '清屏序列必须剥除')
    assert.ok(!out.includes('\x1B[H'), '光标移动必须剥除')
    assert.ok(out.includes('\x1B[31m'), '同行的 SGR 必须存活')
    assert.ok(out.includes('红色'), '文本内容存活')
  })
})
