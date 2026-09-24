/**
 * oracle 生成器（第五十七刀）—— 从**真实 TS 实现**导出 terse 输出风格的判定。
 *
 * 覆盖：
 *   - resolveTersenessFlags（RIVET_TERSE 取值矩阵 × ctx 两 bool）
 *   - renderTersenessNudge（escalate 两分支）
 *
 * **为什么必须生成**：`resolveTersenessFlags` 的判定是三态逻辑
 * （optOut 早返回 / optIn / 未知值），且 `RIVET_TERSE` 有 8 个识别字面量
 * ——手写用例容易漏掉某组取值。oracle 把 TS 实现当唯一真相源。
 *
 * 用法：
 *   npx tsx go/testdata/terseness/gen-oracle.ts > go/testdata/terseness/oracle.json
 */
import {
  resolveTersenessFlags,
  renderTersenessNudge,
} from '../../../src/prompt/volatile.ts'

// ── RIVET_TERSE 取值矩阵 ──
//
// TS 识别的字面量：
//   optOut: '0' | 'false' | 'off' | 'no'
//   optIn:  '1' | 'true'  | 'on'  | 'yes'
// 未识别值（如 'maybe' / ''）既非 optOut 也非 optIn → 只看 ctx。
//
// 额外覆盖：大小写（TS 做 toLowerCase）、前导/尾随空白（TS 做 trim）、
// undefined（env 里没有该键）。
const envValues: Array<{ name: string; value: string | undefined }> = [
  { name: 'unset', value: undefined },
  { name: 'empty', value: '' },
  // optOut 组
  { name: 'zero', value: '0' },
  { name: 'false', value: 'false' },
  { name: 'off', value: 'off' },
  { name: 'no', value: 'no' },
  // optIn 组
  { name: 'one', value: '1' },
  { name: 'true', value: 'true' },
  { name: 'on', value: 'on' },
  { name: 'yes', value: 'yes' },
  // 大小写 / 空白变体（TS 做 toLowerCase + trim）
  { name: 'TRUE-upper', value: 'TRUE' },
  { name: 'False-mixed', value: 'False' },
  { name: 'padded-on', value: '  on  ' },
  { name: 'padded-zero', value: ' 0 ' },
  // 未识别值
  { name: 'unknown-maybe', value: 'maybe' },
  { name: 'unknown-2', value: '2' },
  { name: 'unknown-yep', value: 'yep' },
]

// ctx 组合：tersenessEnabled × tersenessEscalate
const ctxCombos: Array<{ name: string; enabled?: boolean; escalate?: boolean }> = [
  { name: 'ctx-none' },
  { name: 'ctx-enabled', enabled: true },
  { name: 'ctx-escalate', escalate: true },
  { name: 'ctx-both', enabled: true, escalate: true },
  { name: 'ctx-enabled-false', enabled: false },
  { name: 'ctx-escalate-false', escalate: false },
]

const resolveCases: Array<{
  name: string
  envName: string
  envValue: string | undefined
  ctxName: string
  enabled?: boolean
  escalate?: boolean
  result: { enabled: boolean; escalate: boolean }
}> = []

for (const e of envValues) {
  for (const c of ctxCombos) {
    // 构造 env 对象：undefined 表示键不存在
    const env: Record<string, string | undefined> = {}
    if (e.value !== undefined) env['RIVET_TERSE'] = e.value

    const ctx: { tersenessEnabled?: boolean; tersenessEscalate?: boolean } = {}
    if (c.enabled !== undefined) ctx.tersenessEnabled = c.enabled
    if (c.escalate !== undefined) ctx.tersenessEscalate = c.escalate

    resolveCases.push({
      name: `${e.name}__${c.name}`,
      envName: e.name,
      envValue: e.value ?? null,
      ctxName: c.name,
      enabled: c.enabled,
      escalate: c.escalate,
      result: resolveTersenessFlags(ctx, env as NodeJS.ProcessEnv),
    })
  }
}

const out = {
  meta: {
    source: 'src/prompt/volatile.ts',
    generatedBy: 'go/testdata/terseness/gen-oracle.ts',
    note: 'terse 输出风格判定逐值对账：resolveTersenessFlags / renderTersenessNudge',
  },
  resolve: resolveCases,
  nudge: [
    { name: 'no-escalate', escalate: false, output: renderTersenessNudge(false) },
    { name: 'escalate', escalate: true, output: renderTersenessNudge(true) },
    { name: 'default-arg', escalate: undefined, output: renderTersenessNudge() },
  ],
}

process.stdout.write(JSON.stringify(out, null, 2) + '\n')
