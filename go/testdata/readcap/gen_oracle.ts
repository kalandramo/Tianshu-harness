import { computeModelReadCap } from '../../../src/tools/model-read-cap.js'
import { truncateContent } from '../../../src/tools/truncation.js'

// 对账链路：contextWindow × providerProfile → cap → truncateContent 的**最终输出**。
//
// 这是 read_file 截断的真实路径（read-file.ts:768 → :707），两层一起锁定。

type Prof = { cacheType: string; persistent: boolean } | null
type C = {
  label: string
  contextWindow: number
  profile: Prof
  content: string
  expectMax: number
  expectHead: number
  expectTail: number
  resultBytes: string
}

const cases: C[] = []

// 真实内容样本（含多字节）。
const SAMPLES: [string, string][] = [
  ['ascii', Array.from({ length: 500 }, (_, i) => `line ${i} ascii content here`).join('\n')],
  ['cjk', Array.from({ length: 500 }, (_, i) => `第 ${i} 行中文内容测试`).join('\n')],
  ['emoji', Array.from({ length: 300 }, (_, i) => `row ${i} 😀🎉 mixed`).join('\n')],
  ['short', 'tiny'],
]

const WINDOWS = [0, 32000, 128000, 200000, 1000000]
const PROFILES: [string, Prof][] = [
  ['nil', null],
  ['exact-prefix', { cacheType: 'exact-prefix', persistent: true }],
  ['none', { cacheType: 'none', persistent: false }],
]

for (const [sname, content] of SAMPLES) {
  for (const win of WINDOWS) {
    for (const [pname, prof] of PROFILES) {
      const cap = computeModelReadCap({ contextWindow: win, providerProfile: prof ?? undefined })
      const result = truncateContent(content, cap.maxChars, cap.headChars, cap.tailChars)
      cases.push({
        label: `${sname}/win=${win}/${pname}`,
        contextWindow: win,
        profile: prof,
        content,
        expectMax: cap.maxChars,
        expectHead: cap.headChars,
        expectTail: cap.tailChars,
        resultBytes: Buffer.from(result, 'utf8').toString('hex'),
      })
    }
  }
}

console.log(JSON.stringify(cases, null, 1))
