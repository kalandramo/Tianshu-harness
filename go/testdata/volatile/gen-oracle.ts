/**
 * buildStableVolatileBlock oracle 生成器。
 *
 * 生成：
 *   npx tsx go/testdata/volatile/gen-oracle.ts
 *
 * 覆盖 src/prompt/volatile.ts 的 buildStableVolatileBlock —— frozen 前缀的
 * 主体。这是缓存命中的核心：它的字节稳定性直接决定 system prompt 之后
 * 整条前缀能否复用。
 *
 * ## 范围与取舍
 *
 * 它消费 11 个 ctx 字段，分三类：
 *   - 纯 ctx 驱动（可对账）：projectInstructions / projectMemory /
 *     knowledgeManifest / seedCapsule / codebaseIndex / workingSet /
 *     sessionMemory / star-domain / sober / locus
 *   - 宿主相关（需参数化）：`<environment platform=... os=...>` 一行
 *   - IO 探测（**本轮不对账**）：detectRuntimeEnvBlock（spawn python3/node）、
 *     renderDeclaredVerify（读 .rivet-config.json）——它们的字节取决于
 *     宿主环境与项目文件，oracle 用例里通过"不提供触发条件"排除
 *
 * ## 纪律
 *
 * 调用**真实** buildStableVolatileBlock，不手抄期望值。
 * 每个用例的 ctx 都显式设 `blockCaps`，避免依赖宿主默认值。
 */
import { writeFileSync, mkdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import { createHash } from 'node:crypto'
import { buildStableVolatileBlock, type VolatileContext } from '../../../src/prompt/volatile.js'

const here = dirname(fileURLToPath(import.meta.url))

// 宿主环境是输出的一部分（<environment> 行）。为让 golden 可跨机器复现，
// 用固定 cwd 且**记录**宿主 platform/os 到 golden——Go 侧对账时用同一组值。
// 注意：os.type()/os.release() 在 Go 侧需按 Node 的格式复刻（见下）。
const FIXTURE_CWD = '/fixture/volatile'

type Case = { name: string; ctx: Partial<VolatileContext>; note?: string }

const cases: Case[] = [
  {
    name: 'minimal',
    note: '最小 ctx：只有 cwd。输出应只有 environment + sober',
    ctx: {},
  },
  {
    name: 'locusSelf',
    note: 'cwdRelation=self → locus 块（这是天枢自己的源码场景）',
    ctx: { cwdRelation: 'self' },
  },
  {
    name: 'locusWorld',
    note: 'cwdRelation=world → 另一条 locus 分支',
    ctx: { cwdRelation: 'world' },
  },
  {
    name: 'projectInstructions',
    note: 'rivetMd → project-instructions 块（含按节选取 + 转义）',
    ctx: { rivetMd: '## 甲节\n内容甲\n\n## 乙节\n内容乙' },
  },
  {
    name: 'projectInstructionsEscaping',
    note: 'rivetMd 含需转义字符（< > & "）',
    ctx: { rivetMd: '## 节\n<a> & "b" <c>' },
  },
  {
    name: 'projectMemory',
    note: 'projectMemoryBlock → 经 truncateBlock',
    ctx: { projectMemoryBlock: '<project-memory>\n记忆内容\n</project-memory>' },
  },
  {
    name: 'knowledgeManifest',
    ctx: { knowledgeManifestBlock: '<knowledge-manifest>\n索引内容\n</knowledge-manifest>' },
  },
  {
    name: 'seedCapsule',
    ctx: { seedCapsuleBlock: '<seed-capsules note="x">胶囊内容</seed-capsules>' },
  },
  {
    name: 'codebaseIndex',
    note: 'codebase-index 走 truncateBlock 的特殊分支',
    ctx: { projectIndexBlock: '<codebase-index>\n模块表\n</codebase-index>' },
  },
  {
    name: 'workingSet',
    note: 'workingSet 数组 → <working-set> 块（含转义）',
    ctx: { workingSet: ['src/a.ts', 'src/b.ts', 'src/<odd>.ts'] },
  },
  {
    name: 'sessionMemory',
    ctx: { sessionMemoryBlock: '记忆条目一\n记忆条目二' },
  },
  {
    name: 'starDomainNoKnowledge',
    note: 'activeDomain 无 knowledgeBlock',
    ctx: {
      activeDomain: { name: '天权', motto: '观天之道，执天之行', volatileBlock: '域内容' },
    },
  },
  {
    name: 'starDomainWithKnowledge',
    note: 'activeDomain 带 knowledgeBlock（含转义）',
    ctx: {
      activeDomain: {
        name: '天权',
        motto: '观天之道',
        volatileBlock: '域内容',
        knowledgeBlock: '域知识 <a> & "b"',
      },
    },
  },
  {
    name: 'blockCapsScaling',
    note: 'blockCaps 缩放——验证 caps 参与渲染（projectMemory 收到更小 cap）',
    ctx: {
      projectMemoryBlock: 'x'.repeat(100),
      blockCaps: { projectMemory: 30 },
    },
  },
  {
    name: 'allBlocks',
    note: '全块齐上——验证拼接顺序与分隔符（\\n\\n）',
    ctx: {
      cwdRelation: 'self',
      rivetMd: '## 项目节\n项目内容',
      projectMemoryBlock: '<project-memory>\n记忆\n</project-memory>',
      knowledgeManifestBlock: '<knowledge-manifest>\n索引\n</knowledge-manifest>',
      seedCapsuleBlock: '<seed-capsules>胶囊</seed-capsules>',
      projectIndexBlock: '<codebase-index>\n模块\n</codebase-index>',
      workingSet: ['a.ts'],
      sessionMemoryBlock: '会话记忆',
      activeDomain: { name: '天枢', motto: '证据先行', volatileBlock: '域块', knowledgeBlock: '域知识' },
    },
  },
  {
    name: 'emptyStrings',
    note: '空串字段应被跳过（falsy 判定）——验证不产生空块',
    ctx: {
      rivetMd: '',
      projectMemoryBlock: '',
      knowledgeManifestBlock: '',
      seedCapsuleBlock: '',
      projectIndexBlock: '',
      sessionMemoryBlock: '',
      workingSet: [],
    },
  },
  {
    name: 'cwdEscaping',
    note: 'cwd 含需转义字符（< > & "）——锁定 environment 行的转义',
    ctx: { cwd: '/tmp/a<b>&"c"/proj' },
  },
  {
    name: 'emojiContent',
    note: '含代理对字符——锁定 UTF-16 计费（truncateBlock 的切断）',
    ctx: {
      projectMemoryBlock: '<project-memory>\n' + '😀'.repeat(30) + '\n</project-memory>',
      blockCaps: { projectMemory: 30 },
    },
  },
]

const results: Record<string, { ctx: unknown; out: string; note?: string }> = {}
for (const c of cases) {
  const ctx = { cwd: FIXTURE_CWD, ...c.ctx } as VolatileContext
  const out = buildStableVolatileBlock(ctx)
  results[c.name] = { ctx, out, note: c.note }
}

// 记录宿主的 platform/os —— Go 侧对账时需产出同一行
const platform = process.platform
const osType = (await import('node:os')).type()
const osRelease = (await import('node:os')).release()

const out = {
  fixtureCwd: FIXTURE_CWD,
  host: { platform, osType, osRelease },
  cases: results,
}

mkdirSync(here, { recursive: true })
writeFileSync(join(here, 'oracle.json'), JSON.stringify(out, null, 2) + '\n')

const sha = createHash('sha256').update(JSON.stringify(out)).digest('hex')
console.error(
  `volatile oracle：${cases.length} 用例 / host=${platform} os=${osType} ${osRelease} — sha256 ${sha.slice(0, 16)}`,
)
