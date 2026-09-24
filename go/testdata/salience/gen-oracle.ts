/**
 * oracle 生成器 —— 从**真实 TS 实现**导出 assignSalience / selectTopKBlocks
 * 的判定结果，供 Go 侧逐值对账。
 *
 * 为什么用生成器而非手写期望值：规则表有 30+ 条前缀分支，手抄必然漏。
 * 生成器把「TS 实现本身」作为唯一真相源——Go 侧只要与它的输出逐值一致，
 * 就不存在"我读漏了一条规则"的风险。
 *
 * 用法：
 *   npx tsx go/testdata/salience/gen-oracle.ts > go/testdata/salience/oracle.json
 */
import { assignSalience, selectTopKBlocks, type SalientBlock } from '../../../src/prompt/volatile.ts'

// ── 1. assignSalience：覆盖全部前缀规则 + 边界 ──
//
// 用例分三组：
//   a) 规则表里的每个前缀（从 TS 源码枚举，含变体）
//   b) 边界：空串、只有前缀无内容、大小写、前导空白
//   c) 默认值路径
const salienceInputs: string[] = [
  // --- 1.0 档 ---
  '<star-domain name="天权">x</star-domain>',
  '<star-domain',
  // --- 0.95 档 ---
  '<plan-mode>\nfoo\n</plan-mode>',
  '<ask-mode>foo</ask-mode>',
  // --- 0.8 档 ---
  '<repair-hint>x</repair-hint>',
  '<星域-advisory>x</星域-advisory>',
  '<historical-lessons>x</historical-lessons>',
  '<mentions><file>a.ts</file></mentions>',
  '<active-plan>x</active-plan>',
  '<progress>turn 3</progress>',
  '<progress ctx="10%">x</progress>',
  // --- 0.75 档 ---
  '<output-style>x</output-style>',
  // --- 0.7 档 ---
  '<task-depth>x</task-depth>',
  '<plan-methodology route="lightweight">x</plan-methodology>',
  '<tool-context>x</tool-context>',
  '<plan-cache-advisory>x</plan-cache-advisory>',
  '<plan-execution-trace status="completed">x</plan-execution-trace>',
  '<intent-retrieval-route advisory="true">x</intent-retrieval-route>',
  '<task-progress>x</task-progress>',
  '<decisions>x</decisions>',
  '<worktree-warning>x</worktree-warning>',
  '<git-status>[main] clean</git-status>',
  '<recent-commits>abc123</recent-commits>',
  // --- 0.6 档 ---
  '<available-skills note="x">y</available-skills>',
  // --- 0.4 档 ---
  '<session-state>x</session-state>',
  '<cross-session-memory>x</cross-session-memory>',
  // --- 0.3 档 ---
  '<read-file-dedup-hint>x</read-file-dedup-hint>',
  // --- 默认 0.5 档 ---
  '<unknown-block>x</unknown-block>',
  '<context-update seq="1">x</context-update>',
  '<task-anchor>x</task-anchor>',
  '',
  'plain text with no tag',
  // --- 边界：前缀匹配是 startsWith，必须精确到标签边界 ---
  '<progress-bar>x</progress-bar>', // 不是 <progress> 也不是 <progress  → 默认 0.5
  '<git-status-extra>x</git-status-extra>', // startsWith('<git-status') → 0.7
  '<STAR-DOMAIN>x</STAR-DOMAIN>', // 大小写敏感 → 默认 0.5
  '  <git-status>x</git-status>', // 前导空白 → 默认 0.5（startsWith 不 trim）
  '<output-style', // 无闭合尖括号，仍是 startsWith → 0.75
]

// ── 2. selectTopKBlocks：预算裁剪 + 排序稳定性 ──
//
// 关键用例设计：
//   - 同 salience 的块：验证**稳定排序**（保持输入顺序）。JS 的 Array.sort
//     在 V8 是稳定的；Go 的 sort.Slice 不稳定 → 必须用 SliceStable。
//   - 预算边界：恰好放下 / 差一字符 / 只放得下第一个
//   - blockCap：单块超过 maxChars*0.4 且 >= 2000 时截断 + '\n[truncated]'
//   - overhead：第 2 个块起 +2 字符（"\n\n" 分隔符）
type TopKCase = {
  name: string
  blocks: SalientBlock[]
  maxChars: number
}

const mk = (content: string, salience: number, source?: string): SalientBlock =>
  source ? { content, salience, source: source as never } : { content, salience }

const topKCases: TopKCase[] = [
  {
    name: 'stable-same-salience',
    // 5 个同 salience 块——若排序不稳定，输出顺序会乱
    blocks: [
      mk('A', 0.7), mk('B', 0.7), mk('C', 0.7), mk('D', 0.7), mk('E', 0.7),
    ],
    maxChars: 1000,
  },
  {
    name: 'stable-mixed-salience',
    // 交错 salience，验证降序 + 同档保序
    blocks: [
      mk('low1', 0.3), mk('high1', 0.9), mk('low2', 0.3),
      mk('high2', 0.9), mk('mid', 0.5),
    ],
    maxChars: 1000,
  },
  {
    name: 'descending-order',
    blocks: [
      mk('a', 0.1), mk('b', 0.5), mk('c', 0.9), mk('d', 0.7),
    ],
    maxChars: 1000,
  },
  {
    name: 'budget-exact-fit',
    // 每块 10 字符 + 第 2 块起 overhead 2 → 10 + 12 + 12 = 34
    blocks: [mk('0123456789', 0.9), mk('0123456789', 0.8), mk('0123456789', 0.7)],
    maxChars: 34,
  },
  {
    name: 'budget-one-char-short',
    // 同上但少 1 字符 → 第 3 块应被跳过
    blocks: [mk('0123456789', 0.9), mk('0123456789', 0.8), mk('0123456789', 0.7)],
    maxChars: 33,
  },
  {
    name: 'budget-only-first',
    // 预算只够第一个（10）——第 2 块需 2+10=12，总共 22 > 11
    blocks: [mk('0123456789', 0.9), mk('0123456789', 0.8)],
    maxChars: 11,
  },
  {
    name: 'budget-zero',
    // 预算 0：至少保留一个（最高 salience）
    blocks: [mk('0123456789', 0.9), mk('0123456789', 0.8)],
    maxChars: 0,
  },
  {
    name: 'single-block',
    blocks: [mk('only', 0.5)],
    maxChars: 1000,
  },
  {
    name: 'empty-blocks',
    blocks: [],
    maxChars: 1000,
  },
  {
    name: 'blockCap-truncation',
    // maxChars=10000 → blockCap = max(floor(10000*0.4), 2000) = 4000
    // 内容 5000 字符 > 4000 → 截断到 4000 + '\n[truncated]'(12) = 4012
    blocks: [mk('x'.repeat(5000), 0.9)],
    maxChars: 10000,
  },
  {
    name: 'blockCap-not-hit',
    // 内容 3999 <= 4000 → 不截断
    blocks: [mk('x'.repeat(3999), 0.9)],
    maxChars: 10000,
  },
  {
    name: 'blockCap-floor-2000',
    // maxChars=3000 → floor(3000*0.4)=1200 → max(1200,2000)=2000
    // 内容 2500 > 2000 → 截断到 2000 + 12 = 2012；预算 3000 放得下
    blocks: [mk('y'.repeat(2500), 0.9)],
    maxChars: 3000,
  },
  {
    name: 'truncated-then-budget-skip',
    // 第一块截断后 2012，第二块 2012 → 2012 + 2 + 2012 = 4026 > 3000 → 跳过
    blocks: [mk('a'.repeat(2500), 0.9), mk('b'.repeat(2500), 0.8)],
    maxChars: 3000,
  },
  {
    name: 'preserve-source-metadata',
    // source 元数据必须原样带出（TS 注释明确要求）
    blocks: [mk('keep', 0.9, 'projection'), mk('drop-me-not', 0.8, 'advisory-appendix')],
    maxChars: 1000,
  },
  {
    name: 'skip-does-not-stop',
    // 超预算的块是 continue 而非 break——后面的小块若放得下仍应入选
    blocks: [
      mk('a'.repeat(100), 0.9), // 100
      mk('b'.repeat(100), 0.8), // 100+2+100=202 > 150 → 跳过
      mk('c', 0.7), // 100+2+1=103 <= 150 → 入选（continue 语义）
    ],
    maxChars: 150,
  },
  {
    name: 'zero-length-content',
    blocks: [mk('', 0.9), mk('x', 0.8)],
    maxChars: 100,
  },
]

const out = {
  meta: {
    source: 'src/prompt/volatile.ts',
    generatedBy: 'go/testdata/salience/gen-oracle.ts',
    note: 'assignSalience + selectTopKBlocks 逐值对账',
  },
  salience: salienceInputs.map(input => ({
    input,
    salience: assignSalience(input),
  })),
  topK: topKCases.map(c => {
    const selected = selectTopKBlocks(c.blocks, c.maxChars)
    return {
      name: c.name,
      maxChars: c.maxChars,
      // 输入顺序的 salience 列表（用于验证稳定排序）
      inputOrder: c.blocks.map(b => b.salience),
      inputContents: c.blocks.map(b => b.content),
      // 输入块的 source（fixture 需据此还原，否则 source 透传无法验证）
      inputSources: c.blocks.map(b => (b as { source?: string }).source ?? null),
      // 选中结果的完整形态
      selected: selected.map(b => ({
        content: b.content,
        salience: b.salience,
        source: (b as { source?: string }).source ?? null,
      })),
    }
  }),
}

process.stdout.write(JSON.stringify(out, null, 2) + '\n')
