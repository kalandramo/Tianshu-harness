import { test } from 'node:test'
import assert from 'node:assert/strict'
import { extractErrorHead, generateToolSummary } from '../tool-summary.js'

// 这两个函数 2026-09-23 从 tool-pipeline.ts 沿接缝拆出（点名巨石只降不升）。
// 搬家最容易出的错是「顺手改了一行」——本组用例按行为锁住拆分前后一致的输出，
// 覆盖每个分支：拆分的价值在于文件变小，不在于行为变化。

test('extractErrorHead: 优先挑错误/失败行，最多 8 行、逐行 trim 且截断到 120 字符', () => {
  const content = [
    'line one',
    'Error: boom',
    '  AssertionError: expected 1 to equal 2  ',
    'plain',
  ].join('\n')
  assert.equal(
    extractErrorHead(content),
    ['Error: boom', 'AssertionError: expected 1 to equal 2'].join('\n'),
  )
})

test('extractErrorHead: 无错误行时回落到末 8 行（摘要通常在尾部）', () => {
  const lines = Array.from({ length: 12 }, (_, i) => `l${i}`)
  assert.equal(extractErrorHead(lines.join('\n')), lines.slice(-8).join('\n'))
})

test('extractErrorHead: 单词边界——errorHandler 这类标识符不算错误行', () => {
  const content = ['const errorHandler = 1', 'all good'].join('\n')
  assert.equal(extractErrorHead(content), 'const errorHandler = 1\nall good')
})

test('generateToolSummary: run_tests 带上汇总行与错误行', () => {
  const content = ['running', 'Tests: 11 passed, 1 failed', 'Error: nope'].join('\n')
  const s = generateToolSummary(content, 'run_tests', {})
  assert.match(s, /^\[run_tests\] 3 lines\. Tests: 11 passed, 1 failed/)
  // error 行的挑选是 /error|Error|FAIL/i——「1 failed」也命中，所以它同时出现在汇总位与错误位。
  assert.match(s, /Errors: Tests: 11 passed, 1 failed; Error: nope$/)
})

test('generateToolSummary: run_tests 的既有口径原样保留——汇总行挑不出来时只剩错误行', () => {
  // 本仓自己的 run_tests 输出是「ℹ pass 11」这种形状，而挑选正则要的是
  // 「test(s) + pass/fail」或「total」——两者对不上，于是汇总行缺席、只剩错误行。
  // 这是拆分前的既有口径：模块搬家只搬不改，此用例把现状钉住；要改挑选正则是
  // 另一件事（会改变所有历史摘要的读法），不夹带在拆分里做。
  const s = generateToolSummary(['ℹ tests 12', 'ℹ pass 11', 'ℹ fail 1'].join('\n'), 'run_tests', {})
  assert.equal(s, '[run_tests] 3 lines. Errors: ℹ fail 1')
})

test('generateToolSummary: diff 数改动文件，超过 5 个给 (+N)', () => {
  const files = ['a.ts', 'b.ts', 'c.ts', 'd.ts', 'e.ts', 'f.ts']
  const content = files.map((f) => `diff --git a/${f} b/${f}`).join('\n')
  const s = generateToolSummary(content, 'diff', {})
  assert.match(s, /^\[diff\] 6 files changed, 6 lines\./)
  assert.match(s, /a\.ts, b\.ts, c\.ts, d\.ts, e\.ts \(\+1\)$/)
})

test('generateToolSummary: glob 带 pattern 与命中数', () => {
  const s = generateToolSummary('a.ts\nb.ts\nc.ts\nd.ts', 'glob', { pattern: 'src/**/*.ts' })
  assert.equal(s, '[glob "src/**/*.ts"] 4 files found. First: a.ts, b.ts, c.ts (+1)')
})

test('generateToolSummary: bash 识别 typecheck / test / 普通命令三条分支', () => {
  const tsc = generateToolSummary('a.ts(1,1): error TS1005: x\nerror TS2304: y', 'bash', { command: 'npx tsc --noEmit' })
  assert.match(tsc, /^\[bash typecheck\] 2 errors, 2 lines\. cmd: npx tsc --noEmit$/)
  const jest = generateToolSummary('PASS src/a.test.ts\nTests: 3 passed', 'bash', { command: 'npx jest' })
  assert.match(jest, /^\[bash test\] 2 lines\. /)
  assert.match(jest, /cmd: npx jest$/)
  const plain = generateToolSummary('hello', 'bash', { command: 'echo hello' })
  assert.equal(plain, '[bash] 5 chars, 1 lines. cmd: echo hello')
})

test('generateToolSummary: repo_map / inspect_project / 未知工具各有兜底', () => {
  assert.match(generateToolSummary('12 files indexed', 'repo_map', {}), /^\[repo_map\] 1 lines\. 12 files indexed$/)
  assert.equal(generateToolSummary('x', 'inspect_project', {}), '[inspect_project] 1 lines of project analysis.')
  assert.equal(
    generateToolSummary('short\nthis line is long enough', 'mystery_tool', {}),
    '[mystery_tool] 30 chars, 2 lines. this line is long enough',
  )
})

test('generateToolSummary: 缺字段的 input 不炸（pattern / url / command 走 ? 兜底）', () => {
  assert.match(generateToolSummary('x', 'glob', {}), /\[glob "\?"\]/)
  assert.match(generateToolSummary('x', 'web_fetch', {}), /\[web_fetch \?\]/)
  assert.match(generateToolSummary('x', 'bash', {}), /cmd: \?$/)
})
