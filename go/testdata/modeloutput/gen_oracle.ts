// 差分 oracle 生成器：真跑 TS 的 buildModelOutput / extractErrorAwareLines。
//
// 用法：node_modules/.bin/tsx testdata/modeloutput/gen_oracle.ts > testdata/modeloutput/oracle.json
//
// **为什么必须真跑**：这两个函数是纯函数（无外部依赖），真跑能给出逐字节黄金数据，
// 远比人工转录可靠。本会话已多次证明「手写期望值」会漏掉 JS 语义细节。
import { buildModelOutput, extractErrorAwareLines } from '../../../src/tools/output-store.js'

interface Case {
  name: string
  raw: string
  exitCode: number
  durationMs: number
  command: string
  rawPath?: string
  maxLines?: number // extractErrorAwareLines 用
}

const cases: Case[] = [
  // ── 空输出 ──
  { name: 'empty-success', raw: '', exitCode: 0, durationMs: 100, command: 'touch x' },
  { name: 'empty-failure', raw: '', exitCode: 1, durationMs: 50, command: 'false' },

  // ── 成功折叠边界 ──
  { name: 'success-20-lines', raw: Array.from({ length: 20 }, (_, i) => `L${i + 1}`).join('\n'), exitCode: 0, durationMs: 10, command: 'seq 1 20' },
  { name: 'success-21-lines', raw: Array.from({ length: 21 }, (_, i) => `L${i + 1}`).join('\n'), exitCode: 0, durationMs: 10, command: 'seq 1 21' },
  { name: 'success-100-lines', raw: Array.from({ length: 100 }, (_, i) => `L${i + 1}`).join('\n'), exitCode: 0, durationMs: 10, command: 'seq 1 100' },

  // ── 失败 error-aware 边界 ──
  { name: 'fail-40-lines-no-marker', raw: Array.from({ length: 40 }, (_, i) => `L${i + 1}`).join('\n'), exitCode: 1, durationMs: 10, command: 'x' },
  { name: 'fail-41-lines-with-marker', raw: Array.from({ length: 41 }, (_, i) => (i === 20 ? 'Error: boom' : `L${i + 1}`)).join('\n'), exitCode: 1, durationMs: 10, command: 'x' },

  // ── 超 200 行 head+tail ──
  { name: 'success-250-lines', raw: Array.from({ length: 250 }, (_, i) => `L${i + 1}`).join('\n'), exitCode: 0, durationMs: 10, command: 'seq 1 250' },
  { name: 'fail-250-lines', raw: Array.from({ length: 250 }, (_, i) => (i === 100 ? 'FAIL at foo.ts:12' : `L${i + 1}`)).join('\n'), exitCode: 1, durationMs: 10, command: 'x' },

  // ── rawPath 恢复提示 ──
  { name: 'with-rawpath', raw: Array.from({ length: 30 }, (_, i) => `L${i + 1}`).join('\n'), exitCode: 0, durationMs: 10, command: 'seq 1 30', rawPath: '/tmp/x.raw' },

  // ── 末尾空行（countLines 语义）──
  { name: 'trailing-newline', raw: 'a\nb\n', exitCode: 0, durationMs: 10, command: 'x' },
  { name: 'only-newlines', raw: '\n\n', exitCode: 0, durationMs: 10, command: 'x' },

  // ── 耗时格式（一位小数）──
  { name: 'duration-1234ms', raw: 'ok', exitCode: 0, durationMs: 1234, command: 'x' },
]

const out = cases.map(c => ({
  name: c.name,
  input: {
    raw: c.raw,
    exitCode: c.exitCode,
    durationMs: c.durationMs,
    command: c.command,
    rawPath: c.rawPath ?? null,
  },
  modelOutput: buildModelOutput(c.raw, {
    command: c.command,
    exitCode: c.exitCode,
    durationMs: c.durationMs,
    rawPath: c.rawPath,
  }),
}))

// extractErrorAwareLines 的独立用例（含 marker 命中与回退）。
const awareCases = [
  { name: 'no-marker', lines: Array.from({ length: 50 }, (_, i) => `L${i + 1}`), maxLines: 20 },
  { name: 'one-marker', lines: Array.from({ length: 50 }, (_, i) => (i === 25 ? 'Error: x' : `L${i + 1}`)), maxLines: 20 },
  { name: 'multi-marker', lines: Array.from({ length: 80 }, (_, i) => ([10, 30, 60].includes(i) ? `FAIL ${i}` : `L${i + 1}`)), maxLines: 30 },
  { name: 'marker-at-edges', lines: ['Error: first', ...Array.from({ length: 40 }, (_, i) => `L${i}`), 'Error: last'], maxLines: 15 },
  { name: 'ts-error-marker', lines: ['src/a.ts(3,5): error TS2322: bad', ...Array.from({ length: 30 }, (_, i) => `L${i}`)], maxLines: 20 },
  { name: 'too-many-markers', lines: Array.from({ length: 200 }, (_, i) => (i % 2 === 0 ? `error ${i}` : `L${i}`)), maxLines: 20 },
]

const awareOut = awareCases.map(c => ({
  name: c.name,
  input: { lines: c.lines, maxLines: c.maxLines },
  result: extractErrorAwareLines(c.lines, c.maxLines),
}))

console.log(JSON.stringify({ modelOutput: out, errorAware: awareOut }, null, 1))
