/**
 * SessionMetadataStore oracle 生成器。
 *
 * 生成：npx tsx go/testdata/metadata/gen-oracle.ts
 *
 * ## 覆盖
 *
 * 对账 src/agent/session-metadata.ts（85 行）：
 * - `load` 的三态缓存（未加载 / 磁盘无文件 / 已加载）
 * - `write` 的**2 空格缩进 + 尾换行**（`JSON.stringify(m, null, 2) + '\n'`）
 * - `update` 的**合并优先级**（sessionId 权威 / createdAt 保留 / updatedAt 推进）
 * - `tokenUsage` 的**嵌套合并**
 * - `flush` 的 dirty 语义与失败保持 dirty
 *
 * ## 可复现性
 *
 * 时间戳（createdAt/updatedAt）不可复现——oracle 只记录**结构**（键集、
 * 哪些字段被更新），不记录具体时间值。缩进格式则逐字节记录。
 */
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync, existsSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createHash } from 'node:crypto'
import { SessionMetadataStore } from '../../../src/agent/session-metadata.js'

const here = dirname(fileURLToPath(import.meta.url))

type Op =
  | { op: 'load' }
  | { op: 'write'; metadata: Record<string, unknown> }
  | { op: 'update'; patch: Record<string, unknown>; sessionId: string }
  | { op: 'flush' }
  | { op: 'readRaw' }

type Case = {
  note: string
  /** 预置的 meta.json 内容（原始文本）*/
  preexisting?: string
  ops: Op[]
}

const cases: Record<string, Case> = {
  loadMissing: {
    note: '磁盘无文件 → load 返回 undefined',
    ops: [{ op: 'load' }, { op: 'load' }], // 二次确认缓存
  },

  loadExisting: {
    note: '磁盘有文件 → load 返回对象',
    preexisting: '{"sessionId":"s1","createdAt":100,"updatedAt":200,"compactEvents":[]}\n',
    ops: [{ op: 'load' }],
  },

  loadCorrupt: {
    note: '损坏 JSON → load 返回 undefined（不抛）',
    preexisting: '{not json',
    ops: [{ op: 'load' }],
  },

  writeFormat: {
    note: '**write 的缩进格式**（2 空格 + 尾换行）',
    ops: [
      { op: 'write', metadata: { sessionId: 's1', createdAt: 1, updatedAt: 2, compactEvents: [] } },
      { op: 'readRaw' },
    ],
  },

  writeWithOptionals: {
    note: 'write 的键序 = **调用方构造序**（此处复刻 initMetadata 的生产序）',
    ops: [
      { op: 'write', metadata: {
        // 复刻 src/agent/session-persist.ts:519-529 的 initMetadata 字面量序：
        // sessionId → createdAt → updatedAt → compactEvents → status
        //   → turnCount → toolCallCount → tokenUsage → ...init
        sessionId: 's1', createdAt: 1, updatedAt: 2, compactEvents: [],
        status: 'active', turnCount: 0, toolCallCount: 0,
        tokenUsage: { prompt: 1, completion: 2, total: 3 },
        model: 'deepseek-v4', provider: 'deepseek', title: 'T',
      } },
      { op: 'readRaw' },
    ],
  },

  updateFresh: {
    note: '无既有文件时 update（createdAt 首次设置）',
    ops: [
      { op: 'update', patch: { model: 'm1' }, sessionId: 's1' },
      { op: 'flush' },
      { op: 'readRaw' },
    ],
  },

  updateMerges: {
    note: '**合并**：既有字段保留、patch 覆盖',
    preexisting: '{"sessionId":"s1","createdAt":100,"updatedAt":100,"compactEvents":[],"model":"old","provider":"p1"}\n',
    ops: [
      { op: 'update', patch: { model: 'new' }, sessionId: 's1' },
      { op: 'flush' },
      { op: 'readRaw' },
    ],
  },

  updateSessionIdAuthoritative: {
    note: '**sessionId 由调用方权威**（覆盖既有）',
    preexisting: '{"sessionId":"OLD","createdAt":100,"updatedAt":100,"compactEvents":[]}\n',
    ops: [
      { op: 'update', patch: {}, sessionId: 'NEW' },
      { op: 'flush' },
      { op: 'readRaw' },
    ],
  },

  tokenUsageNestedMerge: {
    note: '**tokenUsage 嵌套合并**（不是替换）',
    preexisting: '{"sessionId":"s1","createdAt":100,"updatedAt":100,"compactEvents":[],"tokenUsage":{"prompt":10,"completion":20,"total":30}}\n',
    ops: [
      { op: 'update', patch: { tokenUsage: { total: 99 } }, sessionId: 's1' },
      { op: 'flush' },
      { op: 'readRaw' },
    ],
  },

  flushNotDirty: {
    note: '未 dirty 时 flush 不写盘',
    ops: [{ op: 'flush' }, { op: 'readRaw' }],
  },

  updateThenFlushIdempotent: {
    note: '连续两次 flush（第二次无操作）',
    ops: [
      { op: 'update', patch: { model: 'm' }, sessionId: 's1' },
      { op: 'flush' },
      { op: 'flush' },
      { op: 'readRaw' },
    ],
  },
}

const results: Record<string, unknown> = {}

for (const [name, c] of Object.entries(cases)) {
  const dir = mkdtempSync(join(tmpdir(), 'meta-'))
  const metaPath = join(dir, 's.meta.json')
  if (c.preexisting !== undefined) {
    writeFileSync(metaPath, c.preexisting)
  }

  const store = new SessionMetadataStore(metaPath)
  const observations: unknown[] = []

  for (const op of c.ops) {
    switch (op.op) {
      case 'load': {
        const r = store.load()
        observations.push({
          after: 'load',
          // 只记结构，不记时间戳
          hasValue: r !== undefined,
          keys: r ? Object.keys(r).sort() : null,
        })
        break
      }
      case 'write': {
        store.write(op.metadata as never)
        observations.push({ after: 'write' })
        break
      }
      case 'update': {
        store.update(op.patch as never, op.sessionId)
        observations.push({ after: 'update' })
        break
      }
      case 'flush': {
        store.flush()
        observations.push({ after: 'flush' })
        break
      }
      case 'readRaw': {
        observations.push({
          after: 'readRaw',
          exists: existsSync(metaPath),
          raw: existsSync(metaPath) ? readFileSync(metaPath, 'utf-8') : null,
        })
        break
      }
    }
  }

  results[name] = { note: c.note, preexisting: c.preexisting ?? null, observations }
  rmSync(dir, { recursive: true, force: true })
}

const out = { cases: results }
mkdirSync(here, { recursive: true })
writeFileSync(join(here, 'oracle.json'), JSON.stringify(out, null, 2) + '\n')
const sha = createHash('sha256').update(JSON.stringify(out)).digest('hex')
console.error(`metadata oracle：${Object.keys(cases).length} 用例 — sha256 ${sha.slice(0, 16)}`)
