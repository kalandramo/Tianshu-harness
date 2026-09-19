/**
 * extractPatchTargetPaths oracle 生成器。
 *
 * 生成：
 *   npx tsx go/testdata/patchpath/gen-oracle.ts
 *
 * 覆盖 src/tools/apply-patch.ts:34 的 extractPatchTargetPaths —— 从 unified
 * diff 文本提取目标文件路径。**纯函数，完全可对账**。
 *
 * 语义（对账 TS 源码逐行）：
 *   - 只看以 `+++ ` 开头的行
 *   - 取 `+++ ` 之后的内容，trim
 *   - 若含 tab，截断到 tab 之前（git 的 `+++ b/file\ttimestamp` 形态）
 *   - `/dev/null` 跳过（纯删除无应用后内容可验）
 *   - 去掉首尾成对引号（`"path"` → `path`，git 对含特殊字符的路径加引号）
 *   - 去掉开头的 `a/` 或 `b/` 前缀
 *   - 空串跳过；用 Set 去重；保持首次出现顺序
 *
 * 纪律：调用真实导出函数，不手抄。
 */
import { writeFileSync, mkdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import { createHash } from 'node:crypto'
import { extractPatchTargetPaths } from '../../../src/tools/apply-patch.js'

const here = dirname(fileURLToPath(import.meta.url))

type Case = { name: string; note?: string; diff: string }

const cases: Case[] = [
  { name: 'empty', note: '空 diff', diff: '' },
  { name: 'noPlusLines', note: '无 +++ 行', diff: '--- a/x\n@@ -1 +1 @@\n-a\n+b\n' },
  {
    name: 'singleFile',
    note: '单文件标准 diff',
    diff: '--- a/src/x.ts\n+++ b/src/x.ts\n@@ -1,2 +1,2 @@\n-old\n+new\n',
  },
  {
    name: 'multiFile',
    note: '多文件——顺序保持首次出现',
    diff:
      '--- a/a.ts\n+++ b/a.ts\n@@ -1 +1 @@\n-x\n+y\n' +
      '--- a/b.ts\n+++ b/b.ts\n@@ -1 +1 @@\n-p\n+q\n',
  },
  {
    name: 'duplicatePaths',
    note: '**去重**：同一路径出现两次只算一次',
    diff: '--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n--- a/x\n+++ b/x\n@@ -2 +2 @@\n-c\n+d\n',
  },
  {
    name: 'devNullDeletion',
    note: '**跳过 /dev/null**（纯删除）',
    diff: '--- a/gone.ts\n+++ /dev/null\n@@ -1 +0,0 @@\n-bye\n',
  },
  {
    name: 'mixedDevNull',
    note: '删除 + 修改混合——只取后者',
    diff:
      '--- a/gone.ts\n+++ /dev/null\n@@ -1 +0,0 @@\n-bye\n' +
      '--- a/kept.ts\n+++ b/kept.ts\n@@ -1 +1 @@\n-x\n+y\n',
  },
  {
    name: 'tabTimestamp',
    note: '**含 tab 时间戳**：截断到 tab 之前',
    diff: '--- a/x\t2024-01-01\n+++ b/x\t2024-01-01\n@@ -1 +1 @@\n-a\n+b\n',
  },
  {
    name: 'quotedPath',
    note: '**含引号路径**：去掉成对引号',
    diff: '--- "a/with space.ts"\n+++ "b/with space.ts"\n@@ -1 +1 @@\n-a\n+b\n',
  },
  {
    name: 'newFile',
    note: '新建文件（--- 是 /dev/null）',
    diff: '--- /dev/null\n+++ b/new.ts\n@@ -0,0 +1 @@\n+hello\n',
  },
  {
    name: 'noABPrefix',
    note: '**无 a/ b/ 前缀**的路径（非 git diff）',
    diff: '--- x.ts\n+++ x.ts\n@@ -1 +1 @@\n-a\n+b\n',
  },
  {
    name: 'onlyAPrefix',
    note: '只有 a/ 前缀（b/ 不适用时）',
    diff: '--- a/x.ts\n+++ a/x.ts\n@@ -1 +1 @@\n-a\n+b\n',
  },
  {
    name: 'crlfLine',
    note: 'CRLF 行尾——trim 应吃掉 \\r',
    diff: '--- a/x.ts\r\n+++ b/x.ts\r\n@@ -1 +1 @@\r\n-a\r\n+b\r\n',
  },
  {
    name: 'unicodePath',
    note: '含中文路径',
    diff: '--- a/中文.ts\n+++ b/中文.ts\n@@ -1 +1 @@\n-a\n+b\n',
  },
  {
    name: 'emojiPath',
    note: '含 emoji 路径',
    diff: '--- a/😀.ts\n+++ b/😀.ts\n@@ -1 +1 @@\n-a\n+b\n',
  },
  {
    name: 'trailingSpaces',
    note: '路径后有空格（trim 应吃掉）',
    diff: '--- a/x.ts   \n+++ b/x.ts   \n@@ -1 +1 @@\n-a\n+b\n',
  },
  {
    name: 'cPrefixNotStripped',
    note: '**M6 区分点**：`c/` 开头不应被剥（只剥 a/ 与 b/）',
    diff: '+++ c/other.ts\n',
  },
  {
    name: 'dPrefixNotStripped',
    note: '**M6 区分点**：`d/` 开头不应被剥',
    diff: '+++ d/other.ts\n',
  },
  {
    name: 'singleCharBeforeSlash',
    note: '边界：`a/` 与 `b/` 之外的单字符前缀（如 `x/`）不剥',
    diff: '+++ x/other.ts\n',
  },
  {
    name: 'emptyPathAfterPrefix',
    note: '**空前缀**：+++ b/ 去掉前缀后为空 → 跳过',
    diff: '--- a/x\n+++ b/\n@@ -1 +1 @@\n-a\n+b\n',
  },
  {
    name: 'plusPlusPlusInContent',
    note: '**内容行以 +++ 开头**（不是 diff 头）——仍会被当作路径行',
    diff: '--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n++++not-a-header\n',
  },
]

const results: Record<string, { diff: string; out: string[]; note?: string }> = {}
for (const c of cases) {
  results[c.name] = { diff: c.diff, out: extractPatchTargetPaths(c.diff), note: c.note }
}

const out = { cases: results }
mkdirSync(here, { recursive: true })
writeFileSync(join(here, 'oracle.json'), JSON.stringify(out, null, 2) + '\n')

const sha = createHash('sha256').update(JSON.stringify(out)).digest('hex')
console.error(`patchpath oracle：${cases.length} 用例 — sha256 ${sha.slice(0, 16)}`)
