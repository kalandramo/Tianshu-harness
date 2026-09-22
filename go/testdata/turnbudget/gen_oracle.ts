// 差分 oracle 生成器：真跑 TS 的 createTurnBudget / TurnBudget 方法。
//
// 用法：node_modules/.bin/tsx go/testdata/turnbudget/gen_oracle.ts > go/testdata/turnbudget/oracle.json
//
// **为什么必须真跑**：`createTurnBudget` 是纯函数（无外部依赖），真跑给出逐字段
// 黄金数据。`<stored>` 包装的算式（chars / preview / refPath 回退）也一并产出。
import { createTurnBudget, BASE_BUDGET_TOKENS, PRESSURE_BUDGET_TOKENS } from '../../../src/agent/turn-budget.js'

interface Case {
  name: string
  rssRatio: number
}

// ── 三档选档（含阈值边界）──
const cases: Case[] = [
  { name: 'rss-0', rssRatio: 0 },
  { name: 'rss-0.5', rssRatio: 0.5 },
  { name: 'rss-0.699', rssRatio: 0.699 },
  { name: 'rss-0.7-boundary', rssRatio: 0.7 },
  { name: 'rss-0.75', rssRatio: 0.75 },
  { name: 'rss-0.849', rssRatio: 0.849 },
  { name: 'rss-0.85-boundary', rssRatio: 0.85 },
  { name: 'rss-0.9', rssRatio: 0.9 },
  { name: 'rss-1.0', rssRatio: 1.0 },
]

const budgetOut = cases.map(c => {
  const b = createTurnBudget(c.rssRatio)
  return {
    name: c.name,
    rssRatio: c.rssRatio,
    maxTokensPerTurn: b.maxTokensPerTurn,
    // 初始状态
    usedTokens0: b.usedTokens,
    exhausted0: b.isExhausted(),
    // consume 后
    afterConsume100: (() => {
      b.consume(100)
      return { used: b.usedTokens, exhausted: b.isExhausted() }
    })(),
    // budgetFraction（TS 侧算式，max>0 时 1 - used/max，否则 1）
    budgetFractionAfter100:
      b.maxTokensPerTurn > 0 ? 1 - b.usedTokens / b.maxTokensPerTurn : 1,
    // reset 后
    afterReset: (() => {
      b.reset()
      return { used: b.usedTokens, exhausted: b.isExhausted() }
    })(),
  }
})

// ── `<stored>` 包装算式（对账 tool-pipeline.ts:1622-1628）──
// 直接复刻该段算式，产出黄金字符串（不依赖 tool-pipeline 的重型依赖）。
const STORED_CASES = [
  { name: 'has-rawpath', content: 'x'.repeat(1000), rawPath: '/tmp/a.raw', tool: 'bash' },
  { name: 'no-rawpath', content: 'x'.repeat(1000), rawPath: undefined, tool: 'grep' },
  { name: 'short-content', content: 'abc', rawPath: '/p.raw', tool: 'read_file' },
  { name: 'empty-content', content: '', rawPath: '/p.raw', tool: 'bash' },
  { name: 'exactly-500', content: 'y'.repeat(500), rawPath: '/p.raw', tool: 'bash' },
  { name: 'multibyte-600', content: '中'.repeat(600), rawPath: '/p.raw', tool: 'bash' },
]

const storedOut = STORED_CASES.map(c => {
  const contentChars = c.content.length
  const preview = c.content.slice(0, 500)
  const refPath = c.rawPath ?? 'unknown'
  const wrapped = `<stored ref="${refPath}" chars=${contentChars} tool="${c.tool}">\n${preview}\n...(turn budget exceeded — use read_file with offset/limit for full content)</stored>`
  return {
    name: c.name,
    content: c.content,
    rawPath: c.rawPath ?? null,
    tool: c.tool,
    contentChars,
    wrapped,
  }
})

// ── consume 的 ceil(len/4) 算式 ──
const consumeOut = [0, 1, 4, 5, 8, 9, 100, 1000].map(n => ({
  len: n,
  tokens: Math.ceil(n / 4),
}))

console.log(
  JSON.stringify(
    {
      constants: { BASE_BUDGET_TOKENS, PRESSURE_BUDGET_TOKENS },
      budgets: budgetOut,
      stored: storedOut,
      consume: consumeOut,
    },
    null,
    1,
  ),
)
