/**
 * shell 探测 oracle 生成器（resolveShellCommand / resolveGitBashPath）。
 *
 * 生成：
 *   node_modules/.bin/tsx go/testdata/shellprobe/gen-oracle.ts
 *
 * ## 为什么走真实函数调用
 *
 * 这两个函数在 TS 侧**本来就是纯函数 + 注入依赖**（ShellProbeDeps /
 * GitBashProbeDeps），所以可以喂构造好的 deps 直接调真实实现，把返回值
 * 序列化为 golden。Go 侧用**同一组 deps** 断言等价。
 *
 * 这是本项目 oracle 纪律的正例：不手抄优先级顺序表，而是让真实实现产出。
 * （手抄过一次字段序，Go 与 golden 双方同错、测试照绿——见 HANDOFF。）
 *
 * 覆盖 TS 自己的 30+ 用例矩阵：探测顺序、WSL 排除、Scoop 布局、
 * RIVET_USE_POWERSHELL 覆盖、bundled PortableGit 优先级。
 */
import { writeFileSync, mkdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import { createHash } from 'node:crypto'
import { resolveShellCommand, resolveGitBashPath } from '../../../src/platform.js'
import type { ShellProbeDeps, GitBashProbeDeps } from '../../../src/platform.js'

const here = dirname(fileURLToPath(import.meta.url))

// ── 构造 deps 的小工具 ──────────────────────────────────────────
type EnvMap = Record<string, string>

/**
 * 把 env 映射转成 `NodeJS.ProcessEnv` **对象**。
 *
 * ⚠️ 这里踩过一次坑：TS 的 `deps.env` 是**对象**（`NodeJS.ProcessEnv`），
 * 不是取值函数。首版传了函数，于是 `deps.env['RIVET_GIT_BASH_PATH']` 恒为
 * `undefined`——所有 env 相关用例都产出 null/wrong，golden 与真实行为不符。
 * 若照那份 golden 写 Go，两边会同错（正是本项目记录过的假绿事故模式）。
 * oracle 的价值就在于它让这种错误**当场现形**。
 */
const envObj = (m: EnvMap): NodeJS.ProcessEnv => ({ ...m })

/** 构造 GitBashProbeDeps。existsSet 是"存在的路径"集合。 */
function gitBashDeps(opts: {
  isWindows?: boolean
  env?: EnvMap
  whichGit?: string | null
  whichBash?: string | null
  exists?: string[]
}): GitBashProbeDeps {
  const existsSet = new Set(opts.exists ?? [])
  return {
    isWindows: opts.isWindows ?? true,
    env: envObj(opts.env ?? {}),
    whichGit: () => opts.whichGit ?? undefined,
    whichBash: () => opts.whichBash ?? undefined,
    exists: (p: string) => existsSet.has(p),
  }
}

/** 构造 ShellProbeDeps。pwshSet 是"存在的 PowerShell 名"集合。 */
function shellDeps(opts: {
  isWindows?: boolean
  env?: EnvMap
  gitBashPath?: string | null
  hasPwsh?: string[]
}): ShellProbeDeps {
  const pwshSet = new Set(opts.hasPwsh ?? [])
  return {
    isWindows: opts.isWindows ?? true,
    env: envObj(opts.env ?? {}),
    gitBashPath: opts.gitBashPath ?? null,
    hasPwsh: (cmd: string) => pwshSet.has(cmd),
  }
}

// ── Git Bash 路径探测矩阵 ──────────────────────────────────────
const gitBashCases: Array<{ name: string; deps: GitBashProbeDeps }> = [
  { name: 'nonWindows', deps: gitBashDeps({ isWindows: false }) },
  {
    name: 'overrideWins',
    deps: gitBashDeps({
      env: { RIVET_GIT_BASH_PATH: 'D:\\custom\\bash.exe' },
      exists: ['D:\\custom\\bash.exe'],
    }),
  },
  {
    name: 'overrideIgnoredWhenMissing',
    deps: gitBashDeps({
      env: { RIVET_GIT_BASH_PATH: 'D:\\nope\\bash.exe' },
      exists: [],
    }),
  },
  {
    name: 'deriveFromWhereGit',
    deps: gitBashDeps({
      whichGit: 'C:\\Program Files\\Git\\cmd\\git.exe',
      exists: ['C:\\Program Files\\Git\\bin\\bash.exe'],
    }),
  },
  {
    name: 'commonInstallProgramFiles',
    deps: gitBashDeps({ exists: ['C:\\Program Files\\Git\\bin\\bash.exe'] }),
  },
  {
    name: 'commonInstallProgramFilesX86',
    deps: gitBashDeps({ exists: ['C:\\Program Files (x86)\\Git\\bin\\bash.exe'] }),
  },
  {
    name: 'localAppDataPortable',
    deps: gitBashDeps({
      env: { LOCALAPPDATA: 'C:\\Users\\me\\AppData\\Local' },
      exists: ['C:\\Users\\me\\AppData\\Local\\Programs\\Git\\bin\\bash.exe'],
    }),
  },
  {
    name: 'scoopEnvRoot',
    deps: gitBashDeps({
      env: { SCOOP: 'C:\\Scoop' },
      exists: ['C:\\Scoop\\apps\\git\\current\\bin\\bash.exe'],
    }),
  },
  {
    name: 'scoopUserProfile',
    deps: gitBashDeps({
      env: { USERPROFILE: 'C:\\Users\\me' },
      exists: ['C:\\Users\\me\\scoop\\apps\\git\\current\\bin\\bash.exe'],
    }),
  },
  {
    name: 'whereBashFallback',
    deps: gitBashDeps({
      whichBash: 'C:\\tools\\msys64\\usr\\bin\\bash.exe',
      exists: ['C:\\tools\\msys64\\usr\\bin\\bash.exe'],
    }),
  },
  {
    name: 'wslBashExcluded',
    deps: gitBashDeps({
      whichBash: 'C:\\Windows\\System32\\bash.exe',
      exists: ['C:\\Windows\\System32\\bash.exe'],
    }),
  },
  {
    name: 'wslBashExcludedSysWOW64',
    deps: gitBashDeps({
      whichBash: 'C:\\Windows\\SysWOW64\\bash.exe',
      exists: ['C:\\Windows\\SysWOW64\\bash.exe'],
    }),
  },
  {
    name: 'whereGitWinsOverWhereBash',
    deps: gitBashDeps({
      whichGit: 'C:\\Program Files\\Git\\cmd\\git.exe',
      whichBash: 'C:\\other\\bash.exe',
      exists: ['C:\\Program Files\\Git\\bin\\bash.exe', 'C:\\other\\bash.exe'],
    }),
  },
  {
    name: 'bundledIsLastFallback',
    deps: gitBashDeps({
      env: { RIVET_BUNDLED_GIT_DIR: 'C:\\App\\portable-git' },
      exists: ['C:\\App\\portable-git\\bin\\bash.exe'],
    }),
  },
  {
    name: 'systemGitWinsOverBundled',
    deps: gitBashDeps({
      env: { RIVET_BUNDLED_GIT_DIR: 'C:\\App\\portable-git' },
      exists: [
        'C:\\Program Files\\Git\\bin\\bash.exe',
        'C:\\App\\portable-git\\bin\\bash.exe',
      ],
    }),
  },
  {
    name: 'bundledNotExtractedYet',
    deps: gitBashDeps({
      env: { RIVET_BUNDLED_GIT_DIR: 'C:\\App\\portable-git' },
      exists: [],
    }),
  },
  { name: 'nothingFound', deps: gitBashDeps({ exists: [] }) },
]

const gitBash: Record<string, string | null> = {}
for (const c of gitBashCases) {
  gitBash[c.name] = resolveGitBashPath(c.deps)
}

// ── shell 选择矩阵 ──────────────────────────────────────────────
const shellCases: Array<{ name: string; deps: ShellProbeDeps }> = [
  { name: 'nonWindows', deps: shellDeps({ isWindows: false }) },
  {
    name: 'gitBashPreferred',
    deps: shellDeps({ gitBashPath: 'C:\\Program Files\\Git\\bin\\bash.exe' }),
  },
  { name: 'pwshFallback', deps: shellDeps({ hasPwsh: ['pwsh.exe'] }) },
  {
    name: 'powershellWhenOnlyItPresent',
    deps: shellDeps({ hasPwsh: ['powershell.exe'] }),
  },
  {
    name: 'pwshPreferredOverPowershell',
    deps: shellDeps({ hasPwsh: ['pwsh.exe', 'powershell.exe'] }),
  },
  {
    name: 'cmdFallbackViaComSpec',
    deps: shellDeps({ env: { ComSpec: 'C:\\Windows\\system32\\cmd.exe' } }),
  },
  { name: 'cmdDefaultWhenComSpecUnset', deps: shellDeps({}) },
  {
    name: 'forcePwshOverGitBash',
    deps: shellDeps({
      env: { RIVET_USE_POWERSHELL: '1' },
      gitBashPath: 'C:\\Program Files\\Git\\bin\\bash.exe',
      hasPwsh: ['pwsh.exe'],
    }),
  },
  {
    name: 'forcePwshTrue',
    deps: shellDeps({
      env: { RIVET_USE_POWERSHELL: 'true' },
      hasPwsh: ['powershell.exe'],
    }),
  },
  {
    name: 'forcePwshYes',
    deps: shellDeps({
      env: { RIVET_USE_POWERSHELL: 'YES' },
      hasPwsh: ['pwsh.exe'],
    }),
  },
  {
    name: 'forcePwshNoPowershellFallsToCmd',
    deps: shellDeps({
      env: { RIVET_USE_POWERSHELL: '1', ComSpec: 'C:\\Windows\\system32\\cmd.exe' },
      gitBashPath: 'C:\\Program Files\\Git\\bin\\bash.exe',
    }),
  },
  {
    name: 'forcePwshZeroKeepsGitBash',
    deps: shellDeps({
      env: { RIVET_USE_POWERSHELL: '0' },
      gitBashPath: 'C:\\Program Files\\Git\\bin\\bash.exe',
      hasPwsh: ['pwsh.exe'],
    }),
  },
  {
    name: 'forcePwshInvalidValueKeepsGitBash',
    deps: shellDeps({
      env: { RIVET_USE_POWERSHELL: 'maybe' },
      gitBashPath: 'C:\\Program Files\\Git\\bin\\bash.exe',
    }),
  },
]

const shell: Record<string, unknown> = {}
for (const c of shellCases) {
  const r = resolveShellCommand(c.deps)
  shell[c.name] = { cmd: r.cmd, args: r.args, kind: r.kind }
}

const out = { gitBash, shell }

mkdirSync(here, { recursive: true })
writeFileSync(join(here, 'oracle.json'), JSON.stringify(out, null, 2) + '\n')

const sha = createHash('sha256').update(JSON.stringify(out)).digest('hex')
console.error(
  `shellprobe oracle：${gitBashCases.length} 个 git-bash 用例 + ${shellCases.length} 个 shell 用例 — sha256 ${sha.slice(0, 16)}`,
)
