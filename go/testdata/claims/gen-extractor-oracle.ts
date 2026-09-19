// 端到端对账：真实 oracle（src/context/claim-extractor.ts）vs Go 实现。
//
// **注意**：`extractClaimsFromToolResult` 内部用 `Date.now()`——输出含时间戳，
// 不可复现。生成器须 **scrub 时间字段**（createdAt / expiresAt）后再写盘，
// 或用固定时间基准比对。
//
// 运行（必须在**仓库根**）：
//   node_modules/.bin/tsx go/testdata/claims/gen-extractor-oracle.ts
import { writeFileSync } from 'node:fs'
import { extractClaimsFromToolResult, type ToolResultContext, type ClaimExtractionMeta } from '../../../src/context/claim-extractor.js'

const meta: ClaimExtractionMeta = { sessionId: 'sess-x', turn: 3, eventId: 'ev-42' }

function ctx(toolName: string, input: Record<string, unknown>, result: string, isError = false): ToolResultContext {
  return { toolName, input, result, isError }
}

interface Case {
  ctx: ToolResultContext
  /** 已存在的 file_observation 路径集（去重） */
  existing?: string[]
}

const cases: Record<string, Case> = {}

// ── 跳过：SKIP_TOOLS ──
for (const t of ['grep', 'glob', 'diff', 'inspect_project', 'repo_map', 'related_tests', 'recall']) {
  cases[`skip_${t}`] = { ctx: ctx(t, {}, 'a'.repeat(100)) }
}

// ── 跳过：结果太短 ──
cases.skip_short_result = { ctx: ctx('read_file', { file_path: 'a.ts' }, 'short') }

// ── read_file → file_observation（有导出符号）──
cases.read_with_symbols = {
  ctx: ctx('read_file', { file_path: 'src/a.ts' },
    'export function foo() {}\nexport const bar = 1;\nexport class Baz {}\n'),
}

// ── read_file → file_observation（无导出符号，用 Read 兜底）──
cases.read_no_symbols = {
  ctx: ctx('read_file', { file_path: 'src/b.ts' },
    'const x = 1;\nfunction internal() {}\n// 一堆普通代码\n'),
}

// ── read_file 出错 → 不提取 ──
cases.read_error = {
  ctx: ctx('read_file', { file_path: 'src/c.ts' }, '错误信息'.repeat(20), true),
}

// ── read_file 已存在观察 → 去重跳过 ──
cases.read_existing_dedup = {
  ctx: ctx('read_file', { file_path: 'src/dedup.ts' }, 'export const x = 1;\n'.repeat(5)),
  existing: ['src/dedup.ts'],
}

// ── read_file 符号上限 10 ──
cases.read_many_symbols = {
  ctx: ctx('read_file', { file_path: 'src/many.ts' },
    Array.from({ length: 15 }, (_, i) => `export const s${i} = ${i};`).join('\n') + '\n'),
}

// ── read_file：export { a, b as c } 形式 ──
cases.read_export_list = {
  ctx: ctx('read_file', { file_path: 'src/list.ts' },
    'const a = 1, b = 2;\nexport { a, b as cc };\n// padding padding padding\n'),
}

// ── run_tests 通过 → verification_fact ──
cases.tests_pass = {
  ctx: ctx('run_tests', {}, 'Tests: 12 passed, 0 failed\nAll good\n'),
}

// ── run_tests 失败 → failure_pattern ──
cases.tests_fail = {
  ctx: ctx('run_tests', {}, 'FAIL: 3 tests failed\n  expected 1 got 2\n', true),
}

// ── run_tests 通过但无 pass 数字 → 不提取 ──
cases.tests_pass_no_number = {
  ctx: ctx('run_tests', {}, 'ok, everything works fine here\n'),
}

// ── bash 测试命令（正则匹配）──
cases.bash_test_cmd = {
  ctx: ctx('bash', { command: 'npx jest --silent' }, 'Tests: 5 passing in 1.2s\n'),
}

// ── bash 非测试命令 → 不提取 ──
cases.bash_non_test = {
  ctx: ctx('bash', { command: 'ls -la' }, 'total 42\ndrwxr-xr-x ...\n'),
}

// ── bash 安全扫描失败 → security_finding ──
cases.bash_security = {
  ctx: ctx('bash', { command: 'npm audit' }, 'found 3 vulnerabilities (CVE-2024-1234)\n', true),
}

// ── git commit（显著：feat）──
cases.git_commit_feat = {
  ctx: ctx('git', { action: 'commit', message: 'feat: add thing' },
    '[main abc1234] feat: add thing\n 3 files changed, 10 insertions(+)\n src/a.ts | 5 +\n src/b.ts | 3 +\n src/c.ts | 2 +\n'),
}

// ── git commit（不显著：普通 message + 少文件）──
cases.git_commit_routine = {
  ctx: ctx('git', { action: 'commit', message: 'update readme' },
    '[main def5678] update readme\n 1 file changed, 1 insertion(+)\n README.md | 1 +\n'),
}

// ── git commit（fix 关键词 → 显著）──
cases.git_commit_fix = {
  ctx: ctx('git', { action: 'commit', message: 'fix: crash on empty input' },
    '[main 111aaaa] fix: crash\n 1 file changed\n'),
}

// ── git commit 带 HEAD 回读格式 ──
cases.git_commit_show_format = {
  ctx: ctx('git', { action: 'commit', message: 'feat: x' },
    'abc9999 (HEAD -> main) feat: x\n 4 files changed\n a | 1\n b | 2\n c | 3\n d | 4\n'),
}

// ── deliver_task commit=true（显著）──
cases.deliver_commit = {
  ctx: ctx('deliver_task', { commit: true, message: 'feat: big change' },
    '[main fedcba9] feat: big change\n 3 files changed\n x | 1\n y | 2\n z | 3\n'),
}

// ── deliver_task 非 commit → 不提取 ──
cases.deliver_no_commit = {
  ctx: ctx('deliver_task', { commit: false, message: 'feat: x' }, '[main aaa1111] feat: x\n'),
}

// ── 不可识别的工具 → 不提取 ──
cases.unknown_tool = {
  ctx: ctx('some_other_tool', {}, 'a'.repeat(50)),
}

// ── 提交体含其他 hash（不应误取）──
cases.commit_body_other_hash = {
  ctx: ctx('git', { action: 'commit', message: 'feat: revert' },
    '[main real123] feat: revert\n reverts deadbeef1234 (previous)\n 3 files changed\n a | 1\n b | 2\n c | 3\n'),
}

interface OracleEntry {
  proposals: unknown[]
}

const out: Record<string, OracleEntry> = {}
for (const [name, c] of Object.entries(cases)) {
  const existing = c.existing ? new Set(c.existing) : undefined
  const proposals = extractClaimsFromToolResult(c.ctx, meta, existing)
  // scrub 时间字段（不可复现）
  const scrubbed = proposals.map(p => {
    const { createdAt, expiresAt, evidence, ...rest } = p as never as Record<string, unknown> & {
      createdAt: number; expiresAt?: number; evidence: Array<Record<string, unknown>>
    }
    return {
      ...rest,
      // 时间用相对值表示：expiresAt - createdAt（TTL），Infinity 保持 Infinity
      ttl: expiresAt === undefined ? null : (expiresAt === Infinity ? 'Infinity' : expiresAt - createdAt),
      evidence: evidence.map(e => {
        const { createdAt: _c, ...er } = e
        return er
      }),
    }
  })
  out[name] = { proposals: scrubbed }
}

writeFileSync(new URL('extractor-oracle.json', import.meta.url), JSON.stringify(out, null, 2) + '\n')
writeFileSync(new URL('extractor-cases.json', import.meta.url), JSON.stringify(cases, null, 2) + '\n')
