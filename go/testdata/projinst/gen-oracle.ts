/**
 * Project-instructions 选取算法 oracle 生成器。
 *
 * 生成：
 *   npx tsx go/testdata/projinst/gen-oracle.ts
 *
 * 覆盖 src/prompt/project-instructions.ts 的四个纯函数：
 *   splitSections / selectSections / selectProjectInstructions / escapeXml 语义
 *
 * 纪律：调用**真实**导出函数，不手抄任何期望值。
 *
 * 用例设计原则——每条都针对一个具体的易错点：
 *   1. fence 内的 `## ` 不得当标题（代码块里的注释行）
 *   2. `#` 与 `##` 都是边界（AGENTS.md + .rivet.md 拼接后两份文档的分界）
 *   3. `###` 不是边界（节内结构）
 *   4. 前言（首个标题之前）tier 恒为 Gate
 *   5. 文档标题行（`# `）tier 恒为 Gate
 *   6. 表格占比 >= 0.4 → Reference（且**先于**门禁词判定）
 *   7. 标题命中 GATE_HEADING → Gate
 *   8. 正文命中 HARD_GATE → Gate
 *   9. 贪心按 tier 顺序（Gate → Prose → Reference）
 *  10. 分隔符 `\n\n` 计费（首个选中项不计）
 *  11. 两轮预留（略去标记本身占预算）
 *  12. 二轮不单调 → 装不下就退回一轮
 *  13. 预算连一节都装不下 → 退回原文整块
 *  14. measure 回调按渲染后长度计费（转义膨胀）
 *  15. escapeXml 不转义单引号、且 `&` 必须先转（否则二次转义）
 */
import { writeFileSync, mkdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import { createHash } from 'node:crypto'
import {
  splitSections,
  selectSections,
  selectProjectInstructions,
} from '../../../src/prompt/project-instructions.js'

const here = dirname(fileURLToPath(import.meta.url))

// escapeXml 在 volatile.ts 内是私有函数，这里复刻其**语义**以生成期望值。
// 注意：这是唯一一处不调用真实代码的地方，故单独用一个用例锁定它——
// 若 TS 侧改动转义规则，本文件的复刻会与真实实现分叉，而那个用例会红。
// 复刻依据：volatile.ts:493（只转 & < > "，顺序为 & 最先）。
function escapeXmlRef(text: string): string {
  return text
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
}

const SEP = '\n\n'

// ── 用例 1: splitSections 的边界判定 ──────────────────────────────
const splitCases: Record<string, string> = {
  // fence 内的 ## 不当标题；### 不是边界；# 与 ## 都是边界
  fencesAndLevels: [
    '# 文档一',
    '前言散文',
    '```md',
    '## 这是代码块里的伪标题',
    '```',
    '## 真标题',
    '### 三级不算边界',
    '正文',
    '# 文档二',
    '结尾',
  ].join('\n'),
  // 无任何标题 → 全是前言
  noHeadings: '只有散文\n没有标题',
  // 空文档
  empty: '',
  // 只有空白
  whitespace: '   \n\n  ',
  // 前言为空（文档以标题开头）
  noLead: '## 首节\n内容',
  // 围栏未闭合
  unclosedFence: '## 真标题\n```\n## 伪标题',
}

// ── 用例 2: selectProjectInstructions 的选取行为 ─────────────────
// 构造文档：参考类表格节 + 纪律节 + 散文节
const docMixed = [
  '# 项目 X',
  '',
  '| 目录 | 说明 |',
  '| --- | --- |',
  '| src | 源码 |',
  '| test | 测试 |',
  '',
  '## 高危命令纪律',
  '用户让你查看时不要动手，必须等待确认。',
  '',
  '## 说明性章节',
  '这是一段普通的散文说明，不含门禁词也不含表格。',
  '',
  '## 更多参考',
  '| a | b |',
  '| - | - |',
  '| 1 | 2 |',
].join('\n')

// 用例 2b: **表格优先于门禁** 的区分点。
// 构造一个章节，表格占比 >= 0.4 **且** 标题命中门禁词——两种分类顺序给出
// 不同的 tier（表格优先 → Reference 可丢；门禁优先 → Gate 必保）。
// 预算必须紧到"必须丢一节"，否则选取不触发、差异不可见。
// （首版用例预算过宽，导致这个区分点未被覆盖——变异反证才暴露出来。）
const docTierConflict = [
  '## 安全规范',
  '| 项 | 说明 |',
  '| - | - |',
  '| 必须 | 甲 |',
  '| 必须 | 乙 |',
  '| 必须 | 丙 |',
  '| 必须 | 丁 |',
  '',
  '## 普通说明',
  '一段不含门禁词的散文内容。'.repeat(4),
  '',
  '## 参考索引',
  '| k | v |',
  '| - | - |',
  '| 1 | 2 |',
  '| 3 | 4 |',
  '| 5 | 6 |',
].join('\n')

const tierConflictCases: Record<string, { md: string; budget: number; measureEscaped: boolean }> = {
  // 140 是临界点：恰好丢两个 Reference、保 Prose。分类顺序颠倒会改变结果。
  conflict140: { md: docTierConflict, budget: 140, measureEscaped: false },
  conflict160: { md: docTierConflict, budget: 160, measureEscaped: false },
  conflict180: { md: docTierConflict, budget: 180, measureEscaped: false },
}

// 用例 2c: **两轮预留** 的区分点。
// 前三版用例都**无法暴露**该差异（变异后仍全绿）——根因是用例文档的节数/
// 标题长度分布不够极端，以及手工重建文档时长度对不上（对不上就换了个文档）。
//
// 最终在 **Go 侧**穷举搜索（3000 文档 × 全预算）定位到真实差异点，
// 并把该文档以 JSON 字面量**原样嵌入**（不做手工重建）：
// budget=171 时 real textLen=168 vs onePass textLen=166。
//
// 为什么必须在 Go 侧搜：TS 侧的"单轮"复刻容易写错（note 用全部标题还是
// 实际略去集），混淆了对照物本身，产生假的差异信号。
//
// 教训：变异反证 0 红时，先怀疑用例无效（且对照物可能自身有 bug），
// 再怀疑测试无判别力。连续两轮 0 红 → 换穷举搜索定位，不要继续猜。
const docTwoPass = "## 标标标标\nxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\n## 标\nxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\n## 标标标标标标\nxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\n## 标标标标标标标标标标标标标标标标标标标标标标标\nxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\n## 标标标标标标标标标标标标标标标标标\nxxxxxxxxxxxxxxxxxxxxxxx"

const twoPassCases: Record<string, { md: string; budget: number; measureEscaped: boolean }> = {
  twoPass171: { md: docTwoPass, budget: 171, measureEscaped: false },
  twoPass175: { md: docTwoPass, budget: 175, measureEscaped: false },
  twoPass190: { md: docTwoPass, budget: 190, measureEscaped: false },
  twoPass220: { md: docTwoPass, budget: 220, measureEscaped: false },
}

// 用例 2d: **UTF-16 code unit vs 码点** 的计费差异（代理对）。
//
// JS 的 String.length 是 UTF-16 code unit 数（emoji = 2），不是 Unicode 码点数。
// TS 侧默认 measure 是 `t => t.length`，生产调用方是 `t => escapeXml(t).length`
// ——两者都是 code unit。Go 若用 len([]rune(t))（码点）会分叉。
//
// 这个用例由 Go 侧穷举搜索（20000 随机含 emoji 文档 × 全预算）定位：
// 文档码点 98 / UTF16 122，budget=79 时两种计费给出**截然不同**的选取结果
// （UTF16 全保住 omitted=[]，码点丢 3 节）。差异只在预算临界点出现，
// 普通中文/ASCII 文档完全掩盖它——这是最容易漏的一类分叉。
const docEmoji = "## 节A\nx😀😀xxxxx\n## 节B\n😀😀x😀xxxx\n## 节C\nx😀x😀xxxxx😀x😀xxx😀xx😀xx\n## 节D\n😀😀😀xxxx😀x😀xx😀xx😀😀xx😀xxxxxxxx😀😀x😀x😀"

const emojiCases: Record<string, { md: string; budget: number; measureEscaped: boolean }> = {
  emoji79: { md: docEmoji, budget: 79, measureEscaped: false },
  emoji85: { md: docEmoji, budget: 85, measureEscaped: false },
  emoji95: { md: docEmoji, budget: 95, measureEscaped: false },
  emoji120: { md: docEmoji, budget: 120, measureEscaped: false },
}

const selectCases: Record<string, { md: string; budget: number; measureEscaped: boolean }> = {
  // 预算充足 → 原样返回，无略去
  ample: { md: docMixed, budget: 100000, measureEscaped: false },
  // 预算紧 → 丢参考类，保纪律类
  tight: { md: docMixed, budget: 260, measureEscaped: false },
  // 预算极紧 → 只保得住最高优先级
  veryTight: { md: docMixed, budget: 130, measureEscaped: false },
  // 预算装不下任何节 → 退回原文
  impossible: { md: docMixed, budget: 5, measureEscaped: false },
  // 按转义后长度计费
  escaped: { md: docMixed, budget: 400, measureEscaped: true },
  // 空文档
  emptyDoc: { md: '', budget: 100, measureEscaped: false },
}

// ── 用例 3: selectSections 的贪心与计费 ──────────────────────────
// 直接用 splitSections 的结果喂给 selectSections，覆盖 sep 计费
const greedyDoc = [
  '## 甲',
  'A'.repeat(50),
  '## 乙',
  'B'.repeat(50),
  '## 丙',
  'C'.repeat(50),
].join('\n')
const greedySections = splitSections(greedyDoc)
const greedyBudgets = [0, 10, 55, 56, 60, 120, 170, 300]
const greedy: Record<string, unknown> = {}
for (const b of greedyBudgets) {
  greedy[String(b)] = selectSections(greedySections, b)
}

// ── 用例 4: escapeXml 语义锁定 ───────────────────────────────────
const escapeCases = [
  'plain',
  'a & b',
  '<tag>',
  '"quoted"',
  "'single'", // 不应被转义
  '&<"', // 顺序敏感：& 必须先转
  '&&&',
  '中文 & 符号 <测试>',
]

const out = {
  split: Object.fromEntries(
    Object.entries(splitCases).map(([k, md]) => [
      k,
      { md, sections: splitSections(md).map(s => ({ heading: s.heading, title: s.title, text: s.text, tier: s.tier })) },
    ]),
  ),
  select: Object.fromEntries(
    Object.entries({ ...selectCases, ...tierConflictCases, ...twoPassCases, ...emojiCases }).map(([k, c]) => {
      const measure = c.measureEscaped ? (t: string) => escapeXmlRef(t).length : undefined
      const r = measure
        ? selectProjectInstructions(c.md, c.budget, measure)
        : selectProjectInstructions(c.md, c.budget)
      return [k, { md: c.md, budget: c.budget, measureEscaped: c.measureEscaped, ...r }]
    }),
  ),
  greedy,
  escape: Object.fromEntries(escapeCases.map(t => [t, escapeXmlRef(t)])),
}

mkdirSync(join(here, '..', '..', 'internal', 'prompt', 'data'), { recursive: true })
writeFileSync(join(here, 'oracle.json'), JSON.stringify(out, null, 2) + '\n')

// 指纹：供 Go 侧完整性对账
const sha = createHash('sha256').update(JSON.stringify(out)).digest('hex')
console.error(
  `projinst oracle：${Object.keys(splitCases).length} split / ${Object.keys(selectCases).length} select / ` +
    `${greedyBudgets.length} greedy / ${escapeCases.length} escape — sha256 ${sha.slice(0, 16)}`,
)
