import { summarizeFileContent, summarizeGrepResult, summarizeBashOutput } from '../../../src/artifact/summarize.js'

const cases: { kind: string; content: string; arg: string; extra?: number }[] = []

function addFile(name: string, content: string) {
  cases.push({ kind: 'file', content, arg: name })
}

// --- TS/JS ---
addFile('a.ts', [
  "import { foo } from './foo'",
  "import bar from 'bar'",
  "import './side-effect'",
  "export { a, b as c } from './re'",
  "export function alpha() {",
  "  return 1",
  "}",
  "function hidden() {}",
  "export class Beta {",
  "  x = 1",
  "}",
  "class Hidden {}",
  "export const gamma = 1",
  "export { delta }",
  "export { e as f }",
].join('\n'))
addFile('empty.ts', '')
addFile('one.ts', 'const x = 1')
addFile('UPPER.TS', 'export function upper() {}')
addFile('noext', 'plain text')
addFile('a.jsx', 'export function j() {}')
addFile('multi.ts', 'export function a() {\n}\nexport function b() {\n  const x = { y: 1 }\n}\nexport function c() {}')

// --- Python ---
addFile('p.py', [
  "import os",
  "class Foo:",
  "    def bar(self):",
  "        pass",
  "    def baz(self):",
  "        return 1",
  "def top():",
  "    pass",
  "async def atop():",
  "    pass",
].join('\n'))
addFile('blank.py', '')
addFile('indent.py', 'def f():\n    if x:\n        pass\n    return 1\ndef g():\n    pass')
addFile('blankfirst.py', '\ndef f():\n    pass')

// --- Rust ---
addFile('r.rs', [
  "use std::io",
  "pub fn alpha() {",
  "  let x = 1;",
  "}",
  "fn hidden() {}",
  "pub struct S { x: i32 }",
  "enum E { A }",
  "pub trait T {}",
  "impl S {",
  "  fn m(&self) {}",
  "}",
  "impl T for S {}",
].join('\n'))

// --- Go ---
addFile('g.go', 'package main\n\nfunc main() {}\n')

// --- Markdown ---
addFile('m.md', [
  "# Title",
  "text",
  "## Sub",
  "more",
  "### Deep",
  "d",
  "## Sub2",
  "x",
  "# Title2",
  "y",
].join('\n'))
addFile('m.mdx', '# A\n## B\n')
addFile('h.md', 'no headings here')

// --- JSON ---
addFile('j.json', '{\n  "alpha": 1,\n  "beta": { "n": 1 },\n  "gamma": [1,2],\n  "delta": null\n}')
addFile('arr.json', '[1,2,3]')
addFile('bad.json', '{not json')
addFile('empty.json', '')
addFile('nested.json', '{"a":{"b":{"c":1}}}')
addFile('dup.json', '{"a":1,"a":2}')
addFile('dupobj.json', '{"a":1,"a":{"b":2}}')
addFile('duprev.json', '{"a":{"b":2},"a":1}')
addFile('deepkey.json', '{"outer":{"inner":{"x":1}}}')

// --- Generic ---
addFile('x.txt', 'hello\nworld')
addFile('x.yaml', 'a: 1')
// 边界：ext 取值（对账 `split('.').pop()`）
addFile('a.', 'content')
addFile('.hidden', 'content')
addFile('a.b.c.ts', 'export function deep() {}')
// 边界：JS 空白（U+FEFF / NBSP / 全角空格）——Go 的 \s 不含这些
addFile('\uFEFFa.ts', 'export function bom() {}')
addFile('a.ts', 'export\u00A0function nbsp() {}')
addFile('a.ts', '\u3000export function ideo() {}')
// 边界：UTF-16 code unit 截断（BMP 外字符）
addFile('emoji.ts', 'export function \u{1F600}\u{1F600}\u{1F600}() {}')

// --- grep ---
const grepCases: [string, string][] = [
  ['src/a.ts:12:const x = 1\nsrc/a.ts:13:const y = 2\nsrc/b.ts:1:hi', 'const'],
  ['C:/foo/bar.ts:42:match here\nD:/x/y.ts:7:other', 'match'],
  ['a-b/c.ts-5-context line', 'ctx'],
  ['', 'empty'],
  ['plain text no match format', 'p'],
  ['src/a.ts:1:x\nsrc/a.ts:2:y\nsrc/b.ts:3:z\nsrc/c.ts:4:w\nsrc/d.ts:5:v\nsrc/e.ts:6:u\nsrc/f.ts:7:t', 'many'],
  ['a.ts:1:x\n\n\nb.ts:2:y', 'blank-lines'],
  // 边界：行内容只含 U+FEFF（JS `\s` 含 FEFF → 视为空白行被过滤）
  ['a.ts:1:x\n\uFEFF\nb.ts:2:y', 'feff-line'],
  // 边界：FEFF 在行首/行尾（trim 语义）
  ['\uFEFFa.ts:1:x\uFEFF', 'feff-edge'],
]
for (const [content, pat] of grepCases) {
  cases.push({ kind: 'grep', content, arg: pat })
}

// --- bash ---
const bashCases: [string, string, number][] = [
  ['3 tests passed\nall good', 'npm test', 0],
  ['2 tests failed\nError: boom', 'npm test', 1],
  ['line1\nline2', 'ls -la', 0],
  ['', 'true', 0],
  ['error at foo\nError bar\nFAIL baz\nError qux', 'make', 2],
  ['total 100\nok', 'go test', 0],
  ['a very long command that exceeds forty characters for sure here', 'x', 0],
  // 边界：命令含 BMP 外字符 → `command.slice(0,40)` 按 code unit 切
  ['out', '\u{1F600}\u{1F600}\u{1F600}\u{1F600}\u{1F600}\u{1F600}\u{1F600}\u{1F600}\u{1F600}\u{1F600}\u{1F600}\u{1F600}\u{1F600}\u{1F600}\u{1F600}\u{1F600}\u{1F600}\u{1F600}\u{1F600}\u{1F600}\u{1F600}\u{1F600}', 0],
  // 边界：错误行截 60 code unit（含中文）
  ['错误：中文错误信息非常非常非常非常非常非常非常非常非常非常非常非常长的一行', 'make', 1],
  // 边界：末尾空行（last non-empty line 取法）
  ['line1\n\n\n', 'ls', 0],
  // 边界：末行只含 U+FEFF（JS 视为空白 → 应取前一个非空行）
  ['first\n\uFEFF', 'ls', 0],
  // 边界：命令含 FEFF（slice 前的 trim 不含 FEFF 会留脏字符）
  ['out', '\uFEFFcmd here', 0],
]
for (const [content, cmd, ec] of bashCases) {
  cases.push({ kind: 'bash', content, arg: cmd, extra: ec })
}

const out: any[] = []
for (const c of cases) {
  let r
  if (c.kind === 'file') r = summarizeFileContent(c.content, c.arg)
  else if (c.kind === 'grep') r = summarizeGrepResult(c.content, c.arg)
  else r = summarizeBashOutput(c.content, c.arg, c.extra!)
  out.push({ kind: c.kind, content: c.content, arg: c.arg, extra: c.extra ?? 0, summary: r.summary, sections: r.sections })
}
console.log(JSON.stringify(out, null, 1))
