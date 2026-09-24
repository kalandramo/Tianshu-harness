/**
 * oracle 生成器（第五十五刀）—— 从**真实 TS 实现**导出纯函数子集的输出。
 *
 * 覆盖：
 *   - renderPlanMethodologyAdvisory（4 组合：lightweight/full × reason 有无，
 *     外加 planMode 分支）
 *   - renderPermissionNote（全模式）
 *   - renderPlanExecutingBlock（无参）
 *   - appendixBlockName（标签提取 + anon 回退 + 非 ASCII）
 *
 * **为什么必须生成而非手抄**：模板文本长达数百字符（full 档模板 ~600 字符），
 * 手抄必然逐字出错。oracle 把「TS 实现本身」当唯一真相源。
 *
 * 用法：
 *   npx tsx go/testdata/appendix/gen-oracle.ts > go/testdata/appendix/oracle.json
 */
import {
  renderPlanMethodologyAdvisory,
  renderPermissionNote,
  renderPlanExecutingBlock,
  appendixBlockName,
} from '../../../src/prompt/volatile.ts'

// ── renderPlanMethodologyAdvisory ──
// 组合矩阵：methodology(lightweight|full) × reason(有|无) × planMode(true|false)
const methodologyCases: Array<{
  name: string
  methodology: 'lightweight' | 'full' | undefined
  reason?: string
  planMode?: boolean
}> = [
  { name: 'undefined-methodology', methodology: undefined },
  { name: 'lightweight-no-reason', methodology: 'lightweight' },
  { name: 'lightweight-with-reason', methodology: 'lightweight', reason: '任务 scope 内聚' },
  { name: 'full-no-reason', methodology: 'full' },
  { name: 'full-with-reason', methodology: 'full', reason: '跨多模块' },
  { name: 'planMode-no-reason', methodology: 'full', planMode: true },
  { name: 'planMode-with-reason', methodology: 'full', planMode: true, reason: '设计阶段' },
  { name: 'planMode-lightweight', methodology: 'lightweight', planMode: true },
  { name: 'planMode-undefined', methodology: undefined, planMode: true },
]

// ── renderPermissionNote ──
// 覆盖：各审批模式 + undefined + 空串
const permissionModes: Array<string | undefined> = [
  'dangerously-skip-permissions',
  'auto-safe',
  'manual',
  'auto-accept',
  'suggest',
  '',
  undefined,
]

// ── appendixBlockName ──
// 覆盖：常规标签 / 带属性 / 自闭合 / 非 ASCII / 无标签回退 / 边界
const blockNameInputs: string[] = [
  '<git-status>[main] clean</git-status>',
  '<progress ctx="10%">x</progress>',
  '<context-update seq="1">x</context-update>',
  '<星域-advisory level="warning">x</星域-advisory>',
  '<self-closing/>',
  '<self-closing />',
  'no tag at all',
  '',
  '<>',
  '< with-space>x',
  '<tag',
  '<a>',
  '  <indented>x',
  '<ns:tag>x',
  '<tag-with-dash>x',
  // ★ anon 回退的**长度语义**判别用例：
  // TS 用 content.length（UTF-16 code unit）。纯 ASCII 输入下 byte/rune/
  // UTF-16 三种长度无法区分——必须用多字节输入才能钉住真实语义。
  '中文无标签', // 4 字：byte=12, rune=4, utf16=4
  'emoji🎯x', // 🎯 是 BMP 外：byte=10, rune=7, utf16=8
  'a😀b', // 😀 是 BMP 外：byte=7, rune=3, utf16=4
]

const out = {
  meta: {
    source: 'src/prompt/volatile.ts',
    generatedBy: 'go/testdata/appendix/gen-oracle.ts',
    note: '纯函数子集逐值对账：renderPlanMethodologyAdvisory / renderPermissionNote / renderPlanExecutingBlock / appendixBlockName',
  },
  methodology: methodologyCases.map(c => ({
    name: c.name,
    methodology: c.methodology ?? null,
    reason: c.reason ?? null,
    planMode: c.planMode ?? false,
    output: renderPlanMethodologyAdvisory(
      c.methodology,
      c.reason,
      c.planMode === undefined ? undefined : { planMode: c.planMode },
    ),
  })),
  permissionNote: permissionModes.map(mode => ({
    mode: mode ?? null,
    output: renderPermissionNote(mode),
  })),
  planExecuting: {
    output: renderPlanExecutingBlock(),
  },
  blockName: blockNameInputs.map(input => ({
    input,
    output: appendixBlockName(input),
  })),
}

process.stdout.write(JSON.stringify(out, null, 2) + '\n')
