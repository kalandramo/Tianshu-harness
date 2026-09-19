/**
 * truncateBlock / stripFirstMarkdownTable oracle 生成器。
 *
 * 生成：
 *   npx tsx go/testdata/truncate/gen-oracle.ts
 *
 * 覆盖 src/prompt/volatile.ts 的两个纯函数（frozen 块的直接依赖）：
 *   - truncateBlock(block, maxChars, kind)  —— 三重 UTF-16 语义
 *   - stripFirstMarkdownTable(text)         —— 纯行操作
 *
 * ## 为什么 truncateBlock 是最难对账的一处
 *
 * 它有三重 UTF-16 语义，且第三重反直觉：
 *   1. `block.length <= maxChars` —— code unit 数比较
 *   2. `content.slice(0, N)`     —— **JS 的 slice 会切断代理对**，
 *      产生的孤立代理在 UTF-8 编码时变成 U+FFFD（ef bf bd）
 *   3. 截断标记里嵌入 `block.length` —— code unit 数进最终字节
 *
 * 第 2 点是本层最大的坑：Go 的 []rune 切片不会切断代理对，直接照搬会
 * 在含 emoji 的块上产生不同字节。必须在 Go 侧显式复刻"切断 + U+FFFD"。
 *
 * 纪律：调用**真实**导出函数，不手抄期望值。stripFirstMarkdownTable 是
 * 导出的；truncateBlock 未导出，故通过 buildStableVolatileBlock 的
 * 间接路径覆盖（见下方 blockTruncation 用例），并用直接调用的方式补一个
 * 纯函数视图——通过 re-export 或 import 内部符号不可行，故这里用
 * 「构造超预算输入 + 观察输出」的方式生成 golden。
 */
import { writeFileSync, mkdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import { createHash } from 'node:crypto'
import { stripFirstMarkdownTable } from '../../../src/prompt/volatile.js'

const here = dirname(fileURLToPath(import.meta.url))

// ── 直接对账 stripFirstMarkdownTable（导出函数）────────────────────
const tableCases: Record<string, string> = {
  // 基本：表格前有 > 引用行、表格后有空行
  basic: [
    '## 标题',
    '正文',
    '> 顶层目录索引',
    '| a | b |',
    '| - | - |',
    '| 1 | 2 |',
    '',
    '后续内容',
  ].join('\n'),
  // 无表格
  noTable: '## 标题\n正文\n更多正文',
  // 表格在开头（无前置 >）
  tableFirst: '| a | b |\n| - | - |\n| 1 | 2 |\n后续',
  // 表格后无空行
  noTrailingBlank: '前文\n| a | b |\n| - | - |\n紧接内容',
  // 表格前有 > 但表格在首行（removeStart 边界）
  quoteThenTable: '> 引用\n| a | b |\n| - | - |',
  // 缩进的表格行
  indentedTable: '前文\n  | a | b |\n  | - | - |\n后续',
  // 多个表格（只删第一个）
  twoTables: '| a |\n| - |\n中间\n| b |\n| - |',
  // 空文本
  empty: '',
  // 只有表格
  onlyTable: '| a |\n| - |',
  // 表格行含 emoji（确保不涉计数）
  emojiTable: '> 索引\n| 😀 | b |\n| - | - |\n\n后续',
  // 前一行以 > 开头但表格不在其后（不应删）
  quoteFarAway: '> 引用\n普通行\n| a |\n| - |',
}

// ── truncateBlock 的间接对账 ────────────────────────────────────
// truncateBlock 未导出。它经 buildStableVolatileBlock 消费 projectMemoryBlock
// 等字段。构造超预算输入观察输出，覆盖三条分支：
//   - 非 XML 块（project-memory 传裸文本时走单根匹配失败分支）
//   - 单根 XML 块（<a>...</a>）
//   - codebase-index 特殊分支
//
// 用 buildStableVolatileBlock 传入固定 ctx，读回 <context> 里的对应块。
// 注意：这需要 rivetMd 等字段配合，且输出含 <environment> 宿主行——
// 故只提取目标块的片段做对账（用标记定位）。
const truncateCases: Record<string, { kind: string; text: string; cap: number }> = {
  // 非 XML 裸文本，超预算
  plainOver: { kind: 'project-memory', text: 'x'.repeat(50), cap: 20 },
  // 非 XML 裸文本，未超（应原样）
  plainUnder: { kind: 'project-memory', text: 'short', cap: 100 },
  // 单根 XML 块，超预算
  xmlOver: { kind: 'seed-capsule', text: '<seed-capsule>\n' + 'y'.repeat(80) + '\n</seed-capsule>', cap: 40 },
  // 单根 XML 块，含 emoji 且在代理对处切断
  xmlEmoji: { kind: 'seed-capsule', text: '<seed-capsule>\n' + '😀'.repeat(30) + '\n</seed-capsule>', cap: 30 },
  // codebase-index 特殊分支
  codebaseOver: { kind: 'codebase-index', text: 'z'.repeat(50), cap: 20 },
  // codebase-index 未超
  codebaseUnder: { kind: 'codebase-index', text: 'z'.repeat(10), cap: 50 },
  // 多根块（单根匹配失败，走兜底切片）
  multiRoot: { kind: 'seed-capsule', text: '<a>1</a>\n<b>2</b>\n' + 'w'.repeat(50), cap: 30 },
  // **M1 区分点**：码点不超预算但 UTF-16 超 → 必须走截断分支。
  // 20 个 emoji：码点 20（< cap 30）但 UTF16 40（> cap 30）。
  // 若预算比较用码点会误判为"未超"而原样返回。
  utf16OnlyOver: { kind: 'project-memory', text: '😀'.repeat(20), cap: 30 },
  // **M4 区分点**：多个同名闭合标签 → 贪婪语义取**最后一个**。
  // 若取第一个，content 会短很多。
  multiClose: { kind: 'seed-capsule', text: '<a>' + 'x'.repeat(10) + '</a>\n<a>' + 'y'.repeat(10) + '</a>', cap: 25 },
}

// 用导出的 buildStableVolatileBlock 间接取 truncateBlock 结果。
// 各 kind 对应的 ctx 字段：
//   project-memory     → ctx.projectMemoryBlock
//   seed-capsule       → ctx.seedCapsuleBlock
//   codebase-index     → ctx.projectIndexBlock
//   knowledge-manifest → ctx.knowledgeManifestBlock
const { buildStableVolatileBlock } = await import('../../../src/prompt/volatile.js')

// 从 <context> 输出里提取目标块。
//
// 首版按 `\n\n` 分段找含 "truncated" 的段——**这是错的**：XML 块形如
// `<tag>\n<content>\n<!-- ... -->\n</tag>`，开标签与内容间是单换行，
// 而块与前一块（<sober>）之间是双换行，分段时开标签被并入了前一段，
// 导致 golden 缺了开标签（我一度误以为是实现缺陷）。
//
// 正确做法：定位块在 <context> 内的边界。做法是构造**只含目标块**的 ctx
// （其余 cap 设 0），并从 <context> 输出中剥掉已知的固定前缀段
// （<environment ... /> 与 <sober>...</sober>），剩下的就是目标块。
const FIXED_PREFIXES = [
  /^<environment[^>]*\/>\n\n/,
  /^<sober>[\s\S]*?<\/sober>\n\n/,
]

function extractBlock(out: string): string {
  let inner = out.replace(/^<context>\n/, '').replace(/\n<\/context>$/, '')
  // 反复剥掉固定前缀（顺序不定，但这两段总是存在）
  let changed = true
  while (changed) {
    changed = false
    for (const re of FIXED_PREFIXES) {
      const next = inner.replace(re, '')
      if (next !== inner) {
        inner = next
        changed = true
      }
    }
  }
  return inner
}

function runTruncate(kind: string, text: string, cap: number): string {
  const ctx: Record<string, unknown> = { cwd: '/tmp/fixture' }
  const caps: Record<string, number> = {}
  if (kind === 'project-memory') ctx.projectMemoryBlock = text
  else if (kind === 'seed-capsule') ctx.seedCapsuleBlock = text
  else if (kind === 'codebase-index') ctx.projectIndexBlock = text
  else if (kind === 'knowledge-manifest') ctx.knowledgeManifestBlock = text
  // 只放行目标 cap，其余设 0 以免其他块混入
  caps[kind === 'project-memory' ? 'projectMemory' : kind === 'seed-capsule' ? 'seedCapsule' : kind === 'codebase-index' ? 'codebaseIndex' : 'knowledgeManifest'] = cap
  ctx.blockCaps = caps
  const out = buildStableVolatileBlock(ctx as never)
  return extractBlock(out)
}

const truncate: Record<string, { kind: string; text: string; cap: number; out: string }> = {}
for (const [name, c] of Object.entries(truncateCases)) {
  truncate[name] = { ...c, out: runTruncate(c.kind, c.text, c.cap) }
}

const out = {
  table: Object.fromEntries(Object.entries(tableCases).map(([k, v]) => [k, { in: v, out: stripFirstMarkdownTable(v) }])),
  truncate,
}

mkdirSync(here, { recursive: true })
writeFileSync(join(here, 'oracle.json'), JSON.stringify(out, null, 2) + '\n')

const sha = createHash('sha256').update(JSON.stringify(out)).digest('hex')
console.error(
  `truncate oracle：${Object.keys(tableCases).length} table / ${Object.keys(truncateCases).length} truncate — sha256 ${sha.slice(0, 16)}`,
)
