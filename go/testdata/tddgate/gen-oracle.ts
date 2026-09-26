/**
 * TDD gate oracle 生成器 —— 从 TS 真实实现产出决策矩阵真值。
 *
 * 用法（仓库根）：
 *   npx tsx go/testdata/tddgate/gen-oracle.ts
 *
 * 产出 go/testdata/tddgate/oracle.json：每组输入 → evaluateTddGate 的真实输出。
 * Go 侧测试逐值对账（不是手写断言——手写会把「我以为的语义」写成期望）。
 */

import { evaluateTddGate, DEFAULT_TDD_GATE_CONFIG, EDIT_TOOLS, type TddGateConfig } from '../../../src/agent/tdd-gate.js'
import { writeFileSync } from 'node:fs'

interface Case {
  name: string
  gateState: {
    filesModified: number
    verifications: number
    editsSinceLastTest: number
    hasFailedTests: boolean
    hasCodeEdits: boolean
    hasReadTestFiles: boolean
  }
  toolName: string
  config: TddGateConfig
  targetPath?: string
}

const base = {
  filesModified: 0,
  verifications: 0,
  editsSinceLastTest: 0,
  hasFailedTests: false,
  hasCodeEdits: true,
  hasReadTestFiles: true,
}

const ENFORCE: TddGateConfig = { ...DEFAULT_TDD_GATE_CONFIG, mode: 'enforce' }
const DISABLED: TddGateConfig = { ...DEFAULT_TDD_GATE_CONFIG, enabled: false }
const NO_SKIP: TddGateConfig = { ...ENFORCE, skipIfNoTests: false }

const cases: Case[] = []
const add = (name: string, over: Partial<Case['gateState']>, toolName: string, config: TddGateConfig, targetPath?: string) =>
  cases.push({ name, gateState: { ...base, ...over }, toolName, config, ...(targetPath !== undefined ? { targetPath } : {}) })

// --- 分支 1：config 关闭 → allow（任何输入） ---
add('disabled-blocks-nothing', { filesModified: 9, editsSinceLastTest: 99, hasFailedTests: true }, 'edit_file', DISABLED)

// --- 分支 2：非 edit 工具 → allow ---
for (const t of ['read_file', 'bash', 'grep', 'run_tests', 'todo', 'git_scout']) {
  add(`non-edit-tool-${t}`, { filesModified: 5, editsSinceLastTest: 9 }, t, ENFORCE)
}

// --- 分支 3：scratch 路径 → allow（即使已超阈值） ---
add('scratch-path-allows', { filesModified: 5, editsSinceLastTest: 9 }, 'edit_file', ENFORCE, '.rivet/scratch/probe.ts')
add('scratch-nested-allows', { filesModified: 5, editsSinceLastTest: 9 }, 'edit_file', ENFORCE, 'a/b/.rivet/scratch/x.ts')

// --- 分支 4：无代码编辑（纯文档）→ allow ---
add('no-code-edits-allows', { filesModified: 3, editsSinceLastTest: 9, hasCodeEdits: false }, 'edit_file', ENFORCE)

// --- 分支 5：filesModified === 0 → allow ---
add('zero-files-modified-allows', { filesModified: 0, editsSinceLastTest: 0 }, 'edit_file', ENFORCE)

// --- 分支 6：已有验证 + 无失败 → allow ---
add('verified-no-failure-allows', { filesModified: 2, verifications: 1, editsSinceLastTest: 0 }, 'edit_file', ENFORCE)
add('verified-many-allows', { filesModified: 2, verifications: 7, editsSinceLastTest: 1 }, 'edit_file', ENFORCE)

// --- 分支 7：已有验证 + 有失败 → suggest（失败提示） ---
add('verified-with-failure-suggests', { filesModified: 2, verifications: 3, hasFailedTests: true }, 'edit_file', ENFORCE)
add('verified-with-failure-suggests-default', { filesModified: 2, verifications: 1, hasFailedTests: true }, 'edit_file', DEFAULT_TDD_GATE_CONFIG)

// --- 分支 8：达阈值 + suggest 模式 → suggest（**永不 block**） ---
add('threshold-suggest-mode-suggests', { filesModified: 3, editsSinceLastTest: 3 }, 'edit_file', DEFAULT_TDD_GATE_CONFIG)
add('threshold-suggest-mode-far-over', { filesModified: 3, editsSinceLastTest: 99 }, 'edit_file', DEFAULT_TDD_GATE_CONFIG)

// --- 分支 9：达阈值 + enforce + skipIfNoTests + 未读测试 → suggest（降级） ---
add('enforce-skip-no-tests-read-suggests', { filesModified: 3, editsSinceLastTest: 3, hasReadTestFiles: false }, 'edit_file', ENFORCE)

// --- 分支 10：达阈值 + enforce + 读过测试 → block ---
add('enforce-with-test-read-blocks', { filesModified: 3, editsSinceLastTest: 3, hasReadTestFiles: true }, 'edit_file', ENFORCE)
add('enforce-far-over-blocks', { filesModified: 5, editsSinceLastTest: 10, hasReadTestFiles: true }, 'edit_file', ENFORCE)

// --- 分支 11：达阈值 + enforce + skipIfNoTests=false + 未读测试 → block（不降级） ---
add('enforce-noskip-no-test-read-blocks', { filesModified: 3, editsSinceLastTest: 3, hasReadTestFiles: false }, 'edit_file', NO_SKIP)

// --- 分支 12：达阈值 + enforce + 目标是测试文件 → suggest（RED 步骤豁免） ---
add('enforce-test-target-suggests', { filesModified: 3, editsSinceLastTest: 3, hasReadTestFiles: true }, 'edit_file', ENFORCE, 'src/agent/__tests__/foo.test.ts')
add('enforce-test-target-dot-test', { filesModified: 3, editsSinceLastTest: 3, hasReadTestFiles: true }, 'write_file', ENFORCE, 'src/foo.spec.ts')
add('enforce-test-target-camelcase', { filesModified: 3, editsSinceLastTest: 3, hasReadTestFiles: true }, 'edit_file', ENFORCE, 'src/foo_test.go')

// --- 分支 13：阈值边界（threshold-1 / threshold） ---
add('under-threshold-suggests', { filesModified: 2, editsSinceLastTest: 2 }, 'edit_file', ENFORCE)
add('at-threshold-minus-1', { filesModified: 1, editsSinceLastTest: 1 }, 'edit_file', ENFORCE)
add('exactly-at-threshold-blocks', { filesModified: 3, editsSinceLastTest: 3 }, 'edit_file', ENFORCE)

// --- 分支 14：所有 edit 工具名 ---
for (const t of [...EDIT_TOOLS]) {
  add(`edit-tool-${t}-at-threshold`, { filesModified: 3, editsSinceLastTest: 3, hasReadTestFiles: true }, t, ENFORCE)
}

// --- 分支 15：自定义阈值 ---
add('custom-threshold-1-blocks', { filesModified: 1, editsSinceLastTest: 1, hasReadTestFiles: true }, 'edit_file', { ...ENFORCE, threshold: 1 })
add('custom-threshold-5-under', { filesModified: 3, editsSinceLastTest: 3, hasReadTestFiles: true }, 'edit_file', { ...ENFORCE, threshold: 5 })

const out = cases.map(c => ({
  name: c.name,
  input: { gateState: c.gateState, toolName: c.toolName, config: c.config, targetPath: c.targetPath ?? null },
  output: evaluateTddGate(c.gateState, c.toolName, c.config, c.targetPath),
}))

writeFileSync(new URL('./oracle.json', import.meta.url), JSON.stringify({ generatedBy: 'go/testdata/tddgate/gen-oracle.ts', cases: out }, null, 2) + '\n')
// 生成器不打印进度（与 shellsplit / selfkill / evidence 三个既有生成器一致）。
// 成功判据是 oracle.json 落盘且用例数非零——Go 侧测试会断言这一点
// （`loadTddOracle` 在 0 用例时 Fatal），无需 stdout 回执。
