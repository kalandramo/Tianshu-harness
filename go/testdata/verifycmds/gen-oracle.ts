/**
 * renderDeclaredVerify oracle 生成器。
 *
 * 生成：
 *   npx tsx go/testdata/verifycmds/gen-oracle.ts
 *
 * 覆盖 src/prompt/volatile.ts 的 renderDeclaredVerify。
 *
 * ## 走真实路径（不手抄）
 *
 * renderDeclaredVerify 未导出，但可通过 buildStableVolatileBlock + 真实
 * fixture 目录取到它的输出。两个关键前提（已探针验证）：
 *   1. `RIVET_TRUST_PROJECT=1` 绕过信任门（否则未授信项目返回空声明）
 *   2. 临时目录 fixture **不含路径进块**——块内容只由 config 内容决定，
 *      故 golden 可复现（两次生成字节相同）
 *
 * 为什么不用 tmpdir 之外的固定目录：tmpdir 无上层 `.rivet-config.json`，
 * 避免 findProjectConfig 向上 20 层查找时继承仓库自身的配置。
 *
 * ## 拆分说明
 *
 * TS 的 renderDeclaredVerify 是「读 config + 渲染」一体。读取部分
 * （findProjectConfig 向上查找 + 信任门 + zod 校验）依赖仓库状态与全局
 * trust store。Go 侧拆成两层：LoadDeclaredVerify（读取）+ RenderDeclaredVerify
 * （渲染，本 oracle 对账）。生成器通过真实读取路径产出 golden，故两者都被覆盖。
 */
import { writeFileSync, mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import { createHash } from 'node:crypto'

// 必须在 import volatile.js **之前**设（loadDeclaredVerify 读它做信任判定）
process.env['RIVET_TRUST_PROJECT'] = '1'

const here = dirname(fileURLToPath(import.meta.url))

const { buildStableVolatileBlock } = await import('../../../src/prompt/volatile.js')

/** 用 fixture 目录的真实读取路径取 <verify-commands> 块（或 null）。 */
function renderViaRealPath(verify: unknown): string | null {
  const dir = mkdtempSync(join(tmpdir(), 'verifycmds-'))
  try {
    writeFileSync(join(dir, '.rivet-config.json'), JSON.stringify({ verify }))
    const out = buildStableVolatileBlock({ cwd: dir } as never)
    const m = out.match(/<verify-commands[\s\S]*?<\/verify-commands>/)
    return m ? m[0] : null
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
}

type Case = { name: string; note?: string; verify: unknown }

const cases: Case[] = [
  { name: 'empty', note: '空 config → null（无声明）', verify: {} },
  { name: 'testOnly', note: '只有 test', verify: { test: 'npm test' } },
  {
    name: 'allFour',
    note: '四种 kind 齐上——顺序固定 test→build→typecheck→lint',
    verify: { test: 'npm test', build: 'npm run build', typecheck: 'tsc --noEmit', lint: 'eslint .' },
  },
  {
    name: 'orderIndependent',
    note: '**顺序区分点**：config 里 key 的书写顺序不影响输出（过滤按固定数组）',
    verify: { lint: 'L', typecheck: 'T', build: 'B', test: 'X' },
  },
  { name: 'blankStrings', note: '空白串应被过滤（trim 后为空）', verify: { test: '   ', build: '\t', typecheck: 'tsc' } },
  { name: 'whitespaceTrimmed', note: '前后空白应被 trim', verify: { test: '  npm test  ', build: '\nmake\n' } },
  { name: 'escaping', note: '含需转义字符（< > & "）', verify: { test: 'echo "<x>" && true', build: 'a & b' } },
  {
    name: 'routesOnly',
    note: '只有 routes（无四种 kind）',
    verify: { routes: [{ match: 'desktop/**', run: 'tsc -p desktop', kind: 'typecheck' }] },
  },
  {
    name: 'routesWithKinds',
    note: 'kind 与 routes 并存——routes 追加在四种之后',
    verify: {
      test: 'npm test',
      routes: [
        { match: 'src/**', run: 'eslint', kind: 'lint' },
        { match: 'go/**', run: 'go test', kind: 'test' },
      ],
    },
  },
  { name: 'emptyRoutes', note: 'routes 为空数组 → 不追加', verify: { test: 'npm test', routes: [] } },
  {
    name: 'routeEscaping',
    note: 'route 的 match/run 含需转义字符',
    verify: { routes: [{ match: 'a<b>&"c"', run: 'x && y', kind: 'build' }] },
  },
  {
    name: 'fourKindsWithRoutes',
    note: '四种 kind + 多个 routes——完整组合',
    verify: {
      test: 'npm test',
      build: 'npm run build',
      typecheck: 'tsc --noEmit',
      lint: 'eslint .',
      routes: [
        { match: 'desktop/**', run: 'tsc -p desktop', kind: 'typecheck' },
        { match: '**/*.go', run: 'go test ./...', kind: 'test' },
      ],
    },
  },
]

const results: Record<string, { verify: unknown; out: string | null; note?: string }> = {}
for (const c of cases) {
  results[c.name] = { verify: c.verify, out: renderViaRealPath(c.verify), note: c.note }
}

const out = { cases: results }
mkdirSync(here, { recursive: true })
writeFileSync(join(here, 'oracle.json'), JSON.stringify(out, null, 2) + '\n')

const sha = createHash('sha256').update(JSON.stringify(out)).digest('hex')
console.error(`verifycmds oracle：${cases.length} 用例 — sha256 ${sha.slice(0, 16)}`)
