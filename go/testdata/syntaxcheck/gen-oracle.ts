/**
 * syntax-check oracle 生成器。
 *
 * 生成：npx tsx go/testdata/syntaxcheck/gen-oracle.ts
 *
 * ## 覆盖
 *
 * 对账 TS `checkSyntax` 的**判定结果**（fatal 是否非空）：
 * - `.css` 花括号平衡（含字符串/注释状态机）
 * - `.html` 标签平衡（含 void 元素与自闭合）
 * - `.json` 严格解析
 *
 * ## 不对账的部分（有意）
 *
 * - **消息文本**：Go 与 JS 的解析器错误文本天然不同（如 `json.Unmarshal` vs
 *   `JSON.parse`）。oracle 只锁**判定**（fatal 有/无），不锁文本。
 * - **`.go`**：TS 侧不支持 `.go`。Go 版用原生 `go/parser`——这是**生态重映射**，
 *   oracle 无从对账，由 Go 侧单测独立覆盖。
 * - **`.ts/.js/.py`**：需 esbuild / tree-sitter，Go 侧无等价物（已知边界）。
 */
import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createHash } from 'node:crypto'
import { checkSyntax } from '../../../src/tools/syntax-check.js'

const here = dirname(fileURLToPath(import.meta.url))

interface Case {
  file: string
  content: string
  note: string
}

const cases: Case[] = [
  // ── CSS ──
  { file: 'a.css', content: 'body { color: red; }', note: '平衡' },
  { file: 'b.css', content: 'body { color: red;', note: '缺 }' },
  { file: 'c.css', content: 'body { color: red; }}', note: '多余 }' },
  { file: 'd.css', content: '@media (min-width: 100px) { .a { x: 1 } }', note: '嵌套平衡' },
  { file: 'e.css', content: '.a { content: "}"; }', note: '字符串里的 } 不计' },
  { file: 'f.css', content: ".a { content: '}'; }", note: '单引号字符串里的 }' },
  { file: 'g.css', content: '.a { /* } */ }', note: '注释里的 } 不计' },
  { file: 'h.css', content: '.a { /* } ', note: '未闭合注释吞掉后续（应 fatal）' },
  { file: 'i.css', content: '', note: '空文件' },
  { file: 'j.css', content: '.a { content: "\\"}"; }', note: '转义引号后的 }' },

  // ── HTML ──
  { file: 'a.html', content: '<div><span>x</span></div>', note: '平衡' },
  { file: 'b.html', content: '<div><span>x</div>', note: '标签不匹配' },
  { file: 'c.html', content: '<div><span>x</span>', note: '未闭合 div' },
  { file: 'd.html', content: '<br><img src="x"><hr>', note: 'void 元素不入栈' },
  { file: 'e.html', content: '<div/>', note: '自闭合不入栈' },
  { file: 'f.html', content: '</div>', note: '多余的闭合标签' },
  { file: 'g.html', content: '<DIV></div>', note: '大小写不敏感' },
  { file: 'h.html', content: '<div class="a"><p>x</p></div>', note: '带属性' },
  { file: 'i.html', content: '', note: '空文件' },

  // ── JSON ──
  { file: 'a.json', content: '{"a":1}', note: '合法' },
  { file: 'b.json', content: '{"a":1', note: '截断' },
  { file: 'c.json', content: '{"a":1,}', note: '尾逗号' },
  { file: 'd.json', content: '[1,2,3]', note: '数组' },
  { file: 'e.json', content: '', note: '空文件（JSON.parse 空串会抛）' },
  { file: 'f.json', content: 'null', note: '字面量 null' },
  { file: 'g.json', content: '{"a":"b"}extra', note: '尾部多余内容' },

  // ── 未覆盖的扩展名（应一律 OK） ──
  { file: 'a.unknownext', content: 'anything {{{', note: '未知扩展名 → OK' },
  { file: 'a.md', content: '# hi\n```\nunclosed', note: 'md → OK' },
]

const results: Record<string, unknown> = {}
for (const c of cases) {
  const r = await checkSyntax(c.file, c.content)
  results[`${c.file}::${c.note}`] = {
    file: c.file,
    note: c.note,
    content: c.content,
    // **只锁判定**（消息文本跨语言不可比）
    hasFatal: r.fatal !== null,
    hasWarning: r.warning !== null,
  }
}

mkdirSync(here, { recursive: true })
writeFileSync(join(here, 'oracle.json'), JSON.stringify(results, null, 2) + '\n')
const sha = createHash('sha256').update(JSON.stringify(results)).digest('hex')
console.error(`syntaxcheck oracle：${Object.keys(results).length} 用例 — sha256 ${sha.slice(0, 16)}`)
