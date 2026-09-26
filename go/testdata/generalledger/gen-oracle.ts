/**
 * general-ledger oracle 生成器 —— 从 TS 真实实现产出真值。
 *
 * 用法（仓库根）：
 *   npx tsx go/testdata/generalledger/gen-oracle.ts
 *
 * 产出 oracle.json：每个纯函数在给定输入下的真实输出。
 * **只对账纯函数**（路径推导 / slug 解析 / markdown 解析）——
 * 文件 I/O（readGeneralLedger / appendGeneralFinding）由 Go 侧用临时目录测。
 */

import {
  starToGeneralSlug,
  generalLedgerPath,
  parseLedgerFamilies,
} from '../../../src/agent/general-ledger.js'
import { writeFileSync } from 'node:fs'

// ── starToGeneralSlug 用例 ──
const slugInputs = [
  // 域 id（小写）
  'tianshu', 'pojun', 'tianfu', 'tianliang', 'tianquan', 'tianji', 'tianxuan',
  'fu', 'wenqu', 'kaiyang', 'yaoguang', 'huagai', 'qiming', 'changgeng',
  'qisha', 'taiyi',
  // 中文名
  '天枢', '破军', '天府', '天梁', '天权', '天机', '天璇', '辅', '文曲',
  '开阳', '瑶光', '华盖', '启明', '长庚', '七杀', '太一',
  // 大小写混合
  'TIANQUAN', 'TianQuan', 'YAOGUANG',
  // EXTRA_GENERAL_SLUGS（星域之外的将星）
  '贪狼',
  // 未知 / 边界
  'unknown', '不存在', '', '   ', '  tianquan  ', ' 天权 ',
]

const slugCases = slugInputs.map(input => ({
  input,
  output: starToGeneralSlug(input),
}))

// ── generalLedgerPath 用例 ──
const pathCases = [
  { cwd: '/tmp/proj', slug: 'tianquan' },
  { cwd: '/a/b/c', slug: 'yaoguang' },
  { cwd: '.', slug: 'fu' },
  { cwd: '/tmp/x', slug: 'tanlang' },
].map(c => ({ input: c, output: generalLedgerPath(c.cwd, c.slug) }))

// ── parseLedgerFamilies 用例 ──
const ledgerSamples = [
  // 空
  '',
  // 单族
  `### 类型断言失败 | recurrenceCount: 1 | lastSeen: 2026-09-26
**signature**: 编译期报错与运行时不符

- 2026-09-26 首次记录
`,
  // 多族
  `### 族A | recurrenceCount: 3 | lastSeen: 2026-09-20
**signature**: 签名A

- 实例1
- 实例2

### 族B | recurrenceCount: 1 | lastSeen: 2026-09-25
**signature**: 签名B

- 实例3
`,
  // 无 signature
  `### 族C | recurrenceCount: 2 | lastSeen: 2026-09-01

- 无签名的实例
`,
  // 标题格式不符（应被忽略）
  `### 缺字段 | 没有 recurrenceCount
### 族D | recurrenceCount: 5 | lastSeen: 2026-09-26
**signature**: 有效
`,
  // 非 ASCII 族名（\S+ 应匹配中文）
  `### 中文族名 | recurrenceCount: 7 | lastSeen: 2026-09-26
**signature**: 中文签名
`,
  // 行尾空白（正则有 \s*$）
  `### 族E | recurrenceCount: 4 | lastSeen: 2026-09-26   
**signature**: 尾空白
`,
  // signature 在下一族之前
  `### 族F | recurrenceCount: 1 | lastSeen: 2026-09-26
内容行
**signature**: 隔了一行

### 族G | recurrenceCount: 2 | lastSeen: 2026-09-26
`,
]

const parseCases = ledgerSamples.map((content, i) => ({
  name: `sample_${i}`,
  input: content,
  output: parseLedgerFamilies(content),
}))

const doc = {
  generatedBy: 'go/testdata/generalledger/gen-oracle.ts',
  slugCases,
  pathCases,
  parseCases,
}

writeFileSync(new URL('./oracle.json', import.meta.url), JSON.stringify(doc, null, 2) + '\n')
// 不打印进度（与既有生成器一致）——成功判据是 oracle.json 落盘且用例数非零，
// Go 侧 loadOracle 会断言。
