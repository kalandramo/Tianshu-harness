/**
 * plan 工具的差分 oracle —— 真跑 TS 原实现，产出黄金 JSON。
 *
 * 用法：cd go && node_modules/../.bin/tsx testdata/plan/gen-oracle.ts > testdata/plan/oracle.json
 *
 * **为什么必须真跑**：手抄 TS 行为会在边界处引入「我以为」的偏差。本仓库
 * 已有先例——Wave 1 假绿事故（手抄 prompt 数据导致「逐字节一致」是假的）。
 */
import { slugify, stripPlanStatusMarkers, insertPlanStatusMarker, insertPlanModelMarker, parsePlanModel, isDraftSlug } from '../../../src/plan/plan-store.js'
import { parseTaskSelection, closePlanMarkdown } from '../../../src/plan/plan-close.js'
import { inferModelTierFromName } from '../../../src/agent/model-tier-policy.js'
import { detectPointerPlaceholder } from '../../../src/tools/pointer-guard.js'

const out: Record<string, unknown> = {}

// ── slugify ──
const slugInputs = [
  'Fix memory leak in loop.ts',
  '修复 内存泄露',
  '  hello world!  ',
  '!!!',
  '',
  '   ',
  '---',
  'a'.repeat(100),
  'Café ÉTÉ',
  '🎉🎊 party time',
  '🎉'.repeat(50),
  '中文字符测试' + 'x'.repeat(90),
  'MiXeD CaSe Title',
  'tabs\tand\nnewlines',
  'dots.and-dashes_and_underscores',
  '中文English混合123',
  'A'.repeat(79) + '🎉',
  'A'.repeat(80) + '🎉',
  'A'.repeat(78) + '🎉🎉',
  '\u00a0nbsp\u00a0',
  '𠮷野家',  // 补充平面 CJK（U+20BB7，代理对）
]
out.slugify = slugInputs.map(input => ({ input, output: slugify(input) }))

// ── stripPlanStatusMarkers ──
const stripInputs = [
  '> **Status: APPROVED** — 2026-01-01T00:00:00.000Z\n\n# Plan\n\nbody',
  '> **Status: REJECTED** — 2026-01-01T00:00:00.000Z\n\n# Plan\n\nbody',
  '> **Status: EXECUTED** — 2026-01-01T00:00:00.000Z\n\n# Plan\n\nbody',
  '> **Status: approved** — lower\n\n# Plan',  // 小写不应匹配
  '# Plan\n\n> **Status: APPROVED** — 2026-01-01\n\nbody',  // 不在开头
  '> **Note:** keep me\n\n# Plan',
  '# Plan\n\nbody',
  '> **Status: APPROVED** — x\n\n> **Status: REJECTED** — y\n\n# Plan',
  '>  **Status:  APPROVED** — extra spaces\n\n# Plan',
  '> **Status: APPROVED** — x\r\n\r\n# Plan',
]
out.stripStatus = stripInputs.map(input => ({ input, output: stripPlanStatusMarkers(input) }))

// ── insertPlanStatusMarker（时间戳固定，便于确定性对账）──
const ts = '2026-01-01T00:00:00.000Z'
const insertStatusInputs = [
  '# Plan\n\nbody',
  '# Plan',
  'no h1 here\n\nbody',
  '',
  '# Plan\n\n## Section\n\nbody',
  '#Plan no space',
  '#  Plan with extra space\n\nbody',
]
out.insertStatus = insertStatusInputs.map(input => ({
  input, status: 'EXECUTED', output: insertPlanStatusMarker(input, 'EXECUTED'),
}))
out.insertStatusApproved = insertStatusInputs.map(input => ({
  input, output: insertPlanStatusMarker(input, 'APPROVED'),
}))

// ── insertPlanModelMarker ──
const insertModelInputs = [
  '# Plan\n\nbody',
  'no h1',
  '# Plan\n\nbody',
  '> **Model: old (cheap)**\n\n# Plan\n\nbody',  // 幂等：剥旧标记
]
out.insertModel = insertModelInputs.map(input => ({
  input, output: insertPlanModelMarker(input, 'deepseek-v4.1-flash', 'cheap'),
}))
out.insertModelNoTier = insertModelInputs.map(input => ({
  input, output: insertPlanModelMarker(input, 'unknown-model', null),
}))

// ── parsePlanModel ──
const parseModelInputs = [
  '> **Model: deepseek-v4.1-flash (cheap)**\n\n# Plan',
  '> **Model: some-model**\n\n# Plan',
  '# Plan\n\nbody',
  '> **Model: m (strong)**\n\n# Plan',
]
out.parseModel = parseModelInputs.map(input => ({ input, output: parsePlanModel(input) ?? null }))

// ── isDraftSlug ──
out.isDraft = ['draft-1751600000000', 'draft-1', 'draft-', 'draft-abc', 'my-plan', 'draft-1-2', '']
  .map(input => ({ input, output: isDraftSlug(input) }))

// ── parseTaskSelection ──
const selectionInputs = ['1', '1-3', '1,3-4', 'all', 'ALL', ' All ', '1,1,2', '3,1,2', '1-1', '', '  ', '0', '-1', '1-', '-3', '1-0', '3-1', 'a', '1,a', '1,,2', '1.5', '01', '1, 2 , 3', '1-3,5,7-9']
out.parseSelection = selectionInputs.map(input => {
  try {
    return { input, output: parseTaskSelection(input) }
  } catch (e) {
    return { input, error: (e as Error).message }
  }
})

// ── closePlanMarkdown ──
const planA = [
  '# Plan',
  '',
  '## Tasks',
  '',
  '### Task 1',
  '- [ ] step one',
  '- [x] step two',
  '',
  '### Task 2',
  '- [ ] alpha',
  '- [ ] beta',
  '',
  '### Task 3',
  '- [ ] gamma',
  '',
].join('\n')

const planWave = [
  '# Plan',
  '',
  '### Wave 1',
  '- [ ] w1a',
  '',
  '### Wave 2',
  '- [ ] w2a',
  '',
].join('\n')

const planCN = '# Plan\n\n### 任务 1\n- [ ] 一\n\n### 任务 2\n- [ ] 二\n'

const planFence = [
  '# Plan',
  '',
  '### Task 1',
  '- [ ] real',
  '',
  '```md',
  '### Task 99',
  '- [ ] fake inside fence',
  '```',
  '',
  '### Task 2',
  '- [ ] real2',
  '',
].join('\n')

const planStatus = '# Plan\n\n**技术栈：** Go\n\n### Task 1\n- [ ] x\n'
const planNoTrailing = '# Plan\n\n### Task 1\n- [ ] x'

// checkbox 三种形态：`[ ]` 未选 / `[x]` 小写已选 / `[X]` 大写已选。
// **大写的存在是为锁定 checkboxAnyRe 的 `[ xX]` 字符类**——只写小写时，
// 把 `X` 从字符类里删掉的变异不可观测（计数相同）。
const planCheckboxVariants = [
  '# Plan',
  '',
  '### Task 1',
  '- [ ] unchecked',
  '- [x] checked-lower',
  '- [X] checked-upper',
  '- [  ] two-spaces',
  '* [ ] star-bullet',
  '- [ ]no-space-after',
  '  - [ ] indented',
  '',
].join('\n')

const closeCases: Array<[string, string, any]> = [
  ['planA-all', planA, { tasks: 'all' }],
  ['planA-1', planA, { tasks: '1' }],
  ['planA-1-2', planA, { tasks: '1-2' }],
  ['planA-2,3', planA, { tasks: '2,3' }],
  ['planWave-all', planWave, { tasks: 'all' }],
  ['planCN-all', planCN, { tasks: 'all' }],
  ['planFence-all', planFence, { tasks: 'all' }],
  ['planA-with-verified', planA, { tasks: 'all', verifiedCommands: ['go test ./...', 'gofmt -l .'] }],
  ['planA-with-state', planA, { tasks: 'all', deliveryState: 'GREEN' }],
  ['planA-with-note', planA, { tasks: 'all', note: '  trimmed note  ' }],
  ['planA-no-closure', planA, { tasks: 'all', updateClosure: false }],
  ['planStatus-techstack', planStatus, { tasks: 'all' }],
  ['planCheckboxVariants', planCheckboxVariants, { tasks: 'all' }],
  ['planNoTrailing', planNoTrailing, { tasks: 'all' }],
  ['planA-idempotent', closePlanMarkdown(planA, { tasks: 'all' }).content, { tasks: 'all' }],
  ['planA-missing-task', planA, { tasks: '99' }],
]
out.close = closeCases.map(([name, md, opts]) => {
  try {
    return { name, input: md, options: opts, result: closePlanMarkdown(md, opts) }
  } catch (e) {
    return { name, input: md, options: opts, error: (e as Error).message }
  }
})

// ── inferModelTierFromName ──
out.tier = [
  'gemini-2.5-flash', 'claude-haiku', 'minimax-m2', 'gemini-2.5-pro',
  'claude-opus-4', 'gpt-5.5', 'deepseek-chat', 'unknown-model',
  'deepseek-v4.1-flash', 'FLASH', 'Pro', 'm2-ultra', 'gpt-5-flash',
  'small-pro', 'haiku-pro',
].map(input => ({ input, output: inferModelTierFromName(input) }))

// ── detectPointerPlaceholder ──
const ptrTag = '#RIVET-POINTER-DISPLAY-ONLY#'
const ptrInputs = [
  `[file written to src/a.ts — 10 lines, 100 chars] ${ptrTag} Display placeholder, never emit as content. Use read_file to review.`,
  `[edit on src/a.ts: replaced 3 lines] ${ptrTag}`,
  `[hash_edit applied to src/a.ts — 5 lines] ${ptrTag}`,
  `[patch applied to 2 files] ${ptrTag}`,
  `[new block — 4 lines] ${ptrTag}`,
  `[plan persisted to .rivet/plans/x.md — submitted] ${ptrTag}`,
  'real content mentioning [file written to somewhere] mid-text',
  '[file written to src/a.ts] no marker phrase',
  `prefix text\n[plan persisted to .rivet/plans/x.md] ${ptrTag}\nmore`,
  `  [file written to src/a.ts — x] ${ptrTag}`,
  '',
  'plain content',
  `[file written to x] Display placeholder\nreal second line`,
  `[unknown prefix] ${ptrTag}`,
]
out.pointer = ptrInputs.map(input => ({ input, output: detectPointerPlaceholder(input) }))

process.stdout.write(JSON.stringify(out, null, 2))
