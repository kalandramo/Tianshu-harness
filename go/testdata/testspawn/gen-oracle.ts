/**
 * resolveTestSpawn oracle 生成器（Windows .cmd runner 处理 + 参数引号消毒）。
 *
 * 生成：
 *   node_modules/.bin/tsx go/testdata/testspawn/gen-oracle.ts
 *
 * ## 为什么值得对账
 *
 * Windows 上 npm/npx 是 `.cmd` shim（不是 .exe）。走 shell 时参数会被
 * cmd.exe 拼接——**仓库可控的字符串**（filter / 路径）若不消毒，可注入
 * （`a&calc`、`%VAR%` 展开）。TS 侧为此写了 CMD_SAFE 引号 + `%"` 消毒。
 *
 * Go 侧的对应缺口：`sanitizeFilter` 去掉了 `&` 等元字符但**保留 `%`**——
 * 在 .cmd 路径上 `%VAR%` 会展开。本 oracle 锁定正确行为。
 *
 * 用例矩阵取自 TS 自己的 `src/tools/__tests__/run-tests-spawn.test.ts`，
 * 另加注入用例（原测试未覆盖 `%` 展开，属补充）。
 */
import { writeFileSync, mkdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import { createHash } from 'node:crypto'
import { resolveTestSpawn } from '../../../src/tools/run-tests.js'
import type { TestSpawnDeps } from '../../../src/tools/run-tests.js'

const here = dirname(fileURLToPath(import.meta.url))

const CWD = 'C:\\proj'
const win = (exists: (p: string) => boolean = () => false): TestSpawnDeps => ({ isWindows: true, exists })
const nix: TestSpawnDeps = { isWindows: false, exists: () => true }

type Case = { name: string; command: string; args: string[]; cwd: string; deps: TestSpawnDeps }

const shim = 'C:\\proj\\node_modules\\.bin\\tsx.cmd'

const cases: Case[] = [
  // ── 非 Windows：一切直接 spawn，不经 shell ──
  { name: 'nix_npm', command: 'npm', args: ['test'], cwd: '/proj', deps: nix },
  { name: 'nix_npx', command: 'npx', args: ['vitest', 'run'], cwd: '/proj', deps: nix },
  { name: 'nix_tsx', command: 'tsx', args: ['--test', 'a.test.ts'], cwd: '/proj', deps: nix },
  { name: 'nix_node', command: 'node', args: ['--test', 'a.test.ts'], cwd: '/proj', deps: nix },
  { name: 'nix_pytest', command: 'pytest', args: ['-q'], cwd: '/proj', deps: nix },

  // ── Windows npm/npx：走 shell（PATH 上的 .cmd shim）──
  { name: 'win_npm', command: 'npm', args: ['test'], cwd: CWD, deps: win() },
  { name: 'win_npx', command: 'npx', args: ['vitest', 'run'], cwd: CWD, deps: win() },

  // ── Windows tsx：优先项目本地 shim，缺失则回落 npx ──
  { name: 'win_tsx_local_shim', command: 'tsx', args: ['--test', 'a.test.ts'], cwd: CWD, deps: win((p) => p === shim) },
  { name: 'win_tsx_fallback_npx', command: 'tsx', args: ['--test', 'a.test.ts'], cwd: CWD, deps: win(() => false) },

  // ── Windows 真可执行文件：直接 spawn ──
  { name: 'win_node', command: 'node', args: ['--test', 'a.test.ts'], cwd: CWD, deps: win() },
  { name: 'win_pytest', command: 'pytest', args: ['-q'], cwd: CWD, deps: win() },

  // ── 引号消毒（核心安全面）──
  { name: 'win_quote_spaces', command: 'npx', args: ['vitest', 'run', 'tests/My Feature.test.ts'], cwd: CWD, deps: win() },
  { name: 'win_no_double_quote', command: 'npx', args: ['"already quoted"'], cwd: CWD, deps: win() },

  // ── 注入用例（TS 原测试未覆盖 % 展开，此处补充）──
  { name: 'win_pct_expand_filter', command: 'npm', args: ['test', '--', 'foo%PATH%'], cwd: CWD, deps: win() },
  { name: 'win_pct_userprofile', command: 'npx', args: ['foo%USERPROFILE%'], cwd: CWD, deps: win() },
  { name: 'win_amp_injection', command: 'npx', args: ['a&calc&b'], cwd: CWD, deps: win() },
  { name: 'win_quote_break', command: 'npx', args: ['foo"&calc&"bar'], cwd: CWD, deps: win() },
  { name: 'win_caret_meta', command: 'npx', args: ['a^b'], cwd: CWD, deps: win() },
  { name: 'win_pipe_meta', command: 'npx', args: ['a|b'], cwd: CWD, deps: win() },
  { name: 'win_lt_gt_meta', command: 'npx', args: ['a<b>c'], cwd: CWD, deps: win() },
  { name: 'win_paren_meta', command: 'npx', args: ['a(b)c'], cwd: CWD, deps: win() },

  // ── 已引号包裹的 token（含 % 时必须重新消毒——引号挡不住变量展开）──
  { name: 'win_quoted_with_pct', command: 'npx', args: ['"%PATH%"'], cwd: CWD, deps: win() },
  { name: 'win_quoted_clean', command: 'npx', args: ['"clean token"'], cwd: CWD, deps: win() },
]

const out: Record<string, { command: string; args: string[]; shell: boolean }> = {}
for (const c of cases) {
  const r = resolveTestSpawn(c.command, c.args, c.cwd, c.deps)
  out[c.name] = { command: r.command, args: r.args, shell: r.shell }
}

mkdirSync(here, { recursive: true })
writeFileSync(join(here, 'oracle.json'), JSON.stringify(out, null, 2) + '\n')

const sha = createHash('sha256').update(JSON.stringify(out)).digest('hex')
console.error(`testspawn oracle：${cases.length} 个用例 — sha256 ${sha.slice(0, 16)}`)
