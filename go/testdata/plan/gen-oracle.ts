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

// ── extractPlanAnchors（**lookbehind 替代的边界用例**）──
import { extractPlanAnchors } from '../../../src/plan/plan-fact-anchors.js'

const anchorInputs = [
  // 基础形态
  '见 `src/agent/loop.ts:643` 的实现。',
  '见 src/agent/loop.ts:643 的实现。',
  '引用 src/agent/loop.ts 但不带行号',
  '范围 src/plan/plan-close.ts:12-34 两端',
  // **URL 内嵌**（lookbehind 的核心用途）
  '见 https://github.com/foo/bar/blob/main/src/a.ts 这个链接',
  '见 http://x.com/a/b/c.ts:5 链接',
  // **粘连 token**
  '路径 a/b.ts 前面是字母x/b.ts',
  '路径 a/b.ts 前面是斜杠/x/b.ts',
  '路径 a/b.ts 前面是点./b.ts',
  '路径 a/b.ts 前面是反斜杠\\x/b.ts',
  '路径 a/b.ts 前面是横杠-x/b.ts',
  // **枚举粘连**（README.md/README.zh.md）
  '同步 `README.md/README.zh.md/README.i18n.yaml` 三处描述。',
  // 扩展名最长优先
  '见 src/ui/selector.tsx 组件',
  '见 src/ui/selector.ts 模块',
  // 模块相对 / 越界
  '见 ./local/file.ts 相对路径',
  '见 ../parent/file.ts 越界路径',
  '见 node_modules/pkg/index.js 依赖',
  // 围栏
  '```mermaid\nflowchart TD\n  A[src/a.ts] --> B\n```',
  '```bash\ngo test ./internal/plan/\n```',
  '```\nplain fence src/x.ts\n```',
  '```md\n### Task 1\n- [ ] see src/y.ts\n```',
  // 新增标记
  '新增 src/new/module.ts 文件',
  '新建 src/fresh/api.ts',
  'create src/created/file.ts',
  '普通引用 src/plain/file.ts',
  // 占位形态
  '见 src/foo.ts 示例',
  '见 src/a.py 单字母',
  // 多锚点同行
  '同时改 src/a/b.ts 与 src/c/d.ts:9',
  // 无目录段的裸文件名（应不匹配）
  '见 loop.ts 裸文件',
  '见 file.go 裸文件',
  // **后边界 `(?!\w)`**：扩展名后紧跟 \w 字符 → 不是完整 token，应排除。
  // 长度降序**挡不住这个**（a.tsx5 不是已知扩展名），只有后边界能挡。
  '见 src/a.tsx5 后缀',
  '见 src/a.ts_foo 后缀',
  '见 src/a.go1 后缀',
  '见 src/b.json2 后缀',
].join('\n')

out.extractAnchors = anchorInputs.split('\n').flatMap(line => {
  const got = extractPlanAnchors(line)
  return [{ line, anchors: got.map(a => ({ raw: a.raw, path: a.path, line: a.line ?? null, declaredNew: a.declaredNew, placeholderShaped: a.placeholderShaped })) }]
})

// ── 多行输入的锚点提取（围栏状态跨行）──
const multiLineAnchorDoc = [
  '# Plan',
  '',
  '见 src/agent/loop.ts:643。',
  '',
  '```mermaid',
  'flowchart TD',
  '  A[src/should/skip.ts] --> B',
  '```',
  '',
  '```bash',
  'go test ./internal/plan/  # 见 src/checked/in-bash.ts',
  '```',
  '',
  '新增 src/newly/created.ts 文件',
  '再引用 src/newly/created.ts 一次（应豁免）',
  '',
].join('\n')
out.extractAnchorsMulti = (() => {
  const got = extractPlanAnchors(multiLineAnchorDoc)
  return { input: multiLineAnchorDoc, anchors: got.map(a => ({ raw: a.raw, path: a.path, line: a.line ?? null, declaredNew: a.declaredNew, placeholderShaped: a.placeholderShaped })) }
})()

// ── formatAnchorDrifts ──
import { formatAnchorDrifts } from '../../../src/plan/plan-fact-anchors.js'
out.formatDrifts = [
  [],
  [{ anchor: 'src/a.ts:1', path: 'src/a.ts', line: 1, kind: 'missing-file' as const, detail: '详情一' }],
  [
    { anchor: 'a', path: 'a', kind: 'missing-file' as const, detail: 'D1' },
    { anchor: 'b', path: 'b', kind: 'root-mismatch' as const, detail: 'D2' },
  ],
].map(drifts => ({ input: drifts.length, output: formatAnchorDrifts(drifts) }))

// ── isPlaceholderShaped 与 hasFileShapedIntermediateSegment（经 extractAnchors 间接覆盖）──

// ── parsePlanStatus（TS 侧未导出，内联复刻其逻辑产黄金数据）──
// **为什么内联**：`parsePlanStatus` 在 plan-store.ts:422 是**未导出**的
// （scout 报告称其为内部函数，已核实）。复刻其 5 行逻辑产期望值——
// 关键锁定「优先级固定」而非「取第一个匹配」。
const parseStatus = (s: string): string =>
  /Status:\s*EXECUTED/i.test(s) ? 'executed'
  : /Status:\s*APPROVED/i.test(s) ? 'approved'
  : /Status:\s*REJECTED/i.test(s) ? 'rejected'
  : 'submitted'

out.parseStatus = [
  '# Plan\n\nbody\n',
  '> **Status: APPROVED** — x\n\n# P\n',
  '> **Status: REJECTED** — x\n\n# P\n',
  '> **Status: EXECUTED** — x\n\n# P\n',
  // **叠加**：优先级测试——这是首版实现搞错的地方。
  '> **Status: REJECTED** — x\n\n> **Status: APPROVED** — y\n\n# P\n',
  '> **Status: APPROVED** — x\n\n> **Status: REJECTED** — y\n\n# P\n',
  '> **Status: APPROVED** — x\n\n> **Status: EXECUTED** — y\n\n# P\n',
  '> **status: approved** — 小写\n\n# P\n',
].map(input => ({ input, output: parseStatus(input) }))

process.stdout.write(JSON.stringify(out, null, 2))
