import { foldCode } from '../../../src/compact/code-fold.js'

// 对账 `foldCode`（428 行，五分支）。**真跑 TS 原实现**（无外部依赖）。

type C = { label: string; filePath: string; content: string; maxLines: number }

const cases: C[] = []
function add(c: C) { cases.push(c) }

// ── ts-like：真实感源码 ──
const TS_SRC = [
  "import { foo } from './foo'",
  "import type { Bar } from './bar'",
  "",
  "export interface Config {",
  "  name: string",
  "  value: number",
  "}",
  "",
  "export type Alias = Config | null",
  "",
  "export function alpha(x: number) {",
  "  const y = x + 1",
  "  return y",
  "}",
  "",
  "function hidden() {",
  "  return 1",
  "}",
  "",
  "export class Beta {",
  "  private x = 1",
  "  method() {",
  "    return this.x",
  "  }",
  "}",
  "",
  "const arrow = (a: number) => {",
  "  return a * 2",
  "}",
  "",
  "export default function main() {",
  "  console.log('hi')",
  "}",
].join('\n')

// 造足够长（> MIN_LINES_TO_FOLD = 50）的变体。
function padTo(src: string, minLines: number): string {
  const lines = src.split('\n')
  while (lines.length < minLines) lines.push('// filler comment line')
  return lines.join('\n')
}

add({ label: 'ts-主路径', filePath: 'a.ts', content: padTo(TS_SRC, 60), maxLines: 200 })
add({ label: 'tsx', filePath: 'a.tsx', content: padTo(TS_SRC, 60), maxLines: 200 })
add({ label: 'js', filePath: 'a.js', content: padTo(TS_SRC, 60), maxLines: 200 })
add({ label: 'mjs', filePath: 'a.mjs', content: padTo(TS_SRC, 60), maxLines: 200 })
// 短文件（< 50）→ wasFolded=false
add({ label: 'ts-短文件', filePath: 'a.ts', content: TS_SRC, maxLines: 200 })
// 恰好 49 / 50 行
add({ label: 'ts-49行', filePath: 'a.ts', content: padTo(TS_SRC, 49), maxLines: 200 })
add({ label: 'ts-50行', filePath: 'a.ts', content: padTo(TS_SRC, 50), maxLines: 200 })
// maxLines 软限
add({ label: 'ts-maxLines=10', filePath: 'a.ts', content: padTo(TS_SRC, 200), maxLines: 10 })
add({ label: 'ts-maxLines=1', filePath: 'a.ts', content: padTo(TS_SRC, 100), maxLines: 1 })
add({ label: 'ts-maxLines=0', filePath: 'a.ts', content: padTo(TS_SRC, 100), maxLines: 0 })
// 块在下一行开（`{` 单独一行）
add({
  label: 'ts-块在下一行',
  filePath: 'a.ts',
  content: padTo(
    "export function nextLineBlock()\n{\n  const a = 1\n  return a\n}\n" +
      Array.from({ length: 50 }, () => 'const x = 1').join('\n'),
    60,
  ),
  maxLines: 200,
})
// 单行自闭合（net 0）
add({
  label: 'ts-单行自闭合',
  filePath: 'a.ts',
  content: padTo(
    "export const inline = { a: 1, b: 2 }\n" +
      Array.from({ length: 55 }, () => 'const y = 2').join('\n'),
    60,
  ),
  maxLines: 200,
})
// 字符串里的花括号（countNetBraces 的字符串感知）
add({
  label: 'ts-字符串花括号',
  filePath: 'a.ts',
  content: padTo(
    'export function withBrace() {\n  const s = "} not a real brace {"\n  return s\n}\n' +
      Array.from({ length: 55 }, () => 'const z = 3').join('\n'),
    60,
  ),
  maxLines: 200,
})
// 未知扩展名
add({ label: '未知扩展名', filePath: 'a.txt', content: padTo(TS_SRC, 60), maxLines: 200 })
add({ label: '无扩展名', filePath: 'noext', content: padTo(TS_SRC, 60), maxLines: 200 })

// ── Python ──
const PY_SRC = [
  "import os",
  "",
  "class Foo:",
  "    def bar(self):",
  "        pass",
  "    def baz(self):",
  "        return 1",
  "",
  "def top():",
  "    pass",
  "",
  "@decorator",
  "def decorated():",
  "    return 2",
].join('\n')
add({ label: 'py-主路径', filePath: 'a.py', content: padTo(PY_SRC, 60), maxLines: 200 })
add({ label: 'pyi', filePath: 'a.pyi', content: padTo(PY_SRC, 60), maxLines: 200 })
add({ label: 'py-短', filePath: 'a.py', content: PY_SRC, maxLines: 200 })

// ── JSON ──
add({
  label: 'json-嵌套',
  filePath: 'a.json',
  content: JSON.stringify(
    { name: 'x', nested: { a: 1, b: { c: 2 } }, arr: [1, 2, 3], empty: [], nil: null, num: 5 },
    null,
    2,
  ),
  maxLines: 200,
})
add({ label: 'json-非法', filePath: 'a.json', content: '{not json', maxLines: 200 })
add({ label: 'json-数组', filePath: 'a.json', content: '[1,2,3]', maxLines: 200 })

// ── Markdown ──
add({
  label: 'md-标题与正文',
  filePath: 'a.md',
  content: [
    '# Title',
    'topic sentence',
    'body paragraph one',
    'body paragraph two',
    '## Section',
    'section topic',
    'more body',
    '```',
    'code here',
    '```',
    'tail body',
  ].join('\n'),
  maxLines: 200,
})
add({ label: 'md-无标题', filePath: 'a.md', content: 'just\nbody\ntext\nmore\nlines\nand\nmore', maxLines: 200 })
add({ label: 'mdx', filePath: 'a.mdx', content: '# H\nbody\nmore body', maxLines: 200 })

const out = cases.map(c => {
  const r = foldCode(c.content, { filePath: c.filePath, maxLines: c.maxLines })
  return {
    label: c.label,
    filePath: c.filePath,
    content: c.content,
    maxLines: c.maxLines,
    folded: r.folded,
    originalLines: r.originalLines,
    foldedLines: r.foldedLines,
    signatures: r.signatures,
    wasFolded: r.wasFolded,
  }
})
console.log(JSON.stringify(out, null, 1))
