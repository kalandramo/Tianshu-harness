// gen_useraccept.ts —— 用户级验收的黄金数据生成器。
//
// **与 gen_oracle.ts 的区别**：那个用合成字符串；这个读**真实仓库文件**，
// 覆盖真实世界的多字节分布、真实长度、真实行结构。
//
// 运行：node_modules/.bin/tsx go/testdata/truncation/gen_useraccept.ts > golden.json
import { readFileSync } from 'node:fs'
import { truncateContent, buildPartialView } from '../../../src/tools/truncation.js'

type G = {
  label: string
  kind: 'truncate' | 'partial' | 'skeleton'
  content: string
  filePath: string
  maxChars: number
  keepHead: number
  keepTail: number
  skelLines: number
  skelChars: number
  resultBytes: string
}

const cases: G[] = []
function add(g: Partial<G> & { label: string; content: string; kind: G['kind'] }) {
  cases.push({
    filePath: 'a.ts', maxChars: 0, keepHead: 0, keepTail: 0,
    skelLines: 0, skelChars: 0, ...g,
  } as G)
}

// 真实文件（相对仓库根）。
const REAL_FILES: [string, string][] = [
  ['src/tools/read-file.ts', 'read-file.ts (144KB 源码)'],
  ['src/artifact/summarize.ts', 'summarize.ts (407 行源码)'],
  ['go/HANDOFF.md', 'HANDOFF.md (中文+emoji 混合文档)'],
  ['src/tools/truncation.ts', 'truncation.ts (被移植源)'],
]

for (const [rel, label] of REAL_FILES) {
  let content: string
  try {
    content = readFileSync(rel, 'utf-8')
  } catch {
    console.error(`跳过（不存在）: ${rel}`)
    continue
  }
  // 真实的 cap 值（来自 ComputeModelReadCap 的典型输出）。
  for (const [maxChars, head, tail] of [[120000, 72000, 36000], [8000, 4800, 2400], [3000, 1800, 900], [100, 60, 30]]) {
    add({ label: `${label} truncate(${maxChars})`, kind: 'truncate', content, filePath: rel, maxChars, keepHead: head, keepTail: tail })
  }
  add({ label: `${label} keepTail=0`, kind: 'truncate', content, filePath: rel, maxChars: 100, keepHead: 60, keepTail: 0 })
  // PARTIAL 视图。
  for (const mc of [120000, 8000, 3000, 100, 0]) {
    add({ label: `${label} partial(${mc})`, kind: 'partial', content, filePath: rel, maxChars: mc })
  }
  // SKELETON 形态。
  add({ label: `${label} skeleton`, kind: 'skeleton', content, filePath: rel, maxChars: 8000, skelLines: 5000, skelChars: 200000 })
}

const out = cases.map(c => {
  let result: string
  if (c.kind === 'truncate') {
    result = truncateContent(c.content, c.maxChars, c.keepHead, c.keepTail)
  } else if (c.kind === 'partial') {
    result = buildPartialView(c.content, c.filePath, c.maxChars)
  } else {
    result = buildPartialView(c.content, c.filePath, c.maxChars, { lines: c.skelLines, chars: c.skelChars })
  }
  return { ...c, resultBytes: Buffer.from(result, 'utf8').toString('hex') }
})
console.log(JSON.stringify(out, null, 1))
