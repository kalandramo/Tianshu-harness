/**
 * repo_map / inspect_project / file_info 的差分 oracle —— 真跑 TS 原实现。
 *
 * 用法：cd 仓库根 && node_modules/.bin/tsx go/testdata/projmap/gen-oracle.ts > go/testdata/projmap/oracle.json
 */
import { mkdirSync, writeFileSync, rmSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import { classifyPath } from '../../../src/context/attention-filter.js'
import { isScanExcludedDir, SCAN_EXCLUDE_DIRS } from '../../../src/tools/scan-excludes.js'
import { REPO_MAP_TOOL } from '../../../src/tools/repo-map.js'
import { INSPECT_PROJECT_TOOL } from '../../../src/tools/inspect-project.js'
import { FILE_INFO_TOOL, formatPermissions } from '../../../src/tools/file-info.js'

// **固定夹具路径**：Go 测试需在**同一路径**复现同一棵树才能逐字节比对。
// 故不用 tmpdir（每次不同），改用本文件同级的 `fixtures/` 目录。
// 两侧（TS oracle 与 Go 测试）都用同一个 `makeFixture` 逻辑建树。
const HERE = dirname(fileURLToPath(import.meta.url))
const FIXTURES = join(HERE, 'fixtures')

const out: Record<string, unknown> = {}

// ── classifyPath（纯函数，全分支覆盖）──
const classifyInputs = [
  '', '.', './', 'src/a.ts', 'src/agent/loop.ts',
  'node_modules/pkg/index.js', 'dist/bundle.js', 'build/out.js', '.next/x.js',
  'target/debug/app', '__pycache__/x.pyc', 'coverage/lcov.info',
  '.agents/x.md', '.codex/y.json', '.obsidian/z.md', '.claude/a.md', '.cursor/b.md', '.od-skills/c.md',
  '.rivet/sessions/a.jsonl', '.rivet/plans/x.md', '.rivet/cache-log/y.log',
  '.rivet/sensorium/z.json', '.rivet/prefix-diag/p.txt', '.rivet/playbook/pb.md',
  '.rivet/runtime/r.json', '.rivet/tmp/t.txt', '.rivet/external/e.md',
  '.rivet/foo.jsonl', '.rivet/a/b.jsonl',
  '.rivet/meridian.db', '.rivet/meridian.db-shm', '.rivet/meridian.db.wal',
  '.rivet/meridian.db.extra', '.rivet/other.db',
  '.DS_Store', 'sub/Thumbs.db',
  'a.log', 'b.lock', 'c.pid', 'd.swp', 'e.tmp', 'f.map', 'g.tsbuildinfo',
  '.test-tmp/x.jsonl', '.test-tmp/y.txt',
  'arch.zip', 'arch.tgz', 'arch.tar.gz', 'ARCH.ZIP',
  'src/main.ts', 'README.md', 'src/foo/bar/baz.go',
  'DEEP/nested/path/file.ts',
]
out.classifyPath = classifyInputs.map(input => {
  const v = classifyPath(input)
  return { input, tier: v.tier, silent: v.silent, reason: v.reason }
})

// ── scan-excludes ──
out.scanExcludes = [...SCAN_EXCLUDE_DIRS].sort().map(name => ({ name, excluded: isScanExcludedDir(name) }))
out.scanExcludesProbe = ['node_modules', '.git', 'dist', '.next', 'build', 'target', '__pycache__', 'TianshuData', '.rivet', 'src', 'coverage']
  .map(name => ({ name, excluded: isScanExcludedDir(name) }))

// ── repo_map（造一棵受控目录树）──
const root = join(FIXTURES, 'repomap')
rmSync(root, { recursive: true, force: true })
mkdirSync(root, { recursive: true })

function mk(rel: string, content = 'x') {
  const p = join(root, rel)
  mkdirSync(join(p, '..'), { recursive: true })
  writeFileSync(p, content)
}

// 入口/配置/测试/文档 标注用例
mk('src/main.ts')
mk('src/index.tsx')
mk('src/app.tsx')
mk('src/agent/loop.ts')
mk('src/agent/__tests__/loop.test.ts')
mk('src/agent/helper.spec.ts')
mk('tsconfig.json')
mk('package.json')
mk('vite.config.ts')
mk('README.md')
mk('docs/guide.md')
mk('a.ts')
mk('z.ts')
mk('m.ts')
// 排除目录（应被剪掉）
mk('node_modules/pkg/index.js')
mk('dist/out.js')
mk('.git/config')
mk('target/debug/app')
// 点开头（默认跳过，除 .gitignore/.env.example）
mk('.gitignore')
mk('.env.example')
mk('.hidden/secret.ts')
// silent（应跳过）
mk('x.log')
mk('.rivet/sessions/s.jsonl')
// 空目录（不应出现在树中）
mkdirSync(join(root, 'emptydir'), { recursive: true })

const repoMapCases: Array<[string, any]> = [
  ['default', {}],
  ['depth1', { depth: 1 }],
  ['depth2', { depth: 2 }],
  ['depth0', { depth: 0 }],
  ['maxFiles3', { max_files: 3 }],
  ['maxFiles0', { max_files: 0 }],
  ['path-src', { path: 'src' }],
  ['path-agent', { path: 'src/agent' }],
  ['path-nonexistent', { path: 'nope' }],
  ['path-escape', { path: '../outside' }],
  ['path-hidden', { path: '.hidden' }],
]
out.repoMap = await Promise.all(repoMapCases.map(async ([name, input]) => {
  const r = await REPO_MAP_TOOL.execute({ cwd: root, input } as any)
  return { name, input, content: r.content, isError: r.isError ?? false }
}))

// ── inspect_project（**独立的 Node 项目夹具**，不复用 repomap 树）──
//
// **为什么独立**：repomap 树里有 `docs/guide.md`、`.hidden/` 等会干扰
// inspect 的输出（测试文件探测会走进整棵树）。用独立夹具才能让 Go 侧
// 用**同样的建树逻辑**复现。
const inspectRoot = join(FIXTURES, 'inspect')
rmSync(inspectRoot, { recursive: true, force: true })
mkdirSync(join(inspectRoot, 'src'), { recursive: true })
writeFileSync(join(inspectRoot, 'package.json'), JSON.stringify({
  name: 'oracle-fixture',
  scripts: { build: 'tsc', test: 'node --test', lint: 'eslint .', dev: 'tsx watch', start: 'node .', typecheck: 'tsc --noEmit', other: 'echo x' },
  dependencies: { react: '^18', express: '^4' },
  devDependencies: { typescript: '^5', vitest: '^1', eslint: '^8' },
}, null, 2))
writeFileSync(join(inspectRoot, 'pnpm-lock.yaml'), '')
writeFileSync(join(inspectRoot, 'src', 'index.ts'), '')
out.inspectProject = await (async () => {
  const r = await INSPECT_PROJECT_TOOL.execute({ cwd: inspectRoot, input: {} } as any)
  return { content: r.content, isError: r.isError ?? false }
})()

// 非 Node 项目
const nonNode = join(FIXTURES, 'nonnode')
rmSync(nonNode, { recursive: true, force: true })
mkdirSync(nonNode, { recursive: true })
out.inspectProjectNonNode = await (async () => {
  const r = await INSPECT_PROJECT_TOOL.execute({ cwd: nonNode, input: {} } as any)
  return { content: r.content, isError: r.isError ?? false }
})()

// ── file_info ──
const fiDir = join(FIXTURES, 'fileinfo')
rmSync(fiDir, { recursive: true, force: true })
mkdirSync(fiDir, { recursive: true })
writeFileSync(join(fiDir, 'a.ts'), 'const a = 1\n')
writeFileSync(join(fiDir, 'b.png'), Buffer.from([0x89, 0x50, 0x4e, 0x47]))
writeFileSync(join(fiDir, 'empty.txt'), '')
writeFileSync(join(fiDir, 'big.bin'), Buffer.alloc(2048))
mkdirSync(join(fiDir, 'sub'), { recursive: true })
writeFileSync(join(fiDir, 'sub', 'x.ts'), 'x')
writeFileSync(join(fiDir, 'sub', 'y.log'), 'y')
mkdirSync(join(fiDir, 'sub', 'node_modules'), { recursive: true })
writeFileSync(join(fiDir, 'sub', 'node_modules', 'p.js'), 'p')
mkdirSync(join(fiDir, 'sub', '.hidden'), { recursive: true })
writeFileSync(join(fiDir, 'sub', '.hidden', 'h.ts'), 'h')

const fileInfoCases: Array<[string, string]> = [
  ['file-ts', 'a.ts'],
  ['file-binary', 'b.png'],
  ['file-empty', 'empty.txt'],
  ['file-2kb', 'big.bin'],
  ['dir-root', '.'],
  ['dir-sub', 'sub'],
  ['missing', 'nope.ts'],
]
out.fileInfo = await Promise.all(fileInfoCases.map(async ([name, path]) => {
  const r = await FILE_INFO_TOOL.execute({ cwd: fiDir, input: { path } } as any)
  return { name, path, content: r.content, uiContent: r.uiContent ?? null, isError: r.isError ?? false }
}))

out.fileInfoNoPath = await (async () => {
  const r = await FILE_INFO_TOOL.execute({ cwd: fiDir, input: {} } as any)
  return { content: r.content, isError: r.isError ?? false }
})()

// ── formatPermissions（平台分叉，两平台都测）──
out.formatPermissions = [
  { mode: 0o755, platform: 'linux', out: formatPermissions(0o755, 'linux' as any) },
  { mode: 0o644, platform: 'linux', out: formatPermissions(0o644, 'linux' as any) },
  { mode: 0o600, platform: 'linux', out: formatPermissions(0o600, 'linux' as any) },
  { mode: 0o755, platform: 'win32', out: formatPermissions(0o755, 'win32' as any) },
  { mode: 0o444, platform: 'win32', out: formatPermissions(0o444, 'win32' as any) },
  { mode: 0o200, platform: 'win32', out: formatPermissions(0o200, 'win32' as any) },
]

// **不输出 fixtureRoot / fixtureFileInfoDir**——它们是绝对路径，会让
// oracle.json 跨机器不可复现（且测试并不使用它们：工具输出全是相对路径）。
// 若将来需要，输出相对路径而非绝对。

process.stdout.write(JSON.stringify(out, null, 2))
