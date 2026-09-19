/**
 * Prompt 引擎 oracle 生成器 —— 从**真实 TS 代码路径**导出 golden。
 *
 * 生成：
 *   npx tsx go/testdata/prompt/gen-oracle.ts
 *
 * 产物 oracle.json 结构：
 *   {
 *     "main": "<BASE_PROMPT 原样，无 calibration>",
 *     "calibrations": { "deepseek": "<base + \n\n + calibration>", "mimo": ..., "glm": ... },
 *     "families": ["deepseek","mimo","glm","openai","anthropic","unknown"],
 *     "detect": { "<modelName>": "<family>" },
 *     "sha256": { "main": "<hex>" }
 *   }
 *
 * 关键纪律：本脚本调用**真实** buildSystemPrompt / detectModelFamily，
 * 不手抄任何字段或文本。Wave 1 的假绿事故（手抄 wire 字段序，双方同错、
 * 测试照绿）正是本纪律的由来——golden 必须来自被测代码自身。
 */
import { writeFileSync, mkdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import { createHash } from 'node:crypto'
import {
  buildSystemPrompt,
  detectModelFamily,
  MAIN_BASE_PROMPT,
  type ModelFamily,
} from '../../../src/prompt/static.js'

const here = dirname(fileURLToPath(import.meta.url))

const FAMILIES: ModelFamily[] = ['deepseek', 'mimo', 'glm', 'openai', 'anthropic', 'unknown']

// 无 calibration 的主控提示词（audience 未设 → BASE_PROMPT 逐字节原样）
const main = buildSystemPrompt({ tools: [] })

// 各模型家族的完整渲染结果（base + '\n\n' + calibration，若有）
const calibrations: Record<string, string> = {}
for (const fam of FAMILIES) {
  calibrations[fam] = buildSystemPrompt({ tools: [], modelFamily: fam })
}

// 模型名 → 家族 的探测映射（覆盖各分支与边界）
const detectNames = [
  'deepseek-v4.1-flash',
  'deepseek-chat',
  'DeepSeek-V3',
  'mimo-7b',
  'MiMo-VL',
  'glm-4.6',
  'GLM-4-Plus',
  'gpt-4o',
  'o1-preview',
  'o3-mini',
  'o4-mini',
  'claude-sonnet-4',
  'claude-opus-4-1',
  'haiku-3.5',
  'unknown-model',
  '',
  'DEEPSEEK-UPPER', // 大小写不敏感验证
  'my-gptish-model', // 子串匹配验证
]
const detect: Record<string, string> = {}
for (const n of detectNames) detect[n] = detectModelFamily(n)

const sha256: Record<string, string> = {
  main: createHash('sha256').update(main, 'utf8').digest('hex'),
  mainBytes: String(Buffer.byteLength(main, 'utf8')),
}

// ── Go 生产数据文件 ────────────────────────────────────────────────
// Go 侧用 go:embed 读这份 JSON 作为提示词文本来源。关键：**由本脚本从
// 真实 TS 输出写出**，绝不手抄——手抄正是 Wave 1 假绿事故的成因。
//
// 存的是 calibration **片段**（而非渲染结果），因为 Go 要实现自己的
// 拼接逻辑（base + '\n\n' + fragment），渲染结果由 oracle.json 对账。
const SEP = '\n\n'
const fragments: Record<string, string> = {}
for (const fam of FAMILIES) {
  const rendered = calibrations[fam]!
  if (rendered === main) continue // 该家族无 calibration
  if (!rendered.startsWith(main + SEP)) {
    throw new Error(
      `不变量破坏：家族 ${fam} 的渲染结果不是 base + ${JSON.stringify(SEP)} + 片段 —— ` +
        `Go 侧的拼接假设失效，必须同步修改两侧`,
    )
  }
  fragments[fam] = rendered.slice(main.length + SEP.length)
}

const goData = {
  // 数据来源与版本指纹（漂移可检测）
  _source: 'go/testdata/prompt/gen-oracle.ts',
  _regen: 'npx tsx go/testdata/prompt/gen-oracle.ts',
  base: main,
  baseBytes: Buffer.byteLength(main, 'utf8'),
  baseSha256: sha256.main,
  separator: SEP,
  calibrations: fragments,
}

const dataDir = join(here, '..', '..', 'internal', 'prompt', 'data')
mkdirSync(dataDir, { recursive: true })
writeFileSync(join(dataDir, 'prompt.json'), JSON.stringify(goData, null, 2) + '\n')

const out = {
  main,
  calibrations,
  families: FAMILIES,
  detect,
  sha256,
  // 自校验：MAIN_BASE_PROMPT 必须与 main 逐字节相同（audience 未设时不得有偏差）
  mainEqualsBasePrompt: main === MAIN_BASE_PROMPT,
}

writeFileSync(
  new URL('oracle.json', import.meta.url),
  JSON.stringify(out, null, 2) + '\n',
)

console.error(
  `oracle 已生成：main ${Buffer.byteLength(main, 'utf8')} 字节 / sha256 ${sha256.main.slice(0, 16)} / ` +
    `${Object.keys(calibrations).length} 家族 / ${Object.keys(detect).length} 探测用例`,
)
console.error(`Go 数据已生成：${Object.keys(fragments).length} 个 calibration 片段 → internal/prompt/data/prompt.json`)
