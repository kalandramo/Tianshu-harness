/**
 * filediff oracle 生成器。
 *
 * 生成：npx tsx go/testdata/filediff/gen-oracle.ts
 *
 * ## 覆盖
 *
 * 对账 TS `buildFileDiff` 的**输出字节**（这是自写 Myers 算法的最大风险点）：
 * - 标准 unified diff 格式（`@@ -a,b +c,d @@` 头 + 上下文行）
 * - 单行范围的 `,1` 省略约定
 * - 纯插入 / 纯删除 / 全新文件 / 清空文件
 * - 截断（超 maxLines 时的提示行）
 * - 无变化 → 空串
 *
 * 另对账 `computeChangedLineRanges` 的**行范围**（零上下文 hunk）。
 *
 * ## 注
 *
 * `buildFileDiff` 走 cpu-pool（worker）——oracle 里直接调它，pool 不可用时
 * 自动内联兜底，行为一致。
 */
import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createHash } from 'node:crypto'
import { buildFileDiff, computeChangedLineRanges } from '../../../src/tools/edit-diff.js'

const here = dirname(fileURLToPath(import.meta.url))

interface Case {
  note: string
  path: string
  before: string
  after: string
  maxLines?: number
}

const cases: Case[] = [
  { note: '单行替换', path: 'a.ts', before: 'line1\nline2\nline3\n', after: 'line1\nLINE2\nline3\n' },
  { note: '多行替换', path: 'a.ts', before: 'a\nb\nc\nd\ne\n', after: 'a\nB\nC\nd\ne\n' },
  { note: '纯插入', path: 'a.ts', before: 'a\nb\n', after: 'a\nNEW\nb\n' },
  { note: '纯删除', path: 'a.ts', before: 'a\nb\nc\n', after: 'a\nc\n' },
  { note: '全新文件', path: 'new.ts', before: '', after: 'x\ny\n' },
  { note: '清空文件', path: 'a.ts', before: 'x\ny\n', after: '' },
  { note: '无变化', path: 'a.ts', before: 'same\n', after: 'same\n' },
  { note: '仅尾换行差异', path: 'a.ts', before: 'x\n', after: 'x' },
  { note: '首行改动', path: 'a.ts', before: 'FIRST\nb\nc\n', after: 'CHANGED\nb\nc\n' },
  { note: '末行改动', path: 'a.ts', before: 'a\nb\nLAST\n', after: 'a\nb\nEND\n' },
  { note: '相距很远的两处改动', path: 'a.ts',
    before: 'a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n', after: 'A\nb\nc\nd\ne\nf\ng\nh\ni\nJ\n' },
  { note: '相邻改动合并', path: 'a.ts', before: 'a\nb\nc\n', after: 'a\nB\nC\n' },
  { note: '空行改动', path: 'a.ts', before: 'a\n\nb\n', after: 'a\n\n\nb\n' },
  { note: 'Windows 路径归一化', path: 'src\\a.ts', before: 'x\n', after: 'y\n' },
  { note: '截断（maxLines=3）', path: 'a.ts',
    before: 'a\nb\nc\nd\ne\n', after: 'A\nB\nC\nD\nE\n', maxLines: 3 },
  { note: '中文内容', path: 'a.ts', before: '第一行\n第二行\n', after: '第一行\n改过的第二行\n' },
  { note: '重复行（Myers 歧义）', path: 'a.ts', before: 'x\nx\nx\n', after: 'x\nx\n' },
  { note: '大段重排', path: 'a.ts', before: '1\n2\n3\n4\n5\n6\n7\n8\n', after: '8\n7\n6\n5\n4\n3\n2\n1\n' },
]

const results: Record<string, unknown> = {}

for (const c of cases) {
  const diff = await buildFileDiff(c.path, c.before, c.after,
    c.maxLines !== undefined ? { maxLines: c.maxLines } : undefined)
  const ranges = await computeChangedLineRanges(c.before, c.after)
  results[c.note] = {
    note: c.note,
    path: c.path,
    before: c.before,
    after: c.after,
    maxLines: c.maxLines ?? null,
    diff,
    ranges: ranges.map(r => ({ start: r.start, end: r.end })),
  }
}

mkdirSync(here, { recursive: true })
writeFileSync(join(here, 'oracle.json'), JSON.stringify(results, null, 2) + '\n')
const sha = createHash('sha256').update(JSON.stringify(results)).digest('hex')
console.error(`filediff oracle：${Object.keys(results).length} 用例 — sha256 ${sha.slice(0, 16)}`)
