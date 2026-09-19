/**
 * hash_edit oracle 生成器。
 *
 * 生成：npx tsx go/testdata/hashedit/gen-oracle.ts
 *
 * ## 为什么必须走 execute 真实路径
 *
 * `parseAnchor` / `recoverStaleAnchors` / `formatStaleDiagnostic` 都**未导出**，
 * 只能通过 `HASH_EDIT_TOOL.execute` 触发。这与 Wave 1 的教训一致——
 * 手抄内部逻辑会造出「双方同错、测试照绿」的假绿。
 *
 * ## 可复现性
 *
 * 临时目录路径会进 content（成功消息用绝对路径）。归一化为 `<DIR>` 占位符，
 * 否则 golden 三次运行各不同（Wave 3 的 mkdtempSync 教训）。
 *
 * ## 覆盖
 *
 * 成功路径 / 锚点格式错 / 非升序 / 过期诊断 / 锚点恢复 / dry_run /
 * eof 越界 / 删除（空 new_string）/ 多锚点区间 / 位置-only 锚点
 */
import { writeFileSync, mkdtempSync, readFileSync, mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createHash } from 'node:crypto'
import { HASH_EDIT_TOOL, hashLine } from '../../../src/tools/hash-edit.js'

const here = dirname(fileURLToPath(import.meta.url))

type Case = {
  note: string
  file?: string          // 初始文件内容
  input: Record<string, unknown>
}

const cases: Record<string, Case> = {
  // ── 成功路径 ──
  singleLineReplace: {
    note: '单锚点替换该行',
    file: 'line1\nline2\nline3\n',
    input: { anchors: ['L2:{{h:line2}}'], new_string: 'LINE-TWO' },
  },
  rangeReplace: {
    note: '首尾锚点定义区间（含两端）',
    file: 'a\nb\nc\nd\ne\n',
    input: { anchors: ['L2:{{h:b}}', 'L4:{{h:d}}'], new_string: 'X\nY' },
  },
  deleteRange: {
    note: '空 new_string = 删除区间',
    file: 'a\nb\nc\nd\n',
    input: { anchors: ['L2:{{h:b}}', 'L3:{{h:c}}'], new_string: '' },
  },
  insertAfter: {
    note: '单锚点插入（原内容 + 新增）',
    file: 'a\nb\nc\n',
    input: { anchors: ['L2:{{h:b}}'], new_string: 'b\nNEW' },
  },
  threeAnchors: {
    note: '三锚点（中间锚点校验区间内部）',
    file: 'a\nb\nc\nd\ne\n',
    input: { anchors: ['L2:{{h:b}}', 'L3:{{h:c}}', 'L4:{{h:d}}'], new_string: 'XYZ' },
  },
  positionOnly: {
    note: '位置-only 锚点（无哈希）——新 session 未读过文件，不应有漂移警告',
    file: 'a\nb\nc\n',
    input: { anchors: ['L2'], new_string: 'B' },
  },
  dryRun: {
    note: 'dry_run=true 不写盘',
    file: 'a\nb\nc\n',
    input: { anchors: ['L2:{{h:b}}'], new_string: 'B2', dry_run: true },
  },
  anchorWithSuffix: {
    note: '锚点容忍行尾内容后缀（fresh anchor 形如 "L2:hash → content"）',
    file: 'a\nb\nc\n',
    input: { anchors: ['L2:{{h:b}} → b'], new_string: 'B' },
  },

  // ── 错误路径 ──
  badAnchorFormat: {
    note: '锚点格式非法',
    file: 'a\nb\n',
    input: { anchors: ['XYZ'], new_string: 'X' },
  },
  anchorZero: {
    note: 'L0 非法（行号必须 >= 1）',
    file: 'a\nb\n',
    input: { anchors: ['L0:aaaaaaaa'], new_string: 'X' },
  },
  tooManyAnchors: {
    note: '超过 3 个锚点',
    file: 'a\nb\nc\nd\ne\n',
    input: { anchors: ['L1:aaaaaaaa', 'L2:bbbbbbbb', 'L3:cccccccc', 'L4:dddddddd'], new_string: 'X' },
  },
  emptyAnchors: {
    note: '空锚点数组',
    file: 'a\nb\n',
    input: { anchors: [], new_string: 'X' },
  },
  notAscending: {
    note: '锚点非升序（会静默损坏文件）',
    file: 'a\nb\nc\n',
    input: { anchors: ['L3:{{h:c}}', 'L1:{{h:a}}'], new_string: 'X' },
  },
  duplicateLine: {
    note: '锚点行号重复（非严格升序）',
    file: 'a\nb\nc\n',
    input: { anchors: ['L2:{{h:b}}', 'L2:{{h:b}}'], new_string: 'X' },
  },
  staleHash: {
    note: '哈希过期 → 诊断文本（含可重试锚点）',
    file: 'a\nb\nc\n',
    input: { anchors: ['L2:deadbeef'], new_string: 'X' },
  },
  eofOverrun: {
    note: '行号超出文件长度 → <eof> 不匹配',
    file: 'a\nb\n',
    input: { anchors: ['L99:aaaaaaaa'], new_string: 'X' },
  },
  fileNotFound: {
    note: '文件不存在',
    input: { anchors: ['L1:aaaaaaaa'], new_string: 'X' },
    file: undefined,
  },
}

/** 把 `{{h:TEXT}}` 展开为 hashLine(TEXT)。 */
function expandHash(s: string): string {
  return s.replace(/\{\{h:([^}]*)\}\}/g, (_, t) => hashLine(t))
}

/** 归一化：把临时目录替换为 <DIR>，保证 golden 可复现。 */
function normalize(s: string, dir: string): string {
  return s.split(dir).join('<DIR>')
}

const results: Record<string, unknown> = {}

for (const [name, c] of Object.entries(cases)) {
  const dir = mkdtempSync(join(tmpdir(), 'he-'))
  const fp = join(dir, 'target.txt')
  const useFile = 'file' in c && c.file !== undefined
  if (useFile) writeFileSync(fp, c.file!)

  const input: Record<string, unknown> = {}
  for (const [k, v] of Object.entries(c.input)) {
    if (k === 'anchors') {
      input[k] = (v as string[]).map(expandHash)
    } else {
      input[k] = v
    }
  }
  // file_path 缺省指向 target.txt；fileNotFound 用例指向不存在的路径
  input.file_path = useFile ? fp : join(dir, 'nope.txt')

  let res: unknown
  try {
    const r = await HASH_EDIT_TOOL.execute({
      toolUseId: 'tu1', cwd: dir, sessionId: 'oracle-session',
      input,
    } as never)
    res = {
      content: normalize((r as { content: string }).content, dir),
      isError: (r as { isError?: boolean }).isError ?? false,
    }
  } catch (e) {
    res = { threw: String(e) }
  }

  // 记录最终文件内容（dry_run 应未改动；错误路径应未改动）
  let finalContent: string | null = null
  if (useFile) {
    try {
      finalContent = readFileSync(fp, 'utf-8')
    } catch {
      finalContent = null
    }
  }

  results[name] = {
    note: c.note,
    initial: useFile ? c.file : null,
    expandedAnchors: input.anchors ?? null,
    result: res,
    finalContent,
  }
}

const out = { hashLineSamples: { line2: hashLine('line2'), a: hashLine('a') }, cases: results }
mkdirSync(here, { recursive: true })
writeFileSync(join(here, 'oracle.json'), JSON.stringify(out, null, 2) + '\n')
const sha = createHash('sha256').update(JSON.stringify(out)).digest('hex')
console.error(`hashedit oracle：${Object.keys(cases).length} 用例 — sha256 ${sha.slice(0, 16)}`)
