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

process.stdout.write(JSON.stringify(out, null, 2))
