/**
 * related_tests 差分 oracle 生成器。
 *
 * 真跑 TS 的 RELATED_TESTS_TOOL（静态变体，无 Meridian——正是 Go 该实现的对应物），
 * 产出黄金数据供 Go 侧逐例对账。
 *
 * 运行：node_modules/.bin/tsx go/testdata/relatedtests/gen-oracle.ts > oracle.json
 */
import { mkdirSync, writeFileSync, rmSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { RELATED_TESTS_TOOL } from '../../../src/tools/related-tests.js'

const ROOT = 'go/testdata/relatedtests/fixtures'

// ── fixture 布局（Go 测试读同一份 JSON 重建，保证两侧一致）──
const FILES: string[] = [
  // TS 项目布局
  'src/tools/bash.ts',
  'src/tools/__tests__/bash.test.ts',
  'src/tools/__tests__/bash.spec.ts',
  'src/api/client.ts',
  'src/api/__tests__/client.test.ts',
  'src/lone.ts', // 无任何测试
  'lib/util.ts',
  'lib/util.test.ts',
  '__tests__/tools/bash.test.ts', // 顶层 __tests__ 镜像 src 路径
  'tests/api/client.spec.ts', // 顶层 tests 镜像 src 路径
  // Python 布局
  'pkg/mod.py',
  'pkg/tests/test_mod.py', // 同级 tests/
  'testdir/other.py',
  'testdir/other_test.py', // _test.py 后缀
  'flat.py',
  'tests/test_flat.py', // 顶层 tests/ 平铺
  'src/deep.py',
  'tests/deep/test_deep.py', // 顶层 tests/ 镜像路径
  'src/pypkg/inner.py',
  'src/pypkg/tests/test_inner.py',
]

// ── 用例：源文件 → 测试 ──
const SOURCE_CASES: Array<[string, string]> = [
  ['src-tools-bash', 'src/tools/bash.ts'],
  ['src-api-client', 'src/api/client.ts'],
  ['src-lone-notfound', 'src/lone.ts'],
  ['lib-util-colocated', 'lib/util.ts'],
  ['py-pkg-mod', 'pkg/mod.py'],
  ['py-testdir-other', 'testdir/other.py'],
  ['py-flat-top', 'flat.py'],
  ['py-src-deep', 'src/deep.py'],
  ['py-src-pypkg-inner', 'src/pypkg/inner.py'],
  // 反斜杠输入（Windows 风格）——探测 startsWith('src/') 分支的差异
  ['win-backslash-bash', 'src\\tools\\bash.ts'],
  ['win-backslash-client', 'src\\api\\client.ts'],
]

// ── 用例：测试文件 → 源文件 ──
const TEST_CASES: Array<[string, string]> = [
  ['rev-colocated-test', 'src/tools/__tests__/bash.test.ts'],
  ['rev-colocated-spec', 'src/tools/__tests__/bash.spec.ts'],
  ['rev-lib-colocated', 'lib/util.test.ts'],
  ['rev-top-tests-mirror', '__tests__/tools/bash.test.ts'],
  ['rev-tests-dir-mirror', 'tests/api/client.spec.ts'],
  ['rev-py-sibling-tests', 'pkg/tests/test_mod.py'],
  ['rev-py-underscore', 'testdir/other_test.py'],
  ['rev-py-top-flat', 'tests/test_flat.py'],
  ['rev-py-top-mirror', 'tests/deep/test_deep.py'],
  ['rev-nosource', 'src/orphan/__tests__/nope.test.ts'],
]

// ── 用例：错误分支 ──
const ERROR_CASES: Array<[string, string]> = [
  ['err-absolute-win', 'C:\\x\\y.ts'],
  ['err-absolute-fwd', 'C:/x/y.ts'],
  ['err-dotdot', '../x.ts'],
  ['err-dotdot-mid', 'a..b.ts'],
]

async function run(cwd: string, input: Record<string, unknown>) {
  const r = await RELATED_TESTS_TOOL.execute({ cwd, input, toolUseId: 't' } as any)
  return { content: r.content, isError: r.isError ?? false }
}

function writeFixture(root: string) {
  for (const f of FILES) {
    const abs = join(root, f)
    mkdirSync(dirname(abs), { recursive: true })
    writeFileSync(abs, `// ${f}\n`)
  }
}

const out: any = { files: FILES, source: [], test: [], error: [] }

async function main() {
  rmSync(ROOT, { recursive: true, force: true })
  const cwd = join(process.cwd(), ROOT)
  mkdirSync(cwd, { recursive: true })
  writeFixture(cwd)

  for (const [name, file] of SOURCE_CASES) {
    const r = await run(cwd, { file })
    out.source.push({ name, file, ...r })
  }
  for (const [name, file] of TEST_CASES) {
    const r = await run(cwd, { file })
    out.test.push({ name, file, ...r })
  }
  for (const [name, file] of ERROR_CASES) {
    const r = await run(cwd, { file })
    out.error.push({ name, file, ...r })
  }

  process.stdout.write(JSON.stringify(out, null, 2) + '\n')
}

main().catch((e) => { console.error(e); process.exit(1) })
