import { truncateContent, buildPartialView } from '../../../src/tools/truncation.js'

type C = {
  kind: 'truncate' | 'partial' | 'skeleton'
  content: string
  filePath: string
  maxChars: number
  keepHead: number
  keepTail: number
  skelLines: number
  skelChars: number
}

const cases: C[] = []
function add(c: Partial<C>) {
  cases.push({
    kind: 'truncate', content: '', filePath: 'a.ts', maxChars: 0,
    keepHead: 0, keepTail: 0, skelLines: 0, skelChars: 0, ...c,
  })
}

// ── truncateContent ──
add({ content: 'short', maxChars: 100, keepHead: 10, keepTail: 10 })       // 不触发
add({ content: 'abcdefghij', maxChars: 5, keepHead: 2, keepTail: 2 })      // 常规
add({ content: 'abcdefghij', maxChars: 5, keepHead: 2, keepTail: 0 })      // keepTail=0 陷阱
add({ content: 'abcdefghij', maxChars: 5, keepHead: 0, keepTail: 2 })      // keepHead=0
add({ content: 'abcdefghij', maxChars: 5, keepHead: 0, keepTail: 0 })      // 双 0
add({ content: 'abcdefghij', maxChars: 0, keepHead: 3, keepTail: 3 })      // maxChars=0
add({ content: 'abcdefghij', maxChars: -1, keepHead: 3, keepTail: 3 })     // 负 maxChars
add({ content: '', maxChars: 0, keepHead: 1, keepTail: 1 })                // 空串
add({ content: 'a😀bcdefghij', maxChars: 5, keepHead: 2, keepTail: 2 })    // 代理对切断（头）
add({ content: 'a😀bcdefghij', maxChars: 5, keepHead: 1, keepTail: 3 })    // 代理对切断（尾）
add({ content: '你好世界abcdefghij', maxChars: 8, keepHead: 2, keepTail: 3 }) // 中文
add({ content: 'abcdefghij', maxChars: 5, keepHead: 100, keepTail: 100 })  // 超界
add({ content: 'abcdefghij', maxChars: 5, keepHead: 5, keepTail: 5 })      // 恰好等于长度
add({ content: 'abcdefghij', maxChars: 9, keepHead: 3, keepTail: 3 })      // 差 1
add({ content: '😀😀😀😀😀', maxChars: 5, keepHead: 3, keepTail: 3 })          // 全 emoji

// ── buildPartialView ──
const manyLines = Array.from({ length: 50 }, (_, i) => `line ${i} with some content`).join('\n')
add({ kind: 'partial', content: manyLines, filePath: 'src/a.ts', maxChars: 500 })
add({ kind: 'partial', content: manyLines, filePath: 'src/a.ts', maxChars: 100 })   // 小预算
add({ kind: 'partial', content: manyLines, filePath: 'src/a.ts', maxChars: 0 })     // 零预算（至少 1 行）
add({ kind: 'partial', content: manyLines, filePath: 'src/a.ts', maxChars: -5 })    // 负预算
add({ kind: 'partial', content: 'single', filePath: 'x', maxChars: 500 })           // 单行
add({ kind: 'partial', content: '', filePath: 'x', maxChars: 500 })                 // 空
add({ kind: 'partial', content: 'a\n\n\nb', filePath: 'x', maxChars: 500 })         // 空行
add({ kind: 'partial', content: '😀😀😀\nsecond\nthird', filePath: 'x', maxChars: 100 }) // emoji 行
add({ kind: 'partial', content: '你好世界\n第二行\n第三行', filePath: 'x', maxChars: 200 }) // 中文

// ── buildPartialView（skeleton）──
add({ kind: 'skeleton', content: manyLines, filePath: 'src/a.ts', maxChars: 800, skelLines: 500, skelChars: 99999 })
add({ kind: 'skeleton', content: manyLines, filePath: 'src/a.ts', maxChars: 100, skelLines: 500, skelChars: 99999 })
add({ kind: 'skeleton', content: '', filePath: 'x', maxChars: 500, skelLines: 10, skelChars: 100 })
add({ kind: 'skeleton', content: 'one', filePath: 'x', maxChars: 0, skelLines: 1, skelChars: 3 })

const out = cases.map(c => {
  let result: string
  if (c.kind === 'truncate') {
    result = truncateContent(c.content, c.maxChars, c.keepHead, c.keepTail)
  } else if (c.kind === 'partial') {
    result = buildPartialView(c.content, c.filePath, c.maxChars)
  } else {
    result = buildPartialView(c.content, c.filePath, c.maxChars, { lines: c.skelLines, chars: c.skelChars })
  }
  return { ...c, result, resultBytes: Buffer.from(result, 'utf8').toString('hex') }
})
console.log(JSON.stringify(out, null, 1))
