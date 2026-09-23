/**
 * 工具 schema oracle 生成器。
 *
 * 生成：npx tsx go/testdata/toolschema/gen-oracle.ts
 *
 * ## 为什么需要
 *
 * Go 侧的 `InputSchema.Properties` 是 `map[string]any`（**无插入序**），
 * 旧实现用字典序排序属性键。而 TS 侧 schema 是**对象字面量声明序**
 * （zod 亦保声明序）。
 *
 * **后果**：工具定义变化"打的是整个前缀（system+tools 段）"
 * （src/api/openai-client.ts:630 的注释）——键序不同会让前缀缓存完全失效。
 *
 * ## 覆盖
 *
 * 从**真实注册表**（`createDefaultToolRegistry`）导出 10 个核心工具的
 * `input_schema`，**保留键的原始顺序**（`Object.keys` 的插入序）。
 *
 * 不手抄——任何手抄都会与真实代码漂移。
 */
import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createHash } from 'node:crypto'
import { createDefaultToolRegistry } from '../../../src/tools/default-registry.js'
import { ASK_USER_QUESTION_TOOL } from '../../../src/tools/ask-user-question.js'

const here = dirname(fileURLToPath(import.meta.url))

/** 本波次已移植到 Go 的工具（其余不在对账范围）。
 *
 * 注 1：`apply_patch` 已移到 TS 的 EXTENDED 层（default-registry.ts:88），
 * 不在 `createDefaultToolRegistry` 里——故它**无法**纳入本 oracle（会报
 * "注册表里找不到"）。Go 侧的 apply_patch 对账另见
 * `internal/tools/applypatch*_test.go`。
 *
 * 注 2：`related_tests` / `inspect_project` / `file_info` 等受 **preset 门控**
 * （default-registry.ts:159 的 `presetIncludes`）——只在 `full` 档注册。
 * 故此处显式传 `preset: 'full'`，否则这些工具在 oracle 里缺席（静默覆盖
 * 缺口：schema 对账会漏掉它们）。
 *
 * 注 3：`ask_user_question` **不在** `createDefaultToolRegistry` 里——它在
 * `src/bootstrap.ts:667` 注册（interactive 层）。故它单独从模块导入并追加，
 * 见下方的 `extraTools`。 */
const PORTED = [
  'read_file', 'write_file', 'edit_file', 'hash_edit',
  'glob', 'grep', 'bash', 'run_tests', 'todo',
  'related_tests',
  'leave_mark',
  'ask_user_question',
]

/** bootstrap 层注册的工具（不在 default registry 里，需单独并入）。 */
const extraTools: unknown[] = [ASK_USER_QUESTION_TOOL]

const registry = createDefaultToolRegistry([], { preset: 'full' })
// 注意：工具定义在 `tool.definition`（不是 `tool.input_schema`）；
// `getAll()` 返回的数组**未排序**，但 `getEnabledDefinitions()` 按 name 排序
// ——对账要的是**每个工具内部**的 properties 键序，不是工具间顺序。
const all = registry.getAll().concat(extraTools)

/** 递归导出值，**保留对象键的插入序**（用数组表达，避免 JSON.parse 丢序）。 */
type Ordered = { __ordered: Array<[string, Ordered | unknown]> } | unknown

function orderValue(v: unknown): Ordered {
  if (Array.isArray(v)) return v.map(orderValue)
  if (v !== null && typeof v === 'object') {
    return { __ordered: Object.entries(v as Record<string, unknown>).map(([k, val]) => [k, orderValue(val)]) }
  }
  return v
}

const out: Record<string, unknown> = {}
const missing: string[] = []

/** 工具名在 `tool.definition.name`（不是 `tool.name`）。 */
function toolName(t: unknown): string | undefined {
  return (t as { definition?: { name?: string } })?.definition?.name
}

for (const name of PORTED) {
  const tool = all.find(t => toolName(t) === name)
  if (!tool) {
    missing.push(name)
    continue
  }
  const def = (tool as { definition?: { description?: string; input_schema?: unknown } }).definition
  const schema = def?.input_schema
  out[name] = {
    description: def?.description ?? null,
    inputSchema: schema ? orderValue(schema) : null,
    /** 参数键的声明序（对账的核心断言面）。 */
    propOrder: schema && typeof schema === 'object' && 'properties' in schema
      ? Object.keys((schema as { properties: Record<string, unknown> }).properties)
      : [],
    required: schema && typeof schema === 'object' && 'required' in schema
      ? (schema as { required?: string[] }).required ?? []
      : [],
  }
}

if (missing.length > 0) {
  console.error(`⚠ 注册表里找不到这些工具：${missing.join(', ')}`)
}

mkdirSync(here, { recursive: true })
writeFileSync(join(here, 'oracle.json'), JSON.stringify(out, null, 2) + '\n')
const sha = createHash('sha256').update(JSON.stringify(out)).digest('hex')
console.error(`toolschema oracle：${Object.keys(out).length} 个工具 — sha256 ${sha.slice(0, 16)}`)
