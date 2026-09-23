/**
 * skill 差分 oracle 生成器。
 *
 * 生成：npx tsx go/testdata/skills/gen-oracle.ts
 *
 * ## 为什么需要
 *
 * Go 侧 `internal/skills` 要逐字复刻 TS 的：
 *   - `BUILTIN_SKILLS` 的 name / description / triggers / body
 *   - `RETIRED_BUNDLED_SKILLS` 表
 *   - `parseSkillMarkdown` 的 frontmatter 解析（含块标量、数组、CRLF/BOM）
 *   - `renderDiscoveryBlock` 的渲染（含预算溢出、relevant 排序）
 *   - `listSkillFiles` 的目录树（含正斜杠归一化）
 *
 * 手抄会漂移——故从**真实模块**导出。
 */
import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createHash } from 'node:crypto'
import {
  BUILTIN_SKILLS,
  RETIRED_BUNDLED_SKILLS,
  parseSkillMarkdown,
  listSkillFiles,
  SkillRegistry,
} from '../../../src/skills/skill-loader.js'

const here = dirname(fileURLToPath(import.meta.url))

const out: Record<string, unknown> = {}

// ── 内置技能（逐字）──
out.builtin = BUILTIN_SKILLS.map((s) => ({
  name: s.name,
  description: s.description,
  triggers: s.triggers.map((r) => r.source),
  triggerFlags: s.triggers.map((r) => r.flags),
  body: s.body,
  builtIn: s.builtIn ?? false,
}))

// ── 退役表 ──
out.retired = RETIRED_BUNDLED_SKILLS.map((e) => ({ name: e.name, sha256: e.sha256 }))

// ── parseSkillMarkdown 差分用例 ──
const parseCases: Array<{ label: string; content: string; fileName: string }> = [
  {
    label: 'basic',
    fileName: 'foo.md',
    content: '---\nname: foo\ndescription: A foo skill\ntriggers: ["bar", "baz"]\n---\nDo the thing.\n',
  },
  {
    label: 'no-name-falls-back-to-file',
    fileName: 'fallback.md',
    content: '---\ndescription: no name here\n---\nBody text.\n',
  },
  {
    label: 'single-trigger-string',
    fileName: 'single.md',
    content: '---\nname: single\ntrigger: "only-one"\n---\nBody.\n',
  },
  {
    label: 'block-scalar-pipe',
    fileName: 'pipe.md',
    content: '---\nname: pipe\ndescription: |\n  line one\n  line two\n---\nBody.\n',
  },
  {
    label: 'block-scalar-folded',
    fileName: 'folded.md',
    content: '---\nname: folded\ndescription: >\n  folded one\n  folded two\n---\nBody.\n',
  },
  {
    label: 'bom-and-crlf',
    fileName: 'bom.md',
    content: '\uFEFF---\r\nname: bom\r\ndescription: from windows\r\n---\r\nBody with CRLF.\r\n',
  },
  {
    label: 'tier-lock-valid',
    fileName: 'tier.md',
    content: '---\nname: tier\ntierLock: strong\n---\nBody.\n',
  },
  {
    label: 'tier-lock-invalid',
    fileName: 'badtier.md',
    content: '---\nname: badtier\ntierLock: nope\n---\nBody.\n',
  },
  {
    label: 'body-trimmed',
    fileName: 'trim.md',
    content: '---\nname: trim\n---\n\n\n   Padded body   \n\n\n',
  },
  {
    label: 'array-bracket-unquoted',
    fileName: 'arr.md',
    content: '---\nname: arr\ntriggers: [alpha, beta]\n---\nBody.\n',
  },
]

const parsed: Record<string, unknown> = {}
const parseErrors: Record<string, string> = {}
for (const c of parseCases) {
  try {
    const d = parseSkillMarkdown(c.content, c.fileName)
    parsed[c.label] = {
      name: d.name,
      description: d.description,
      triggers: d.triggers.map((r) => r.source),
      body: d.body,
      tierLock: d.tierLock ?? null,
    }
  } catch (e) {
    parseErrors[c.label] = e instanceof Error ? e.message : String(e)
  }
}
out.parsed = parsed
out.parseErrors = parseErrors

// ── renderDiscoveryBlock 差分用例 ──
function mkRegistry(): SkillRegistry {
  const r = new SkillRegistry()
  // **夹具设计**：相关的 skill 必须**字母序靠后**（zeta 而非 alpha）——
  // 否则「relevant 优先排序」与「纯字母序」产出相同结果，排序变异逃逸。
  r.register({ name: 'alpha', description: 'first alphabetically', triggers: [], body: 'A' })
  r.register({ name: 'mid', description: 'middle one', triggers: [/other/], body: 'M' })
  r.register({ name: 'zeta', description: 'last alphabetically', triggers: [/match-me/i], body: 'Z' })
  return r
}

const discovery: Record<string, unknown> = {}
{
  const r = mkRegistry()
  discovery.noHint = r.renderDiscoveryBlock()
  discovery.withHintMatch = r.renderDiscoveryBlock('please match-me now')
  discovery.withHintNoMatch = r.renderDiscoveryBlock('nothing relevant')
  discovery.emptyRegistry = new SkillRegistry().renderDiscoveryBlock()
  discovery.exclude = r.renderDiscoveryBlock(undefined, { exclude: new Set(['mid']) })
  discovery.tinyBudget = r.renderDiscoveryBlock(undefined, { maxChars: 60 })
  discovery.shortDesc = r.renderDiscoveryBlock(undefined, { maxDescChars: 5 })
  discovery.whitespaceDesc = (() => {
    const rr = new SkillRegistry()
    rr.register({ name: 'ws', description: 'multi\n  line\t\tdesc', triggers: [], body: 'W' })
    return rr.renderDiscoveryBlock()
  })()
}
out.discovery = discovery

// ── listSkillFiles 差分（目录树形状，用固定夹具）──
// 夹具在 gen 时创建，故结果可复现。
{
  const fs = await import('node:fs')
  const fixRoot = join(here, 'fixtures', 'dirskill')
  fs.rmSync(fixRoot, { recursive: true, force: true })
  fs.mkdirSync(join(fixRoot, 'references'), { recursive: true })
  fs.mkdirSync(join(fixRoot, 'scripts'), { recursive: true })
  fs.writeFileSync(join(fixRoot, 'SKILL.md'), 'router', 'utf-8')
  fs.writeFileSync(join(fixRoot, 'references', 'api.md'), 'api', 'utf-8')
  fs.writeFileSync(join(fixRoot, 'scripts', 'run.sh'), 'sh', 'utf-8')
  fs.writeFileSync(join(fixRoot, 'top.md'), 'top', 'utf-8')
  out.skillFiles = listSkillFiles(fixRoot)
}

mkdirSync(here, { recursive: true })
writeFileSync(join(here, 'oracle.json'), JSON.stringify(out, null, 2) + '\n')
const sha = createHash('sha256').update(JSON.stringify(out)).digest('hex')
console.error(`skills oracle：builtin=${(out.builtin as unknown[]).length} parse=${Object.keys(parsed).length} — sha256 ${sha.slice(0, 16)}`)
