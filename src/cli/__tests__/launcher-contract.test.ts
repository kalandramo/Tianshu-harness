/**
 * CLI launcher 契约测试（P0-1/P0-2）。
 *
 * 两条链必须被锁住，否则优化会在重构中静默退化：
 *   ① 运行期时序：launcher 必须先 enableCompileCache、再处理 --help/--version，
 *      最后才动态 import main（ESM 静态 import 会抢在模块体之前执行，时序即收益）。
 *   ② 发布接线：package.json bin 指向 launcher、tsup 产出 launcher、main.ts 与
 *      launcher 共用同一份 help/路由事实源。
 * 另覆盖 --version 的 install root 解析（跳过无 version 的 dist/package.json stub）。
 */
import { describe, test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { formatVersionLine, findInstallRoot, readInstallVersion } from '../version.js'

const REPO_ROOT = new URL('../../../', import.meta.url)

function readRepoFile(rel: string): string {
  return readFileSync(new URL(rel, REPO_ROOT), 'utf-8')
}

describe('launcher 运行期时序契约', () => {
  test('entry.ts 在动态 import main 之前启用编译缓存并处理 help/version', () => {
    const source = readRepoFile('src/cli/entry.ts')
    const cacheIdx = source.indexOf('enableCompileCache(')
    const helpIdx = source.indexOf("args.includes('--help')")
    const mainIdx = source.indexOf("await import('../main.js')")

    assert.notEqual(cacheIdx, -1, 'entry.ts 必须调用 enableCompileCache')
    assert.ok(
      source.includes('process.env.NODE_COMPILE_CACHE'),
      'entry.ts 必须尊重父进程已注入的 NODE_COMPILE_CACHE（桌面 sidecar 走这条）',
    )
    assert.notEqual(helpIdx, -1, 'entry.ts 必须处理 --help 快速路径')
    assert.notEqual(mainIdx, -1, 'entry.ts 必须动态 import main')
    assert.ok(cacheIdx < mainIdx, 'enableCompileCache 必须在动态 import main 之前')
    assert.ok(helpIdx < mainIdx, '--help 快速路径必须在动态 import main 之前')
  })

  test('main.ts 与 launcher 共用 HELP_TEXT / formatVersionLine / routeEarlyCli', () => {
    const source = readRepoFile('src/main.ts')
    assert.ok(source.includes('process.stdout.write(HELP_TEXT)'), 'main.ts 必须用共享 HELP_TEXT')
    assert.ok(source.includes('process.stdout.write(formatVersionLine())'), 'main.ts 必须用共享版本输出')
    assert.ok(source.includes('routeEarlyCli(args)'), 'main.ts 必须调用共享早期路由')
    assert.ok(source.includes('await applyEarlyCliEnv(args)'), 'main.ts 必须调用共享早期 env 副作用')
  })
})

describe('发布接线契约', () => {
  test('bin 指向 launcher，tsup entry 产出该 launcher', () => {
    const pkg = JSON.parse(readRepoFile('package.json')) as { bin?: Record<string, string> }
    assert.equal(pkg.bin?.rivet, 'dist/cli/entry.js')
    assert.ok(readRepoFile('tsup.config.ts').includes("'src/cli/entry.ts'"), 'tsup entry 必须包含 src/cli/entry.ts')
  })
})

describe('--version install root 解析', () => {
  test('跳过无 version 的 dist/package.json，继续向上拿真实版本', () => {
    const root = mkdtempSync(join(tmpdir(), 'rivet-version-'))
    try {
      writeFileSync(join(root, 'package.json'), JSON.stringify({ name: 'tianshu-harness', version: '9.9.9' }))
      mkdirSync(join(root, 'dist', 'cli'), { recursive: true })
      writeFileSync(join(root, 'dist', 'package.json'), JSON.stringify({ type: 'module' }))
      const script = join(root, 'dist', 'cli', 'entry.js')
      writeFileSync(script, '')

      assert.equal(findInstallRoot(script), realpathSync(root))
      assert.equal(readInstallVersion(findInstallRoot(script)!), '9.9.9')
      assert.equal(formatVersionLine(script), 'tianshu-harness v9.9.9\n')
    } finally {
      rmSync(root, { recursive: true, force: true })
    }
  })

  test('找不到版本时输出 unknown（形状保持 tianshu-harness v…）', () => {
    const root = mkdtempSync(join(tmpdir(), 'rivet-version-none-'))
    try {
      const script = join(root, 'nested', 'entry.js')
      mkdirSync(join(root, 'nested'), { recursive: true })
      writeFileSync(script, '')
      // 祖先目录没有 package.json（tmpdir 直下建；即使有，也不会带 version 字段）
      assert.equal(findInstallRoot(script), null)
      assert.equal(formatVersionLine(script), 'tianshu-harness vunknown\n')
    } finally {
      rmSync(root, { recursive: true, force: true })
    }
  })

  test('改名过渡期：旧名 tianshu-tui 的安装目录仍被识别（版本可读、行首用新名）', () => {
    const root = mkdtempSync(join(tmpdir(), 'rivet-version-legacy-'))
    try {
      writeFileSync(join(root, 'package.json'), JSON.stringify({ name: 'tianshu-tui', version: '3.22.0' }))
      mkdirSync(join(root, 'dist', 'cli'), { recursive: true })
      const script = join(root, 'dist', 'cli', 'entry.js')
      writeFileSync(script, '')

      assert.equal(findInstallRoot(script), realpathSync(root), '旧名安装的声明必须仍被认作本包')
      assert.equal(readInstallVersion(findInstallRoot(script)!), '3.22.0')
      assert.equal(formatVersionLine(script), 'tianshu-harness v3.22.0\n')
    } finally {
      rmSync(root, { recursive: true, force: true })
    }
  })
})
