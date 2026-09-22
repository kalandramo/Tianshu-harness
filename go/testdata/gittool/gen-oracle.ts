/**
 * git / commit-audit / sensitive-file-detector / workspace-guard 的差分 oracle。
 *
 * 用法：cd 仓库根 && node_modules/.bin/tsx go/testdata/gittool/gen-oracle.ts > go/testdata/gittool/oracle.json
 */
import { mkdirSync, writeFileSync, rmSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import { tmpdir } from 'node:os'
import { execFileSync } from 'node:child_process'
import { extractTaskTags, auditCommitTagScope } from '../../../src/tools/commit-audit.js'
import { detectSensitiveFile, detectSensitiveGitAdd, AGGREGATE_ADD_MARKER } from '../../../src/tools/sensitive-file-detector.js'
import { createWorkspaceGuard } from '../../../src/agent/workspace-guard.js'
import { GIT_TOOL } from '../../../src/tools/git.js'

const HERE = dirname(fileURLToPath(import.meta.url))
const FIXTURES = join(HERE, 'fixtures')

/**
 * 归一化非确定性字段：耗时、绝对路径。
 *
 * `git status --porcelain` 等输出含项目相对路径（可移植），但错误文案含
 * 绝对路径；`git log` 含 commit hash 与日期（跨仓库不可复现，故 fixture
 * 用固定提交信息但 hash 必然不同）——见各段的处理说明。
 */
const normalize = (s: string): string =>
  s.replace(/[A-Za-z]:\\[^\s"]*|[A-Za-z]:\/[^\s"]*|\/(?:tmp|private|var)\/[^\s"]*/g, '<ABS>')

const out: Record<string, unknown> = {}

// ── extractTaskTags / auditCommitTagScope（纯函数）──
const tagMessages = [
  'feat: add S14 support',
  'fix M1 and C2a together',
  'chore: no tags here',
  'S14',
  'S14 M1',
  'B12x L3',
  'lowercase s14 should not match',
  'S14, M1, C2a',
  'S14S15',
  'xS14',
  'S14-',
  '',
]
out.extractTaskTags = tagMessages.map(message => ({ message, tags: extractTaskTags(message) }))

const auditCases: Array<[string, number]> = [
  ['feat: add S14 support', 3],
  ['feat: add S14 support', 0],
  ['S14 M1', 1],
  ['S14 M1', 2],
  ['S14 M1 C2a', 2],
  ['S14 M1 C2a', 3],
  ['chore: no tags', 0],
  ['chore: no tags', 5],
  ['S14', 1],
]
out.auditCommitTagScope = auditCases.map(([message, fileCount]) => {
  const files = Array.from({ length: fileCount }, (_, i) => `f${i}.ts`)
  return { message, fileCount, result: auditCommitTagScope(message, files) }
})

// ── detectSensitiveFile（纯函数，模式 + 白名单全覆盖）──
const sensitivePaths = [
  // 应命中
  '.env', '.env.local', '.env.production', 'src/.env', '.env.staging',
  'credentials.json', 'a/credentials.yaml', 'credentials', 'a/credentials',
  'id_rsa', 'id_ed25519', '.ssh/id_ecdsa',
  'cert.pem', 'server.key', 'a/b/key.pem',
  '.npmrc', 'a/.pypirc',
  'secrets.json', 'token.yaml', 'auth-token.json', 'auth_token.yml', 'tokens.ini',
  '.netrc', '.git-credentials', 'debug.keystore',
  // 尾部修饰（应等同无修饰）
  '.env/', '.env.', '.env ',
  // 白名单（应放行）
  '.env.example', '.env.template', '.env.sample',
  'a/foo.test.ts', 'b/bar.spec.tsx',
  'README.md', 'docs/guide.md',
  // 不该命中
  'secret.ts', 'token.js', 'credentials.ts', 'auth/token-manager.ts',
  'src/index.ts', 'package.json', 'normal.txt',
  'secrets.md',
  '',
]
out.detectSensitiveFile = sensitivePaths.map(p => {
  const r = detectSensitiveFile(p)
  return { path: p, sensitive: r.sensitive, patternName: r.patternName ?? null }
})

// ── detectSensitiveGitAdd（命令文本）──
const gitAddCommands = [
  'git add .env',
  'git add credentials.json',
  'git add .',
  'git add -A',
  'git add --all',
  'git add .env.example',
  'git add src/index.ts',
  'git add .env src/index.ts',
  'git add -A src/',
  'GIT ADD .env',
  'git  add  .env',
  'git status && git add .env',
  'git add',
  'echo "git add .env"',
  'git add secret.json token.yaml',
  'git add -p .env',
]
out.detectSensitiveGitAdd = gitAddCommands.map(command => ({
  command,
  files: detectSensitiveGitAdd(command),
}))

out.aggregateMarker = AGGREGATE_ADD_MARKER

// ── git 工具（受控仓库）──
//
// **非确定性字段归一化**：commit hash、日期、耗时都随运行变化。
// `git log --oneline --decorate` 含 hash → 归一化 `<HASH>`；
// `git show --stat --format=%h%d` 含 hash → 同样处理。
const normalizeGit = (s: string): string =>
  normalize(s)
    .replace(/\b[0-9a-f]{7,40}\b/g, '<HASH>')
    .replace(/\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}[^\s]*/g, '<DATE>')
    .replace(/stash@\{\d+\}/g, 'stash@{N}')

const repo = join(FIXTURES, 'gitrepo')
rmSync(repo, { recursive: true, force: true })
mkdirSync(join(repo, 'src'), { recursive: true })
const git = (args: string[]) => execFileSync('git', args, { cwd: repo, encoding: 'utf-8', stdio: ['ignore', 'pipe', 'pipe'] })
git(['init', '-q'])
git(['config', 'user.email', 'o@t'])
git(['config', 'user.name', 'o'])
git(['config', 'core.autocrlf', 'false'])
git(['config', 'commit.gpgsign', 'false'])
writeFileSync(join(repo, 'src', 'a.ts'), 'export const a = 1\n')
writeFileSync(join(repo, 'README.md'), '# T\n')
git(['add', '.'])
git(['commit', '-qm', 'init S1'])

const gitCases: Array<[string, any]> = [
  ['status-clean', { action: 'status' }],
  ['diff_summary-clean', { action: 'diff_summary' }],
  ['log', { action: 'log' }],
  ['log-maxCount1', { action: 'log', maxCount: 1 }],
  ['log-maxCount0', { action: 'log', maxCount: 0 }],
  ['log-maxCount999', { action: 'log', maxCount: 999 }],
  ['log_graph', { action: 'log_graph' }],
  ['unknown-action', { action: 'bogus' }],
  ['commit-no-message', { action: 'commit' }],
  ['stash-nothing', { action: 'stash' }],
  ['stash_pop-no-stash', { action: 'stash_pop' }],
]
out.git = []
for (const [name, input] of gitCases) {
  try {
    const r = await GIT_TOOL.execute({ cwd: repo, input, toolUseId: 't' } as any)
    out.git.push({ name, input, content: normalizeGit(r.content), isError: r.isError ?? false })
  } catch (e) {
    out.git.push({ name, input, content: 'THREW: ' + (e as Error).message, isError: true })
  }
}

// 制造改动后的 status / diff_summary
writeFileSync(join(repo, 'src', 'a.ts'), 'export const a = 2\n')
writeFileSync(join(repo, 'src', 'new.ts'), 'export const n = 1\n')
// **非 ASCII 文件名**——锁定 `-c core.quotePath=false` 的效果：
// 不加该前缀时 git 会把中文名八进制转义为 "\346\226\207..."。
writeFileSync(join(repo, 'src', '中文文件.ts'), 'export const zh = 1\n')
writeFileSync(join(repo, '说明文档.md'), '# 中文\n')
for (const [name, input] of [['status-dirty', { action: 'status' }], ['diff_summary-dirty', { action: 'diff_summary' }]] as Array<[string, any]>) {
  const r = await GIT_TOOL.execute({ cwd: repo, input, toolUseId: 't' } as any)
  out.git.push({ name, input, content: normalizeGit(r.content), isError: r.isError ?? false })
}

// 暂存后的 diff_summary
git(['add', 'src/a.ts'])
out.gitStagedSummary = await (async () => {
  const r = await GIT_TOOL.execute({ cwd: repo, input: { action: 'diff_summary' }, toolUseId: 't' } as any)
  return { content: normalizeGit(r.content), isError: r.isError ?? false }
})()

// commit（无归属文件但已暂存 → 提交已暂存内容）
out.gitCommitStaged = await (async () => {
  const r = await GIT_TOOL.execute({ cwd: repo, input: { action: 'commit', message: 'feat: staged only S1' }, toolUseId: 't' } as any)
  return { content: normalizeGit(r.content), isError: r.isError ?? false }
})()

// commit（无归属 + 无暂存 → 报错）
out.gitCommitNothing = await (async () => {
  const r = await GIT_TOOL.execute({ cwd: repo, input: { action: 'commit', message: 'x' }, toolUseId: 't' } as any)
  return { content: normalizeGit(r.content), isError: r.isError ?? false }
})()

// commit（归属含敏感文件 → 拦截）
writeFileSync(join(repo, '.env'), 'SECRET=1\n')
out.gitCommitSensitive = await (async () => {
  const r = await GIT_TOOL.execute({ cwd: repo, input: { action: 'commit', message: 'x' }, ownedFiles: ['.env'], toolUseId: 't' } as any)
  return { content: normalizeGit(r.content), isError: r.isError ?? false }
})()

// commit（归属正常文件 → 成功，含 tag 审计）
writeFileSync(join(repo, 'src', 'b.ts'), 'export const b = 1\n')
out.gitCommitScoped = await (async () => {
  const r = await GIT_TOOL.execute({ cwd: repo, input: { action: 'commit', message: 'feat: scoped S1' }, ownedFiles: ['src/b.ts'], toolUseId: 't' } as any)
  return { content: normalizeGit(r.content), isError: r.isError ?? false }
})()

// commit（多标签但文件少 → 审计警告）
writeFileSync(join(repo, 'src', 'c.ts'), 'export const c = 1\n')
out.gitCommitTagWarning = await (async () => {
  const r = await GIT_TOOL.execute({ cwd: repo, input: { action: 'commit', message: 'feat: S1 M1 C1' }, ownedFiles: ['src/c.ts'], toolUseId: 't' } as any)
  return { content: normalizeGit(r.content), isError: r.isError ?? false }
})()

// ── stash 安全（workspace-guard）──
const stashRepo = join(FIXTURES, 'stashrepo')
rmSync(stashRepo, { recursive: true, force: true })
mkdirSync(stashRepo, { recursive: true })
const sgit = (args: string[]) => execFileSync('git', args, { cwd: stashRepo, encoding: 'utf-8', stdio: ['ignore', 'pipe', 'pipe'] })
sgit(['init', '-q'])
sgit(['config', 'user.email', 'o@t'])
sgit(['config', 'user.name', 'o'])
sgit(['config', 'core.autocrlf', 'false'])
writeFileSync(join(stashRepo, 'f.txt'), 'base\n')
sgit(['add', '.'])
sgit(['commit', '-qm', 'init'])

// 造 stash：改文件 → stash
writeFileSync(join(stashRepo, 'f.txt'), 'stashed version\n')
sgit(['stash', 'push', '-q', '-m', 's1'])

// 场景 A：工作树与 stash **不同** → blocked
writeFileSync(join(stashRepo, 'f.txt'), 'current different\n')
const guardA = createWorkspaceGuard(stashRepo)
out.stashSafetyDifferent = await (async () => {
  const r = await guardA.checkStashSafety('stash@{0}')
  return { blocked: r.blocked, conflicts: r.conflicts, reasons: r.reasons.map(normalizeGit) }
})()

// 场景 B：工作树与 stash **相同** → 不 blocked
writeFileSync(join(stashRepo, 'f.txt'), 'stashed version\n')
out.stashSafetySame = await (async () => {
  const r = await guardA.checkStashSafety('stash@{0}')
  return { blocked: r.blocked, conflicts: r.conflicts, reasons: r.reasons.map(normalizeGit) }
})()

// 场景 C：不存在的 ref → blocked
out.stashSafetyMissingRef = await (async () => {
  const r = await guardA.checkStashSafety('stash@{99}')
  return { blocked: r.blocked, conflicts: r.conflicts, reasons: r.reasons.map(normalizeGit) }
})()

// 场景 D：工作树文件缺失 → missing_current
rmSync(join(stashRepo, 'f.txt'), { force: true })
out.stashSafetyMissingCurrent = await (async () => {
  const r = await guardA.checkStashSafety('stash@{0}')
  return { blocked: r.blocked, conflicts: r.conflicts, reasons: r.reasons.map(normalizeGit) }
})()

// stash_pop（有冲突 → 拒绝）
writeFileSync(join(stashRepo, 'f.txt'), 'conflicting\n')
out.gitStashPopBlocked = await (async () => {
  const r = await GIT_TOOL.execute({ cwd: stashRepo, input: { action: 'stash_pop' }, toolUseId: 't' } as any)
  return { content: normalizeGit(r.content), isError: r.isError ?? false }
})()

process.stdout.write(JSON.stringify(out, null, 2))
