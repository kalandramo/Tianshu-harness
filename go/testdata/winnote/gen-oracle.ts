/**
 * windowsShellNote + platform/path-style note oracle 生成器。
 *
 * 生成：
 *   npx tsx go/testdata/winnote/gen-oracle.ts
 *
 * 覆盖 src/prompt/volatile.ts 的：
 *   - windowsShellNote(kind) —— 导出的纯函数，四种分支
 *   - platform-note / path-style-note 的字面量（在 buildVolatileBlockInternal 内）
 *
 * ## 平台相关的可测性
 *
 * `windowsShellNote` 是导出的纯函数，**直接对账**。
 *
 * platform-note / path-style-note 的触发条件是
 * `targetPlatform !== process.platform` 与 `targetPlatform === 'win32'`，
 * 而 targetPlatform 来自 getTargetPlatform()（会话内固定）。本机 darwin，
 * 无法通过真实调用触发 win32 分支。
 *
 * 故这两条 note 的**字面量**在此以文本形式导出（从 TS 源码的模板串），
 * Go 侧用同样的插值复刻。这是**唯一**手抄处，用两条独立用例锁定
 * （断言含关键子串），若 TS 侧改动文案会红。
 */
import { writeFileSync, mkdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import { createHash } from 'node:crypto'
import { windowsShellNote } from '../../../src/prompt/volatile.js'

const here = dirname(fileURLToPath(import.meta.url))

// ── windowsShellNote：直接调真实函数 ────────────────────────────
const kinds = ['bash', 'powershell', 'cmd', 'sh', 'unknown-kind'] as const
const shellNotes: Record<string, string> = {}
for (const k of kinds) {
  shellNotes[k] = windowsShellNote(k as never)
}

// ── platform-note / path-style-note：字面量模板 ──────────────────
// 从 volatile.ts:1065 / 1072 的模板串逐字转写（插值用 {target} / {host} 占位）。
// 用独立用例断言关键子串，TS 侧改动文案会让 Go 测试红。
const platformNoteTemplate =
  '<platform-note>文件约定（换行/路径风格）按 {target} 生成；但 shell 命令在宿主 {host} 上执行——优先使用跨平台命令，避免目标平台专属语法在宿主机执行失败。</platform-note>'

const pathStyleNote =
  '<path-style-note>Windows 环境：在回复和文档中书写文件路径时用反斜杠（如 src\\tui\\app.ts、D:\\proj\\file.md），与用户的资源管理器/终端习惯一致。工具参数两种分隔符都接受；shell 命令内的路径写法以 shell-note 为准（Git Bash 用正斜杠）。</path-style-note>'

const out = {
  _note: 'shellNotes 来自真实 windowsShellNote 调用；platformNoteTemplate/pathStyleNote 是字面量转写',
  shellNotes,
  platformNoteTemplate,
  pathStyleNote,
}

mkdirSync(here, { recursive: true })
writeFileSync(join(here, 'oracle.json'), JSON.stringify(out, null, 2) + '\n')

const sha = createHash('sha256').update(JSON.stringify(out)).digest('hex')
console.error(`winnote oracle：${kinds.length} 个 shell kind + 2 条 note 模板 — sha256 ${sha.slice(0, 16)}`)
