/**
 * checksum oracle 生成器。
 *
 * 生成：
 *   npx tsx go/testdata/checksum/gen-oracle.ts
 *
 * 覆盖 src/agent/checksum.ts 的四个导出纯函数：
 *   - computeLineChecksum(jsonLine)  —— SHA-256 前 8 字节 hex（16 字符）
 *   - appendChecksum(jsonLine)       —— `{json}|{checksum}`
 *   - verifyAndExtract(line)         —— 验证并提取（含 legacy 兼容）
 *   - verifyLines(lines)             —— 批量验证
 *
 * ## 为什么值得对账
 *
 * 这是会话 JSONL 的**完整性机制**：Go 版要能读 TS 版写的行（跨版本兼容，
 * Wave 5 判据），也要能被 TS 版读。校验和算法或 legacy 判定错一位，
 * 会话文件就整个读不出来。
 *
 * 纪律：调用真实导出函数，不手抄。
 */
import { writeFileSync, mkdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import { createHash } from 'node:crypto'
import {
  computeLineChecksum,
  appendChecksum,
  verifyAndExtract,
  verifyLines,
} from '../../../src/agent/checksum.js'

const here = dirname(fileURLToPath(import.meta.url))

// ── computeLineChecksum / appendChecksum 用例 ─────────────────────
const jsonLines = [
  '{"role":"user","content":"hi"}',
  '{}',
  '[]',
  '{"a":1,"b":[1,2,3]}',
  '{"中文":"内容"}',
  '{"emoji":"😀"}',
  '{"nested":{"deep":{"x":null}}}',
  '{"escaped":"a\\"b\\\\c"}',
  '{"pipe":"a|b"}', // 含 | 字符（legacy 判定的关键）
  '{"num":1.5e-7}',
  '', // 空串
  'x'.repeat(2000),
]

const checksums: Record<string, string> = {}
const appended: Record<string, string> = {}
for (const l of jsonLines) {
  checksums[l] = computeLineChecksum(l)
  appended[l] = appendChecksum(l)
}

// ── verifyAndExtract 用例 ────────────────────────────────────────
// 构造各类行：新格式（合法/损坏）、legacy（无 | / 校验和格式错 / jsonPart 非 JSON）
const validJson = '{"role":"user","content":"hi"}'
const validChecksum = computeLineChecksum(validJson)

const verifyCases: Record<string, string> = {
  // 新格式合法
  validNew: `${validJson}|${validChecksum}`,
  // 新格式校验和不匹配
  checksumMismatch: `${validJson}|${'0'.repeat(16)}`,
  // 校验和格式错（非 16 hex）→ legacy
  badChecksumFormat: `${validJson}|abc`,
  // 校验和含大写 → 不匹配正则 → legacy
  uppercaseChecksum: `${validJson}|${validChecksum.toUpperCase()}`,
  // 无 | → legacy
  noPipe: validJson,
  // jsonPart 非 JSON（含 | 的 legacy 行）→ legacy
  legacyWithPipe: 'not json | something',
  // 空行
  empty: '',
  // 只有空白
  whitespace: '   ',
  // 行尾换行（trim 应吃掉）
  trailingNewline: `${validJson}|${validChecksum}\n`,
  // 行首空格
  leadingSpaces: `   ${validJson}|${validChecksum}`,
  // 多个 | —— lastIndexOf 取最后一个
  multiplePipes: `${validJson}|extra|${validChecksum}`,
  // JSON 内含 | 且末尾有合法校验和
  jsonWithPipeAndChecksum: `{"pipe":"a|b"}|${computeLineChecksum('{"pipe":"a|b"}')}`,
  // **M4 区分点**：`|` 后是合法 16-hex 但 jsonPart 非 JSON → 必须靠判定 3
  // （格式判定拦不住，因为校验和格式是对的）
  jsonPartNotJsonButChecksumShaped: `not json at all|${'a'.repeat(16)}`,
  // **M4 区分点**：jsonPart 是残缺 JSON（看起来像但 parse 失败）
  truncatedJsonWithValidChecksumShape: `{"a":1|${'b'.repeat(16)}`,
  // 只有 | 分隔符
  onlyPipe: '|',
  // | 后为空
  pipeThenEmpty: `${validJson}|`,
  // 校验和正确但 jsonPart 是数组
  arrayJson: `[1,2]|${computeLineChecksum('[1,2]')}`,
  // CRLF 行尾
  crlf: `${validJson}|${validChecksum}\r\n`,
}

const verifyResults: Record<string, unknown> = {}
for (const [name, line] of Object.entries(verifyCases)) {
  verifyResults[name] = { line, result: verifyAndExtract(line) }
}

// ── verifyLines 用例 ─────────────────────────────────────────────
const verifyLinesCases: Record<string, string[]> = {
  allValid: [`${validJson}|${validChecksum}`, `[1,2]|${computeLineChecksum('[1,2]')}`],
  mixedLegacy: [validJson, `${validJson}|${validChecksum}`, 'not json | x'],
  withInvalid: [`${validJson}|${'0'.repeat(16)}`, `${validJson}|${validChecksum}`],
  emptyList: [],
  emptyLines: ['', '   ', `${validJson}|${validChecksum}`],
  allInvalid: [`${validJson}|${'0'.repeat(16)}`, `${validJson}|${'1'.repeat(16)}`],
}

const verifyLinesResults: Record<string, unknown> = {}
for (const [name, lines] of Object.entries(verifyLinesCases)) {
  verifyLinesResults[name] = { lines, result: verifyLines(lines) }
}

const out = {
  checksums,
  appended,
  verifyResults,
  verifyLinesResults,
}
mkdirSync(here, { recursive: true })
writeFileSync(join(here, 'oracle.json'), JSON.stringify(out, null, 2) + '\n')

const sha = createHash('sha256').update(JSON.stringify(out)).digest('hex')
console.error(
  `checksum oracle：${jsonLines.length} checksum / ${Object.keys(verifyCases).length} verify / ${Object.keys(verifyLinesCases).length} batch — sha256 ${sha.slice(0, 16)}`,
)
