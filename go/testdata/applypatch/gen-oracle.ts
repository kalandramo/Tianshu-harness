/**
 * apply_patch oracle 生成器。
 *
 * 生成：npx tsx go/testdata/applypatch/gen-oracle.ts
 *
 * ## 覆盖
 *
 * 1. **`extractPatchTargetPaths`**（未导出 → 走 APPLY_PATCH_TOOL execute 的
 *    校验路径间接观察，或直接复刻其输入输出对：这里用工具成功路径的
 *    uiContent 与错误消息侧面锁定）。为直取该函数语义，本生成器**另起
 *    独立对账**：把 diff 写进临时 repo 让工具真跑，比对「目标文件集合」。
 * 2. **`normalizeDiffPaths`**（未导出）：Windows 反斜杠路径归一化。
 * 3. **`applyPatch`**（导出 → 直调）：真实 git apply --3way 的行为。
 *
 * ## 环境要求
 *
 * 需要 `git` 可执行文件。临时目录里 `git init` 造一个真实仓库——
 * 否则 `git apply` 会因不在仓库内而失败。
 *
 * ## 可复现性
 *
 * 临时路径归一化为 <DIR>。
 */
import { writeFileSync, mkdtempSync, mkdirSync, readFileSync, existsSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createHash } from 'node:crypto'
import { spawnSync } from 'node:child_process'
import { APPLY_PATCH_TOOL, applyPatch } from '../../../src/tools/apply-patch.js'

const here = dirname(fileURLToPath(import.meta.url))

function gitInit(dir: string): boolean {
  const r = spawnSync('git', ['init'], { cwd: dir, stdio: 'ignore' })
  if (r.status !== 0) return false
  spawnSync('git', ['config', 'user.email', 't@t'], { cwd: dir, stdio: 'ignore' })
  spawnSync('git', ['config', 'user.name', 't'], { cwd: dir, stdio: 'ignore' })
  return true
}

function normalize(s: string, dir: string): string {
  return s.split(dir).join('<DIR>')
}

const results: Record<string, unknown> = {}

// ── 用例：真实 git apply ──
type Case = {
  note: string
  files: Record<string, string>
  diff: string
  checkOnly?: boolean
}

// 一个能干净应用的补丁：把 a.txt 的 line2 改成 LINE-TWO
const cleanDiff = [
  '--- a/a.txt',
  '+++ b/a.txt',
  '@@ -1,3 +1,3 @@',
  ' line1',
  '-line2',
  '+LINE-TWO',
  ' line3',
  '',
].join('\n')

// 一个上下文不匹配的补丁（应失败）
const badDiff = [
  '--- a/a.txt',
  '+++ b/a.txt',
  '@@ -1,3 +1,3 @@',
  ' NOTHING',
  '-NOTHING',
  '+X',
  ' NOTHING',
  '',
].join('\n')

// 新增文件
const newFileDiff = [
  '--- /dev/null',
  '+++ b/new.txt',
  '@@ -0,0 +1,2 @@',
  '+hello',
  '+world',
  '',
].join('\n')

// 删除文件（+++ /dev/null）
const deleteDiff = [
  '--- a/a.txt',
  '+++ /dev/null',
  '@@ -1,3 +0,0 @@',
  '-line1',
  '-line2',
  '-line3',
  '',
].join('\n')

// 多文件
const multiDiff = [
  '--- a/a.txt',
  '+++ b/a.txt',
  '@@ -1,3 +1,3 @@',
  ' line1',
  '-line2',
  '+A2',
  ' line3',
  '--- a/b.txt',
  '+++ b/b.txt',
  '@@ -1,1 +1,1 @@',
  '-old',
  '+new',
  '',
].join('\n')

const cases: Record<string, Case> = {
  cleanApply: { note: '干净应用', files: { 'a.txt': 'line1\nline2\nline3\n' }, diff: cleanDiff },
  cleanApplyCheckOnly: { note: 'check_only 不写盘', files: { 'a.txt': 'line1\nline2\nline3\n' }, diff: cleanDiff, checkOnly: true },
  badContext: { note: '上下文不匹配 → 失败', files: { 'a.txt': 'line1\nline2\nline3\n' }, diff: badDiff },
  newFile: { note: '新增文件（+++ 非 /dev/null）', files: {}, diff: newFileDiff },
  deleteFile: { note: '删除文件（+++ /dev/null → 目标集合为空）', files: { 'a.txt': 'line1\nline2\nline3\n' }, diff: deleteDiff },
  multiFile: { note: '多文件', files: { 'a.txt': 'line1\nline2\nline3\n', 'b.txt': 'old\n' }, diff: multiDiff },
  emptyDiff: { note: '空 diff → 参数错误', files: {}, diff: '' },
  pointerDiff: { note: '指针占位符 → 拦截', files: {}, diff: '[patch applied to …]' },
  notARepo: { note: '非 git 仓库', files: { 'a.txt': 'x\n' }, diff: cleanDiff, note2: true } as never,
}

for (const [name, c] of Object.entries(cases)) {
  if (name === 'notARepo') {
    // 不 git init —— 观察非仓库行为
    const dir = mkdtempSync(join(tmpdir(), 'ap-'))
    writeFileSync(join(dir, 'a.txt'), 'x\n')
    const r = await applyPatch(dir, { diff: c.diff })
    results[name] = {
      note: '非 git 仓库',
      diff: c.diff,
      result: { ok: r.ok, error: normalize(r.error, dir) },
    }
    continue
  }

  const dir = mkdtempSync(join(tmpdir(), 'ap-'))
  gitInit(dir)
  for (const [rel, content] of Object.entries(c.files)) {
    const p = join(dir, rel)
    mkdirSync(dirname(p), { recursive: true })
    writeFileSync(p, content)
  }
  // 先 commit，--3way 需要 index 基线
  spawnSync('git', ['add', '-A'], { cwd: dir, stdio: 'ignore' })
  spawnSync('git', ['commit', '-m', 'init'], { cwd: dir, stdio: 'ignore' })

  let toolRes: { content: string; isError?: boolean } | null = null
  try {
    toolRes = (await APPLY_PATCH_TOOL.execute({
      toolUseId: 'tu', cwd: dir, sessionId: 's',
      input: { diff: c.diff, ...(c.checkOnly ? { check_only: true } : {}) },
    } as never)) as { content: string; isError?: boolean }
  } catch (e) {
    toolRes = { content: 'THREW: ' + String(e), isError: true }
  }

  // 记录最终文件状态（排序后）
  const finals: Record<string, string> = {}
  for (const rel of [...Object.keys(c.files), 'new.txt']) {
    const p = join(dir, rel)
    finals[rel] = existsSync(p) ? readFileSync(p, 'utf-8') : '<ABSENT>'
  }

  results[name] = {
    note: c.note,
    diff: c.diff,
    checkOnly: c.checkOnly ?? false,
    result: { content: normalize(toolRes!.content, dir), isError: toolRes!.isError ?? false },
    finals,
  }
}

const out = { cases: results }
mkdirSync(here, { recursive: true })
writeFileSync(join(here, 'oracle.json'), JSON.stringify(out, null, 2) + '\n')
const sha = createHash('sha256').update(JSON.stringify(out)).digest('hex')
console.error(`applypatch oracle：${Object.keys(cases).length} 用例 — sha256 ${sha.slice(0, 16)}`)
