// 差分 oracle 生成器：真跑 TS 的 applyCommandFilter（六族过滤器）。
//
// 用法：node_modules/.bin/tsx go/testdata/commandfilter/gen_oracle.ts > go/testdata/commandfilter/oracle.json
//
// **为什么必须真跑**：`applyCommandFilter` 是纯函数（无外部依赖），真跑给出逐字节
// 黄金数据。本会话已多次证明「手写期望值」会漏 JS 语义细节（`slice(0,负数)`、
// `??` 零值、UTF-16 量纲）。
import { applyCommandFilter } from '../../../src/tools/command-filters.js'

interface C {
  name: string
  command: string
  stdout: string
  exitCode: number
}

const cases: C[] = []

// ── 族 1：tsc --noEmit ──
const TSC_FAIL = [
  'src/a.ts(3,5): error TS2322: Type \'string\' is not assignable to type \'number\'.',
  '  some context line',
  'src/b.ts(10,1): error TS2554: Expected 2 arguments, but got 1.',
  '',
  'Found 2 errors in 2 files.',
].join('\n')
cases.push({ name: 'tsc-fail', command: 'npx tsc --noEmit', stdout: TSC_FAIL, exitCode: 2 })
cases.push({ name: 'tsc-pass-with-summary', command: 'tsc --noEmit', stdout: 'Found 0 errors.', exitCode: 0 })
cases.push({ name: 'tsc-pass-no-summary', command: 'tsc --noEmit', stdout: 'some noise\n', exitCode: 0 })
// 内容优先于 exit code：exit 0 但含 error TS
cases.push({
  name: 'tsc-exit0-with-error-signature',
  command: 'tsc --noEmit',
  stdout: TSC_FAIL,
  exitCode: 0,
})

// ── 族 2：node:test / tsx --test ──
const NODE_TEST_FAIL = [
  'TAP version 13',
  'ok 1 - passes',
  'not ok 2 - fails',
  '  AssertionError [ERR_ASSERTION]: expected 1 to equal 2',
  '      at Test.<anonymous> (/x.test.ts:5:3)',
  'ok 3 - another pass',
  '# tests 3',
  '# pass 2',
  '# fail 1',
].join('\n')
cases.push({ name: 'node-test-fail', command: 'node --test', stdout: NODE_TEST_FAIL, exitCode: 1 })
cases.push({
  name: 'node-test-pass',
  command: 'tsx --test',
  stdout: '# tests 3\n# pass 3\n# fail 0\nℹ tests 3\nℹ pass 3\n',
  exitCode: 0,
})

// ── 族 3：git status ──
cases.push({
  name: 'git-status-hints',
  command: 'git status',
  stdout: [
    'On branch main',
    'Changes not staged for commit:',
    '  modified:   a.ts',
    '',
    'no changes added to commit (use "git add" and/or "git commit -a")',
    '(use "git restore <file>..." to discard changes in working directory)',
    '(git add <file>... to include in what will be committed)',
  ].join('\n'),
  exitCode: 0,
})

// ── 族 4：git log ──
// ≤30 行 → null（不过滤）
cases.push({
  name: 'git-log-short',
  command: 'git log',
  stdout: Array.from({ length: 10 }, (_, i) => `commit abc${i}`).join('\n'),
  exitCode: 0,
})
// 默认格式 >30 行 → 压缩（保 commit/Date + ≤3 行 message）
const longLog: string[] = []
for (let i = 0; i < 20; i++) {
  longLog.push(`commit ${'a'.repeat(40)}${i}`)
  longLog.push(`Author: Someone <s@x.com>`)
  longLog.push(`Date:   Mon Sep 21 12:00:00 2026 +0800`)
  longLog.push('')
  longLog.push(`    message line 1 of ${i}`)
  longLog.push(`    message line 2 of ${i}`)
  longLog.push(`    message line 3 of ${i}`)
  longLog.push(`    message line 4 (should be dropped) of ${i}`)
  longLog.push('')
}
cases.push({ name: 'git-log-default-compress', command: 'git log', stdout: longLog.join('\n'), exitCode: 0 })
// --oneline >30 行 → 截 40 行 + omitted 标记
cases.push({
  name: 'git-log-oneline',
  command: 'git log --oneline',
  stdout: Array.from({ length: 60 }, (_, i) => `${'f'.repeat(7)}${i} feat: thing ${i}`).join('\n'),
  exitCode: 0,
})
// 自定义格式超宽行 → 截断到 120
cases.push({
  name: 'git-log-wide-line',
  command: 'git log --format="%H %s"',
  stdout: Array.from({ length: 45 }, (_, i) => `${'e'.repeat(40)}${i} ${'x'.repeat(120)}`).join('\n'),
  exitCode: 0,
})

// ── 族 5：git diff / show ──
// ≤40 行 → null
cases.push({
  name: 'git-diff-short',
  command: 'git diff',
  stdout: ['diff --git a/a.ts b/a.ts', '--- a/a.ts', '+++ b/a.ts', '@@ -1,1 +1,1 @@', '-old', '+new'].join('\n'),
  exitCode: 0,
})
// >40 行 → 压缩
const longDiff: string[] = ['diff --git a/big.ts b/big.ts', 'index abc..def 100644', '--- a/big.ts', '+++ b/big.ts']
longDiff.push('@@ -1,50 +1,50 @@')
for (let i = 0; i < 50; i++) longDiff.push(`-old line ${i}`)
for (let i = 0; i < 50; i++) longDiff.push(`+new line ${i}`)
longDiff.push('\\ No newline at end of file')
cases.push({ name: 'git-diff-compress', command: 'git diff', stdout: longDiff.join('\n'), exitCode: 0 })
// git show 的 preamble（commit 头）
cases.push({
  name: 'git-show-preamble',
  command: 'git show HEAD',
  stdout: [
    'commit abcdef1234567890',
    'Author: A <a@x.com>',
    'Date:   Mon Sep 21 2026',
    '',
    '    commit message',
    '',
    'diff --git a/x.ts b/x.ts',
    'index 111..222 100644',
    '--- a/x.ts',
    '+++ b/x.ts',
    '@@ -1,3 +1,3 @@',
    '-a',
    '+b',
    ...Array.from({ length: 45 }, (_, i) => ` context ${i}`),
  ].join('\n'),
  exitCode: 0,
})

// ── 族 6：test runners ──
// **注意行数阈值**：`filterTestRun` 开头 `if (rawLines.length <= 15) return null`
// ——fixture 必须 >15 行才会过滤（首版 13 行 → 全返回 null，是 fixture 缺陷）。
cases.push({
  name: 'test-run-fail',
  command: 'npm test',
  stdout: [
    '> pkg@1.0.0 test',
    '> vitest run',
    '',
    ' ✓ src/pass1.test.ts (2)',
    ' ✓ src/pass2.test.ts (1)',
    ' ✓ src/pass3.test.ts (1)',
    ' FAIL  src/a.test.ts > does thing',
    'AssertionError: expected 1 to be 2',
    'Expected: 2',
    'Actual: 1',
    '  at /x/a.test.ts:10:5',
    '',
    ' Test Files  1 failed | 3 passed (4)',
    '      Tests  1 failed | 4 passed (5)',
    '   Duration  1.2s',
    '  coverage  line 90%',
    '  coverage  branch 80%',
  ].join('\n'),
  exitCode: 1,
})
cases.push({
  name: 'test-run-pass',
  command: 'npx vitest',
  stdout: [
    '> pkg@1.0.0 test',
    'npm WARN something',
    ' ✓ src/a.test.ts (3)',
    ' ✓ src/b.test.ts (2)',
    ' ✓ src/c.test.ts (1)',
    ' ✓ src/d.test.ts (1)',
    ' ✓ src/e.test.ts (1)',
    ' ✓ src/f.test.ts (1)',
    ' Test Files  6 passed (6)',
    '      Tests  10 passed (10)',
    '   Duration  0.9s',
    '  coverage  line 95%',
    '  coverage  branch 88%',
    '  coverage  func 100%',
    '  coverage  stmt 95%',
    '  coverage  extra 1',
    '  coverage  extra 2',
  ].join('\n'),
  exitCode: 0,
})
// ≤15 行 → null
cases.push({ name: 'test-run-tiny', command: 'jest', stdout: 'PASS a\nTests: 1 passed', exitCode: 0 })

// ── 边界：管道命令不过滤 ──
cases.push({ name: 'pipe-not-filtered', command: 'git status | head -5', stdout: TSC_FAIL, exitCode: 0 })
cases.push({ name: 'pipe-tsc-not-filtered', command: 'tsc --noEmit | head', stdout: TSC_FAIL, exitCode: 0 })

// ── 边界：无匹配族 → null ──
cases.push({ name: 'unmatched-command', command: 'echo hi', stdout: 'hi', exitCode: 0 })
cases.push({ name: 'unmatched-fail', command: 'ls nonexistent', stdout: 'ls: cannot access', exitCode: 2 })

const out = cases.map(c => ({
  name: c.name,
  input: { command: c.command, stdout: c.stdout, exitCode: c.exitCode },
  result: applyCommandFilter(c.command, c.stdout, c.exitCode),
}))

console.log(JSON.stringify(out, null, 1))
