/**
 * project-trust oracle 生成器。
 *
 * 生成：npx tsx go/testdata/trust/gen-oracle.ts
 *
 * ## 覆盖
 *
 * 安全关键语义——**逐键对账**：
 * - `stripUntrustedProjectKeys`：剥离哪些顶层键、哪些嵌套键、保留哪些
 * - `findSensitiveProjectKeys`：点路径的报告形态
 * - `detectProjectTrustStakes`：赌注检测（配置 + hooks）
 *
 * ## 不对账的部分
 *
 * 信任文件读写与 env 覆盖涉及 `rivetHome()`（进程级环境）——oracle 侧用
 * 临时 `RIVET_HOME` 跑真实路径，Go 侧单测独立覆盖。
 */
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createHash } from 'node:crypto'
import {
  stripUntrustedProjectKeys,
  findSensitiveProjectKeys,
  detectProjectTrustStakes,
  PROJECT_CONFIG_FILE_NAME,
} from '../../../src/config/project-trust.js'

const here = dirname(fileURLToPath(import.meta.url))

// ── 1. 剥离语义：逐键对账 ──
const configCases: Array<{ note: string; raw: Record<string, unknown> }> = [
  {
    note: '全部敏感顶层键',
    raw: {
      permissions: { allow: ['*'] }, mcp: { servers: {} }, hooks: {}, env: { A: '1' },
      provider: 'x', providers: {}, search: {}, verify: {}, plugins: [], mirrors: {},
      network: {}, fetch: {},
    },
  },
  {
    note: '嵌套敏感键（agent 下）',
    raw: {
      agent: { approval: 'dangerously-skip-permissions', unsandboxed: true, permissions: {}, model: 'x' },
      ui: { statusLine: 'rm -rf /', theme: 'dark' },
      skills: { importFromClaude: true, other: 1 },
    },
  },
  {
    note: '非敏感键应保留',
    raw: { model: 'deepseek', tools: { preset: 'full' }, ui: { theme: 'dark' } },
  },
  {
    note: '混合：敏感与保留并存',
    raw: {
      hooks: { pre: 'evil' },
      model: 'keep-me',
      agent: { approval: 'yolo', model: 'also-keep' },
      ui: { statusLine: 'evil', theme: 'keep' },
    },
  },
  { note: '空配置', raw: {} },
  {
    note: '嵌套值为数组（不应被当作对象处理）',
    raw: { agent: ['not', 'an', 'object'], ui: null },
  },
  {
    note: '顶层 permissions 与 agent.permissions 并存',
    raw: { permissions: { allow: ['*'] }, agent: { permissions: { deny: [] }, other: 1 } },
  },
]

const stripResults: Record<string, unknown> = {}
const sensitiveResults: Record<string, unknown> = {}
for (const c of configCases) {
  stripResults[c.note] = {
    note: c.note,
    input: c.raw,
    output: stripUntrustedProjectKeys(c.raw),
  }
  sensitiveResults[c.note] = {
    note: c.note,
    input: c.raw,
    found: findSensitiveProjectKeys(c.raw),
  }
}

// ── 2. 赌注检测 ──
const stakesCases: Array<{ note: string; config?: unknown; hooks?: boolean; badJson?: boolean }> = [
  { note: '无配置无 hooks', },
  { note: '有 hooks 无配置', hooks: true },
  { note: '配置有敏感键', config: { hooks: {}, agent: { approval: 'yolo' } } },
  { note: '配置无敏感键', config: { model: 'x' } },
  { note: '配置坏 JSON', badJson: true },
  { note: 'hooks + 敏感配置', hooks: true, config: { mcp: {} } },
]

const stakesResults: Record<string, unknown> = {}
for (const c of stakesCases) {
  const dir = mkdtempSync(join(tmpdir(), 'trust-oracle-'))
  if (c.config !== undefined) {
    writeFileSync(join(dir, PROJECT_CONFIG_FILE_NAME), JSON.stringify(c.config))
  } else if (c.badJson) {
    writeFileSync(join(dir, PROJECT_CONFIG_FILE_NAME), '{not json')
  }
  if (c.hooks) {
    mkdirSync(join(dir, '.rivet'), { recursive: true })
    writeFileSync(join(dir, '.rivet', 'hooks.json'), '{}')
  }
  stakesResults[c.note] = {
    note: c.note,
    hasConfig: c.config !== undefined,
    hasBadJson: c.badJson === true,
    hasHooks: c.hooks === true,
    stakes: detectProjectTrustStakes(dir),
  }
  rmSync(dir, { recursive: true, force: true })
}

const out = { strip: stripResults, sensitive: sensitiveResults, stakes: stakesResults }
mkdirSync(here, { recursive: true })
writeFileSync(join(here, 'oracle.json'), JSON.stringify(out, null, 2) + '\n')
const sha = createHash('sha256').update(JSON.stringify(out)).digest('hex')
console.error(`trust oracle：${Object.keys(stripResults).length} 剥离 + ${Object.keys(stakesResults).length} 赌注 — sha256 ${sha.slice(0, 16)}`)
