/**
 * diff / spawn-git 的差分 oracle —— 真跑 TS 原实现。
 *
 * 用法：cd 仓库根 && node_modules/.bin/tsx go/testdata/diff/gen-oracle.ts > go/testdata/diff/oracle.json
 */
import { mkdirSync, writeFileSync, rmSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import { tmpdir } from 'node:os'
import { execFileSync } from 'node:child_process'
import { sanitizeGitEnv, resolveGitCommand } from '../../../src/tools/spawn-git.js'
import { DIFF_TOOL } from '../../../src/tools/diff.js'

const HERE = dirname(fileURLToPath(import.meta.url))
const FIXTURES = join(HERE, 'fixtures')

/**
 * 归一化非确定性字段。
 *
 * `buildModelOutput` 的 header 含 `time=0.3s`（实测耗时），每次运行都不同，
 * **不能逐字节比对**。把它替换为固定占位符——两侧都归一化后即可比对
 * 除耗时外的全部内容（含 `lines=` 与截断标记，那些是确定性的）。
 *
 * `path-escape` 的错误文案含**绝对路径**（`workspace root: D:\...`），
 * 同样归一化——否则 oracle 跨机器不可复现。
 */
const normalize = (s: string): string =>
  s.replace(/time=[\d.]+s/g, 'time=<T>s')
    .replace(/workspace root: [^\s)]+/g, 'workspace root: <ROOT>')
    .replace(/[A-Za-z]:\\[^\s"]*|[A-Za-z]:\/[^\s"]*|\/(?:tmp|private|var)\/[^\s"]*/g, '<ABS>')

const out: Record<string, unknown> = {}

// ── sanitizeGitEnv（纯函数，全量覆盖危险子集）──
const unsafe = ['GIT_DIR', 'GIT_WORK_TREE', 'GIT_INDEX_FILE', 'GIT_OBJECT_DIRECTORY',
  'GIT_ALTERNATE_OBJECT_DIRECTORIES', 'GIT_COMMON_DIR', 'GIT_REPLACE_REF_BASE']
const benign = ['GIT_SSH', 'GIT_EDITOR', 'GIT_AUTHOR_NAME', 'PATH', 'HOME', 'GIT_PAGER']

const envCases: Array<Record<string, string>> = [
  // 全部危险变量
  Object.fromEntries(unsafe.map(k => [k, '/evil'])),
  // 全部良性变量
  Object.fromEntries(benign.map(k => [k, 'keep'])),
  // 混合
  { GIT_DIR: '/evil', GIT_SSH: 'keep', PATH: '/bin', GIT_WORK_TREE: '/evil2' },
  // 大小写变体（Windows 环境变量大小写不敏感）
  { 'git_dir': '/evil', 'Git_Work_Tree': '/evil2', 'GIT_ssh': 'keep' },
  // 空
  {},
  // 前缀相似但不该被剥
  { GIT_DIRECTORY: 'keep', GIT_DIR_X: 'keep', XGIT_DIR: 'keep' },
]
out.sanitizeGitEnv = envCases.map(env => ({
  input: env,
  output: Object.fromEntries(Object.entries(sanitizeGitEnv(env)).sort()),
  removed: Object.keys(env).filter(k => !(k in sanitizeGitEnv(env))).sort(),
}))

// ── resolveGitCommand（注入依赖，避免依赖真实环境）──
const existsSet = new Set(['C:\\Program Files\\Git\\cmd\\git.exe', '/custom/git'])
const deps = { existsSync: (p: string) => existsSet.has(p) }

out.resolveGitCommand = [
  // RIVET_GIT_PATH 覆盖（存在）
  { env: { RIVET_GIT_PATH: '/custom/git' }, platform: 'linux', out: resolveGitCommand({ RIVET_GIT_PATH: '/custom/git' }, { ...deps, platform: 'linux' }) },
  // RIVET_GIT_PATH 覆盖但不存在 → 走后续
  { env: { RIVET_GIT_PATH: '/nope' }, platform: 'linux', out: resolveGitCommand({ RIVET_GIT_PATH: '/nope' }, { ...deps, platform: 'linux' }) },
  // Windows 常见位置命中
  { env: {}, platform: 'win32', out: resolveGitCommand({}, { ...deps, platform: 'win32' }) },
  // Windows 但都不存在 → 回退 'git'
  { env: {}, platform: 'win32', out: resolveGitCommand({}, { existsSync: () => false, platform: 'win32' }) },
  // 非 Windows → 回退 'git'
  { env: {}, platform: 'linux', out: resolveGitCommand({}, { ...deps, platform: 'linux' }) },
]

// ── 纯函数：truncateDiff / splitByFile（经 DIFF_TOOL 不可直接访问，用行为测）──
// 这两者是模块私有，故通过构造 diff 输出来间接覆盖（在真实仓库上）。

// ── diff 工具（受控 git 仓库）──
const repo = join(FIXTURES, 'repo')
rmSync(repo, { recursive: true, force: true })
mkdirSync(join(repo, 'src'), { recursive: true })

const git = (args: string[]) => execFileSync('git', args, { cwd: repo, encoding: 'utf-8', stdio: ['ignore', 'pipe', 'pipe'] })
git(['init', '-q'])
git(['config', 'user.email', 'oracle@test'])
git(['config', 'user.name', 'oracle'])
git(['config', 'core.autocrlf', 'false'])

writeFileSync(join(repo, 'src', 'a.ts'), 'export const a = 1\n')
writeFileSync(join(repo, 'README.md'), '# Test\n')
writeFileSync(join(repo, 'big.txt'), Array.from({ length: 300 }, (_, i) => `line ${i}`).join('\n') + '\n')
git(['add', '.'])
git(['commit', '-qm', 'init'])

// 制造改动
writeFileSync(join(repo, 'src', 'a.ts'), 'export const a = 2\nexport const b = 3\n')
writeFileSync(join(repo, 'big.txt'), Array.from({ length: 400 }, (_, i) => `line ${i}`).join('\n') + '\n')
writeFileSync(join(repo, 'src', 'new.ts'), 'export const n = 1\n')

const diffCases: Array<[string, any]> = [
  ['default', {}],
  ['context1', { context_lines: 1 }],
  ['context0', { context_lines: 0 }],
  ['path-a', { path: 'src/a.ts' }],
  ['path-src', { path: 'src' }],
  ['staged', { staged: true }],
  ['path-nonexistent', { path: 'nope.ts' }],
  ['path-escape', { path: '../outside' }],
]

out.diff = []
for (const [name, input] of diffCases) {
  const r = await DIFF_TOOL.execute({ cwd: repo, input, toolUseId: 't1' } as any)
  // **剔除 rawPath**（含绝对路径，且 Go 侧临时目录不同）。
  out.diff.push({
    name, input,
    content: normalize(r.content),
    uiContent: r.uiContent ? normalize(r.uiContent) : null,
    isError: r.isError ?? false,
  })
}

// ── 归属过滤（**必须在任何 `git add` 之前**）──
//
// `git add` 会把改动移出工作树，若先跑它，`git diff -- <path>` 会返回
// 「无改动」——首版即踩此坑（oracle 的 diffOwned 期望值是「无改动。」，
// 测的不是归属过滤）。

// current_task_only（B1 归属）
out.diffOwned = await (async () => {
  const r = await DIFF_TOOL.execute({ cwd: repo, input: { current_task_only: true }, ownedFiles: ['src/a.ts'], toolUseId: 't3' } as any)
  return { content: normalize(r.content), isError: r.isError ?? false }
})()

// current_task_only 但归属为空 → 走普通 path 逻辑（无 path）→ 全部改动
out.diffOwnedEmpty = await (async () => {
  const r = await DIFF_TOOL.execute({ cwd: repo, input: { current_task_only: true }, ownedFiles: [], toolUseId: 't4' } as any)
  return { content: normalize(r.content), isError: r.isError ?? false }
})()

// current_task_only 且归属全在项目外 → 应报「没有可 diff 的归属文件」
out.diffOwnedOutside = await (async () => {
  const r = await DIFF_TOOL.execute({ cwd: repo, input: { current_task_only: true }, ownedFiles: ['../../outside.ts'], toolUseId: 't5' } as any)
  return { content: normalize(r.content), isError: r.isError ?? false }
})()

// ── 已暂存改动（**最后跑**，它会 `git add` 污染工作树）──
out.diffStaged = await (async () => {
  git(['add', 'src/a.ts'])
  const r = await DIFF_TOOL.execute({ cwd: repo, input: { staged: true }, toolUseId: 't2' } as any)
  return { content: normalize(r.content), isError: r.isError ?? false }
})()

// 无改动场景（干净仓库）。
//
// **同样必须在仓库外**（理由同 diffNotGit）。
const clean = join(tmpdir(), 'rivet-oracle-clean-' + Date.now())
rmSync(clean, { recursive: true, force: true })
mkdirSync(clean, { recursive: true })
execFileSync('git', ['init', '-q'], { cwd: clean })
out.diffClean = await (async () => {
  const r = await DIFF_TOOL.execute({ cwd: clean, input: {}, toolUseId: 't6' } as any)
  return { content: normalize(r.content), isError: r.isError ?? false }
})()
rmSync(clean, { recursive: true, force: true })

// 非 git 仓库 → stderr 非空。
//
// **必须在仓库外**：git 会向上查找仓库根，若放在本仓库的 `fixtures/` 下，
// 它会找到外层仓库而不报错（首版即踩此坑——`diffNotGit` 返回了本仓库的改动）。
const notgit = join(tmpdir(), 'rivet-oracle-notgit-' + Date.now())
rmSync(notgit, { recursive: true, force: true })
mkdirSync(notgit, { recursive: true })
out.diffNotGit = await (async () => {
  const r = await DIFF_TOOL.execute({ cwd: notgit, input: {}, toolUseId: 't7' } as any)
  return { content: normalize(r.content), isError: r.isError ?? false }
})()
rmSync(notgit, { recursive: true, force: true })

// ── 纯函数截断的边界（经行为覆盖）──
//
// `truncateDiff` 是模块私有，只能经 DIFF_TOOL 的行为观测。要触发**单文件
// 截断**（200 行阈值），需要**恰好跨越阈值**的 diff 块——上面的 `big.txt`
// 改动虽大，但 `buildModelOutput` 会先把它截到 116 行，**永远到不了 200**。
//
// 故造一个「单文件恰好 201 行 diff」的仓库：`MAX_LINES_PER_FILE = 200`，
// 201 行触发截断（留 200 + 「另有 1 行」）。
const boundary = join(FIXTURES, 'boundary')
rmSync(boundary, { recursive: true, force: true })
mkdirSync(boundary, { recursive: true })
execFileSync('git', ['init', '-q'], { cwd: boundary })
execFileSync('git', ['config', 'user.email', 'o@t'], { cwd: boundary })
execFileSync('git', ['config', 'user.name', 'o'], { cwd: boundary })
execFileSync('git', ['config', 'core.autocrlf', 'false'], { cwd: boundary })
// 初始：1 行。改动后：200 行 → diff 块 = 1 头 + 199 删 + 200 增 + 若干上下文
// 需精确控制，故直接构造大块：初始空文件，改动写入 N 行。
writeFileSync(join(boundary, 'f.ts'), '')
execFileSync('git', ['add', '.'], { cwd: boundary })
execFileSync('git', ['commit', '-qm', 'init'], { cwd: boundary })

// 逐行数试出「块恰好 201 行」的行数：diff 块 = 1(diff --git) + 1(index)
// + 1(---) + 1(+++) + 1(@@) + N(增行) + 0(无上下文，因初始为空)
// → 5 + N = 201 → N = 196
for (const [name, n] of [['boundary196', 196], ['boundary195', 195], ['boundary197', 197]] as Array<[string, number]>) {
  writeFileSync(join(boundary, 'f.ts'), Array.from({ length: n }, (_, i) => `row ${i}`).join('\n') + '\n')
  const r = await DIFF_TOOL.execute({ cwd: boundary, input: {}, toolUseId: 'tb' } as any)
  out['diff_' + name] = { n, content: normalize(r.content), isError: r.isError ?? false }
  execFileSync('git', ['add', '.'], { cwd: boundary })
  execFileSync('git', ['commit', '-qm', 'c' + n], { cwd: boundary })
}

process.stdout.write(JSON.stringify(out, null, 2))
