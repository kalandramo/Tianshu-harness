/**
 * lossy markers 差分 oracle。
 *
 * 生成：npx tsx go/testdata/lossymarkers/gen-oracle.ts
 *
 * 导出 TS 的 `LOSSY_CONTENT_MARKERS` 正则源码 + `isLossyObservation` 的
 * 判定结果，供 Go 侧对账。
 *
 * **注意**：Go 侧只移植**真实产生**的标记（见 lossy_markers.go 的 scope
 * 收窄说明），故 oracle 的「全表」与 Go 表**不要求逐条相等**——测试只断言
 * Go 表是 TS 表的**子集**，且 Go 侧真实标记全部命中。
 */
import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createHash } from 'node:crypto'
import { LOSSY_CONTENT_MARKERS, isLossyObservation } from '../../../src/agent/lossy-markers.js'

const here = dirname(fileURLToPath(import.meta.url))

// ── 正例：应当被判为 lossy 的内容 ──
// 每条锚定一个**真实产生点**（Go 侧有对应产出的优先）。
const positiveCases: Array<{ label: string; content: string }> = [
  { label: 'collapsed-header', content: '[collapsed grep: 14 matches in src/]' },
  { label: 'collapsed-readfile', content: '[collapsed read_file: 42 lines, classes: Foo]' },
  { label: 'output-truncated-footer', content: 'some output\n[output truncated: last 100 of 5000 lines shown — 4900 lines omitted]' },
  { label: 'stdout-truncated', content: 'x\n[stdout truncated: 1000 bytes]' },
  { label: 'stderr-truncated', content: 'x\n[stderr truncated: 500 bytes]' },
  { label: 'partial-view', content: '── PARTIAL view of src/a.ts (200 lines, 8000 chars) ──' },
  { label: 'microcompacted', content: '<microcompacted tool_result id=c1>' },
  { label: 'storm-collapsed', content: '[storm-collapsed: 3 bash calls consolidated]' },
  { label: 'tiered-summary', content: '[tiered-summary: 5 tools]' },
  { label: 'budget-evicted', content: '[budget-evicted: 2000 chars]' },
  { label: 'budget-summarized', content: '[budget-summarized: bash]' },
  { label: 'truncated-tokens', content: '[truncated: 5000 tokens → 2000 token budget for bash]' },
  { label: 'stale-compacted', content: '<stale-compacted round=3>' },
]

// ── 负例：**不得**被判为 lossy（反假阳性规则）──
const negativeCases: Array<{ label: string; content: string }> = [
  { label: 'plain-truncated-word', content: 'the output was truncated by the program' },
  { label: 'plain-collapsed-word', content: 'the bridge collapsed in 1999' },
  { label: 'normal-grep', content: 'src/a.ts:12: export const x = 1' },
  { label: 'normal-read', content: 'export function foo() { return 1 }' },
  { label: 'empty', content: '' },
  { label: 'brackets-no-marker', content: '[INFO] all good' },
  { label: 'mid-line-truncated', content: 'log: truncated_at=12345' },
  // 裸「lines omitted (...)」**不带**预算前缀 —— TS 与 Go 都不认（TS 的三条
  // 模式要求具体前缀如 `turn read budget`）。Go 侧不产生此格式，故不移植。
  { label: 'lines-omitted-bare', content: '... (1234 lines omitted) ...' },
]

const out: Record<string, unknown> = {
  tsMarkers: LOSSY_CONTENT_MARKERS.map((r) => r.source),
  positive: positiveCases.map((c) => ({
    label: c.label,
    content: c.content,
    isLossy: isLossyObservation(c.content),
  })),
  negative: negativeCases.map((c) => ({
    label: c.label,
    content: c.content,
    isLossy: isLossyObservation(c.content),
  })),
}

mkdirSync(here, { recursive: true })
writeFileSync(join(here, 'oracle.json'), JSON.stringify(out, null, 2) + '\n')
const sha = createHash('sha256').update(JSON.stringify(out)).digest('hex')
console.error(
  `lossymarkers oracle：TS 标记 ${LOSSY_CONTENT_MARKERS.length} 条，` +
  `正例 ${positiveCases.length} / 负例 ${negativeCases.length} — sha256 ${sha.slice(0, 16)}`,
)
