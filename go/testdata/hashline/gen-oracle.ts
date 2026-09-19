/**
 * hashLine + buildFreshAnchors oracle 生成器。
 *
 * 生成：
 *   npx tsx go/testdata/hashline/gen-oracle.ts
 *
 * 覆盖 src/tools/hash-edit.ts 的两个导出纯函数：
 *   - hashLine(line)        —— 行哈希（sha256 前 8 位）
 *   - buildFreshAnchors()   —— 编辑后生成新鲜锚点
 *
 * ## 为什么先做这两个
 *
 * hash-edit.ts 共 572 行，含锚点解析 / stale 恢复 / 位移查找等复杂逻辑，
 * 但**地基是两个纯函数**——它们决定锚点格式与字节形态。地基不对，上层
 * 全错。且 hashLine 有个易漏的语义：**先剥行尾 `\r` 再哈希**（CRLF 归一化）。
 *
 * 纪律：调用真实导出函数，不手抄。
 */
import { writeFileSync, mkdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import { createHash } from 'node:crypto'
import { hashLine, buildFreshAnchors } from '../../../src/tools/hash-edit.js'

const here = dirname(fileURLToPath(import.meta.url))

// ── hashLine 用例 ────────────────────────────────────────────────
const hashCases: string[] = [
  '',
  'a',
  'hello world',
  '  indented',
  '\ttab',
  '中文行',
  '😀 emoji 行',
  'trailing spaces   ',
  // **关键**：行尾 \r 应被剥掉——故 'x' 与 'x\r' 哈希相同
  'x\r',
  'line with CRLF\r',
  // 只有 \r（剥后为空串）
  '\r',
  // 中间有 \r（不剥，只剥行尾）
  'a\rb',
  // 多字节 + 特殊字符
  'const x = "<tag> & \'q\'"',
  '```',
  '| a | b |',
  // 长行
  'x'.repeat(500),
]

const hashes: Record<string, string> = {}
for (const line of hashCases) {
  hashes[line] = hashLine(line)
}

// 显式锁定 CRLF 归一化不变量
const crlfInvariant = {
  plain: hashLine('abc'),
  withCR: hashLine('abc\r'),
  equal: hashLine('abc') === hashLine('abc\r'),
}

// ── buildFreshAnchors 用例 ──────────────────────────────────────
type FreshCase = { name: string; note?: string; lines: string[]; start0: number; count: number }
const freshCases: FreshCase[] = [
  {
    name: 'middleEdit',
    note: '中间插入：应含前文行、新区间首尾、后文行',
    lines: ['line0', 'line1', 'new1', 'new2', 'line4', 'line5'],
    start0: 2,
    count: 2,
  },
  {
    name: 'atStart',
    note: '编辑在文件开头（无前文行）',
    lines: ['new1', 'new2', 'line2', 'line3'],
    start0: 0,
    count: 2,
  },
  {
    name: 'atEnd',
    note: '编辑在文件末尾（无后文行）',
    lines: ['line0', 'line1', 'new1', 'new2'],
    start0: 2,
    count: 2,
  },
  {
    name: 'pureDeletion',
    note: '纯删除（count=0）——新区间为空',
    lines: ['line0', 'line1', 'line2'],
    start0: 1,
    count: 0,
  },
  {
    name: 'singleLine',
    note: '单行替换',
    lines: ['line0', 'new', 'line2'],
    start0: 1,
    count: 1,
  },
  {
    name: 'wholeFile',
    note: '整个文件被替换（无前后文行）',
    lines: ['new1', 'new2', 'new3'],
    start0: 0,
    count: 3,
  },
  {
    name: 'emptyFile',
    note: '空文件（0 行）',
    lines: [],
    start0: 0,
    count: 0,
  },
  {
    name: 'longLineTruncated',
    note: '**关键**：锚点行内容应截断 80 字符',
    lines: ['line0', 'y'.repeat(200), 'line2'],
    start0: 1,
    count: 1,
  },
  {
    name: 'crlfLines',
    note: '含 \\r 的行——哈希应归一化，内容展示保留原文？',
    lines: ['line0\r', 'new1\r', 'line2\r'],
    start0: 1,
    count: 1,
  },
  {
    name: 'emojiLine',
    note: '含代理对的行',
    lines: ['line0', '😀'.repeat(50), 'line2'],
    start0: 1,
    count: 1,
  },
]

const fresh: Record<string, { lines: string[]; start0: number; count: number; out: string; note?: string }> = {}
for (const c of freshCases) {
  fresh[c.name] = {
    lines: c.lines,
    start0: c.start0,
    count: c.count,
    out: buildFreshAnchors(c.lines, c.start0, c.count),
    note: c.note,
  }
}

const out = { hashes, crlfInvariant, fresh }
mkdirSync(here, { recursive: true })
writeFileSync(join(here, 'oracle.json'), JSON.stringify(out, null, 2) + '\n')

const sha = createHash('sha256').update(JSON.stringify(out)).digest('hex')
console.error(
  `hashline oracle：${hashCases.length} hash / ${freshCases.length} fresh — sha256 ${sha.slice(0, 16)}`,
)
