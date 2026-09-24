/**
 * oracle 生成器（第六十刀）—— 从**真实 TS 实现**导出 plan-mode 纯函数子集的输出。
 *
 * 覆盖（按消费端先行筛选后的 scope）：
 *   - checkPlanMode（off/planning × 工具白名单 × 路径例外 × 委派 profile）
 *   - canonicalizePathForCompare（反斜杠 + 盘符大小写）
 *   - createActivePlanDraftPath（形态断言，非逐值——含 Date.now()）
 *   - formatActivePlanDraftReceipt
 *
 * **不覆盖**（Go 侧无消费方，按「消费端先行」纪律排除）：
 *   - nextShiftTabPlanToggle / shiftTabPlanToggleHint / approvalModeShortLabel
 *     （仅 `main.ts` TUI 层消费）
 *   - profileIsWriteCapable / profileIsPlanModeSafe（在 profile-registry.ts，
 *     独立模块）
 *
 * 用法：
 *   npx tsx go/testdata/planmode/gen-oracle.ts > go/testdata/planmode/oracle.json
 */
import {
  checkPlanMode,
  canonicalizePathForCompare,
  createActivePlanDraftPath,
  formatActivePlanDraftReceipt,
  PLAN_MODE_ALLOWED_TOOLS,
} from '../../../src/agent/plan-mode.ts'

// ── checkPlanMode 用例矩阵 ──
//
// 维度：state(off/planning) × toolName × ctx(targetFilePath / activePlanFilePath /
// delegatesWriteCapableProfile)
type CheckCase = {
  name: string
  state: 'off' | 'planning'
  toolName: string
  cwd?: string
  targetFilePath?: string
  activePlanFilePath?: string | null
  delegatesWriteCapableProfile?: boolean
}

// 固定 cwd（用 POSIX 形，避免平台差异；Windows 形单独用 canonicalize 用例覆盖）
const CWD = '/tmp/rivet-planmode-probe'

const checkCases: CheckCase[] = [
  // ── off 状态：一律放行 ──
  { name: 'off-blocks-nothing-write', state: 'off', toolName: 'write_file', cwd: CWD, targetFilePath: 'src/foo.ts' },
  { name: 'off-blocks-nothing-bash', state: 'off', toolName: 'bash' },
  { name: 'off-blocks-nothing-deliver', state: 'off', toolName: 'deliver_task' },
  // off 状态忽略 write-capable flag（TS 测试明确覆盖）
  { name: 'off-ignores-writecapable-flag', state: 'off', toolName: 'delegate_task', delegatesWriteCapableProfile: true },

  // ── planning：白名单工具放行 ──
  { name: 'planning-allows-read_file', state: 'planning', toolName: 'read_file' },
  { name: 'planning-allows-grep', state: 'planning', toolName: 'grep' },
  { name: 'planning-allows-run_tests', state: 'planning', toolName: 'run_tests' },
  { name: 'planning-allows-delegate_batch', state: 'planning', toolName: 'delegate_batch' },
  { name: 'planning-allows-plan', state: 'planning', toolName: 'plan' },
  { name: 'planning-allows-todo', state: 'planning', toolName: 'todo' },

  // ── planning：非白名单工具拦截 ──
  { name: 'planning-blocks-bash', state: 'planning', toolName: 'bash' },
  { name: 'planning-blocks-git', state: 'planning', toolName: 'git' },
  { name: 'planning-blocks-apply_patch', state: 'planning', toolName: 'apply_patch' },
  { name: 'planning-blocks-deliver_task', state: 'planning', toolName: 'deliver_task' },
  { name: 'planning-blocks-unknown-tool', state: 'planning', toolName: 'nonexistent_tool' },

  // ── planning：写工具 + 活动计划文件例外 ──
  {
    name: 'planning-blocks-write-other-file',
    state: 'planning', toolName: 'write_file', cwd: CWD,
    targetFilePath: 'src/foo.ts', activePlanFilePath: '.rivet/plans/draft-test.md',
  },
  {
    name: 'planning-allows-write-active-plan',
    state: 'planning', toolName: 'write_file', cwd: CWD,
    targetFilePath: '.rivet/plans/draft-test.md', activePlanFilePath: '.rivet/plans/draft-test.md',
  },
  {
    name: 'planning-allows-edit-active-plan',
    state: 'planning', toolName: 'edit_file', cwd: CWD,
    targetFilePath: '.rivet/plans/draft-test.md', activePlanFilePath: '.rivet/plans/draft-test.md',
  },
  // ★ Windows 反斜杠回显
  {
    name: 'planning-allows-write-plan-backslash',
    state: 'planning', toolName: 'write_file', cwd: CWD,
    targetFilePath: '.rivet\\plans\\draft-win.md', activePlanFilePath: '.rivet/plans/draft-win.md',
  },
  // 无 activePlanFilePath 时，写工具一律拦
  {
    name: 'planning-blocks-write-no-active-plan',
    state: 'planning', toolName: 'write_file', cwd: CWD,
    targetFilePath: '.rivet/plans/draft-test.md', activePlanFilePath: null,
  },
  // 缺 cwd / targetFilePath → 走白名单判定（写工具不在白名单 → 拦）
  { name: 'planning-blocks-write-no-ctx', state: 'planning', toolName: 'write_file' },

  // ── planning：scratch 目录放行 ──
  {
    name: 'planning-allows-write-scratch',
    state: 'planning', toolName: 'write_file', cwd: CWD,
    targetFilePath: '.rivet/scratch/probe.py',
  },
  {
    name: 'planning-allows-edit-scratch',
    state: 'planning', toolName: 'edit_file', cwd: CWD,
    targetFilePath: '.rivet/scratch/sub/dir/probe.py',
  },
  // ★ scratch 目录本身（无尾随路径）——startsWith(scratch + '/') 不匹配 → 拦
  {
    name: 'planning-blocks-write-scratch-dir-itself',
    state: 'planning', toolName: 'write_file', cwd: CWD,
    targetFilePath: '.rivet/scratch',
  },
  // ★ 路径穿越：resolve 后逃出 scratch → 拦
  {
    name: 'planning-blocks-scratch-traversal',
    state: 'planning', toolName: 'write_file', cwd: CWD,
    targetFilePath: '.rivet/scratch/../../../etc/passwd',
  },
  // scratch 前缀但非其子目录（.rivet/scratchpad）→ 拦
  {
    name: 'planning-blocks-scratchpad-prefix',
    state: 'planning', toolName: 'write_file', cwd: CWD,
    targetFilePath: '.rivet/scratchpad/x.py',
  },
  // bash 不在「写工具」分支里（只有 write_file/edit_file 走路径例外）→ 拦
  {
    name: 'planning-blocks-bash-even-with-plan-path',
    state: 'planning', toolName: 'bash', cwd: CWD,
    targetFilePath: '.rivet/plans/draft-test.md', activePlanFilePath: '.rivet/plans/draft-test.md',
  },

  // ── planning：委派 profile 安全性 ──
  {
    name: 'planning-blocks-writecapable-delegate_task',
    state: 'planning', toolName: 'delegate_task', delegatesWriteCapableProfile: true,
  },
  {
    name: 'planning-blocks-writecapable-delegate_batch',
    state: 'planning', toolName: 'delegate_batch', delegatesWriteCapableProfile: true,
  },
  {
    name: 'planning-allows-safe-delegate_task',
    state: 'planning', toolName: 'delegate_task', delegatesWriteCapableProfile: false,
  },
  // 非委派工具不受该 flag 影响
  {
    name: 'planning-read_file-unaffected-by-flag',
    state: 'planning', toolName: 'read_file', delegatesWriteCapableProfile: true,
  },
]

// ── canonicalizePathForCompare 用例 ──
const canonInputs: string[] = [
  'C:\\Proj\\.rivet\\plans\\draft-1.md',
  'c:/proj/.rivet/plans/draft-1.md',
  '/tmp/Plans/draft-1.md',
  '/tmp/plans/draft-1.md',
  'C:/Proj/X.md',
  'c:/proj/x.md',
  'relative/path.md',
  'relative\\path.md',
  'D:\\a\\b',
  'd:/a/b',
  '/abs/path',
  '',
  'C:\\',
  'c:/',
]

const out = {
  meta: {
    source: 'src/agent/plan-mode.ts',
    generatedBy: 'go/testdata/planmode/gen-oracle.ts',
    note: 'plan-mode 纯函数子集逐值对账：checkPlanMode / canonicalizePathForCompare / formatActivePlanDraftReceipt',
  },
  check: checkCases.map(c => {
    const ctx: Record<string, unknown> = {}
    if (c.cwd !== undefined) ctx.cwd = c.cwd
    if (c.targetFilePath !== undefined) ctx.targetFilePath = c.targetFilePath
    if (c.activePlanFilePath !== undefined) ctx.activePlanFilePath = c.activePlanFilePath
    if (c.delegatesWriteCapableProfile !== undefined) ctx.delegatesWriteCapableProfile = c.delegatesWriteCapableProfile
    const result = checkPlanMode(c.state, c.toolName, ctx as never)
    return {
      name: c.name,
      state: c.state,
      toolName: c.toolName,
      cwd: c.cwd ?? null,
      targetFilePath: c.targetFilePath ?? null,
      activePlanFilePath: c.activePlanFilePath ?? null,
      delegatesWriteCapableProfile: c.delegatesWriteCapableProfile ?? null,
      allowed: result.allowed,
      // reason 只记是否含 Plan Mode 前缀（全文太长且含前端指引）
      reasonMentionsPlanMode: result.reason ? result.reason.includes('Plan Mode') : false,
      hasReason: result.reason !== undefined,
    }
  }),
  canonicalize: canonInputs.map(input => ({
    input,
    output: canonicalizePathForCompare(input),
  })),
  // 白名单全量（用于 Go 侧对账集合内容）
  allowedTools: [...PLAN_MODE_ALLOWED_TOOLS].sort(),
  // formatActivePlanDraftReceipt：用固定输入（避免 Date.now）
  draftReceipt: {
    // 命中草稿路径时产出 receipt
    hit: formatActivePlanDraftReceipt(CWD, '.rivet/plans/draft-1.md', '.rivet/plans/draft-1.md', 42),
    // 非草稿路径（命名计划文件）→ 空串
    missNamed: formatActivePlanDraftReceipt(CWD, '.rivet/plans/my-plan.md', '.rivet/plans/my-plan.md', 42),
    // 无 activePlanFilePath → 空串
    missNull: formatActivePlanDraftReceipt(CWD, '.rivet/plans/draft-1.md', null, 42),
  },
}

process.stdout.write(JSON.stringify(out, null, 2) + '\n')
