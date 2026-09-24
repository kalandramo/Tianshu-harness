/**
 * oracle 生成器（第五十六刀）—— 从**真实 TS 实现**导出三个模式块的输出。
 *
 * 覆盖：
 *   - renderPlanModeBlock（activePlanFilePath: undefined / null / 有值 / 空串）
 *   - renderAskModeBlock（无参）
 *   - renderPlanExitReminder（无参）
 *
 * **为什么必须生成**：renderPlanModeBlock 的实现体 4229 字符（73 行），
 * 手抄必然逐字出错。oracle 把 TS 实现本身当唯一真相源。
 *
 * 用法：
 *   npx tsx go/testdata/modeblocks/gen-oracle.ts > go/testdata/modeblocks/oracle.json
 */
import {
  renderPlanModeBlock,
  renderAskModeBlock,
  renderPlanExitReminder,
} from '../../../src/prompt/volatile.ts'

// renderPlanModeBlock 的入参矩阵。
//
// 参数类型是 `activePlanFilePath?: string | null`——TS 有三个语义不同的取值：
//   - undefined：未提供
//   - null：显式无计划文件
//   - 有值：计划文件路径
//   - ''：空串（边界——truthy 检查会走"无"分支）
const planFileCases: Array<{ name: string; value: string | null | undefined }> = [
  { name: 'undefined', value: undefined },
  { name: 'null', value: null },
  { name: 'empty-string', value: '' },
  { name: 'with-path', value: '.rivet/plans/my-plan.md' },
  { name: 'with-abs-path', value: '/Users/x/proj/.rivet/plans/p.md' },
]

const out = {
  meta: {
    source: 'src/prompt/volatile.ts',
    generatedBy: 'go/testdata/modeblocks/gen-oracle.ts',
    note: '模式块逐字节对账：renderPlanModeBlock / renderAskModeBlock / renderPlanExitReminder',
  },
  planMode: planFileCases.map(c => ({
    name: c.name,
    value: c.value ?? null,
    isUndefined: c.value === undefined,
    output: renderPlanModeBlock(c.value),
  })),
  askMode: {
    output: renderAskModeBlock(),
  },
  planExit: {
    output: renderPlanExitReminder(),
  },
}

process.stdout.write(JSON.stringify(out, null, 2) + '\n')
