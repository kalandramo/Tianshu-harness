/**
 * scanZstdFrames / isZstdFrameStream oracle 生成器。
 *
 * 生成：
 *   npx tsx go/testdata/zstdframe/gen-oracle.ts
 *
 * 覆盖 src/agent/session-transcript-codec.ts 的两个导出函数：
 *   - isZstdFrameStream(buffer)  —— 魔数判定
 *   - scanZstdFrames(buffer)     —— RFC 8878 帧头结构走查（不解压块）
 *
 * ## 为什么这两个值得对账
 *
 * 会话 transcript 的跨版本兼容（Wave 5 判据）依赖它：Go 版要能读 TS 版写的
 * zstd 帧流。帧扫描是**纯字节逻辑**（无 IO、无状态），完全可对账。
 *
 * 且它有「torn tail」语义：崩溃截断的末帧应被丢弃而非报错——这是崩溃恢复
 * 的核心，错一位就整条会话读不出来。
 *
 * 纪律：用**真实 zstd 帧**（zstdCompressSync 产出）+ 手工构造的损坏/截断用例。
 */
import { writeFileSync, mkdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import { createHash } from 'node:crypto'
import { zstdCompressSync, constants } from 'node:zlib'
import { scanZstdFrames, isZstdFrameStream } from '../../../src/agent/session-transcript-codec.js'

const here = dirname(fileURLToPath(import.meta.url))

const CHECKSUM = { params: { [constants.ZSTD_c_checksumFlag]: 1 } }
const NO_CHECKSUM = {}

type Case = {
  name: string
  note?: string
  /** 构造 buffer 的方式（保证 golden 可复现）。 */
  build: () => Buffer
}

const cases: Case[] = [
  {
    name: 'empty',
    note: '空 buffer → 无帧、无 torn',
    build: () => Buffer.alloc(0),
  },
  {
    name: 'plainTextNotZstd',
    note: '普通文本 → isZstd=false，扫描应抛错（无魔数）',
    build: () => Buffer.from('{"a":1}\n{"b":2}\n', 'utf-8'),
  },
  {
    name: 'singleFrameChecksum',
    note: '单个带校验位的帧',
    build: () => zstdCompressSync(Buffer.from('hello\n', 'utf-8'), CHECKSUM),
  },
  {
    name: 'singleFrameNoChecksum',
    note: '单个不带校验位的帧（descriptor 的 checksum 位为 0）',
    build: () => zstdCompressSync(Buffer.from('hello\n', 'utf-8'), NO_CHECKSUM),
  },
  {
    name: 'twoFrames',
    note: '两帧拼接——应扫出 2 个 frame range',
    build: () =>
      Buffer.concat([
        zstdCompressSync(Buffer.from('batch1\n', 'utf-8'), CHECKSUM),
        zstdCompressSync(Buffer.from('batch2\n', 'utf-8'), CHECKSUM),
      ]),
  },
  {
    name: 'threeFrames',
    note: '三帧拼接',
    build: () =>
      Buffer.concat([
        zstdCompressSync(Buffer.from('a\n', 'utf-8'), CHECKSUM),
        zstdCompressSync(Buffer.from('bb\n', 'utf-8'), CHECKSUM),
        zstdCompressSync(Buffer.from('ccc\n', 'utf-8'), CHECKSUM),
      ]),
  },
  {
    name: 'emptyFrame',
    note: '**空输入的帧**（13 字节，无内容块）',
    build: () => zstdCompressSync(Buffer.alloc(0), CHECKSUM),
  },
  {
    name: 'tornInMagic',
    note: '**torn tail**：只剩 2 字节魔数（< 4）→ 首帧起点作 tornStart',
    build: () => {
      const full = zstdCompressSync(Buffer.from('x\n', 'utf-8'), CHECKSUM)
      return Buffer.concat([full, Buffer.from([0x28, 0xb5])])
    },
  },
  {
    name: 'tornInDescriptor',
    note: '**torn tail**：魔数完整但 descriptor 缺失',
    build: () => {
      const full = zstdCompressSync(Buffer.from('x\n', 'utf-8'), CHECKSUM)
      return Buffer.concat([full, Buffer.from([0x28, 0xb5, 0x2f, 0xfd])])
    },
  },
  {
    name: 'tornInBlockData',
    note: '**torn tail**：帧头完整但块数据截断',
    build: () => {
      const full = zstdCompressSync(Buffer.from('hello world hello\n', 'utf-8'), CHECKSUM)
      const second = zstdCompressSync(Buffer.from('second\n', 'utf-8'), CHECKSUM)
      // 第二帧截掉尾部若干字节
      return Buffer.concat([full, second.subarray(0, second.length - 5)])
    },
  },
  {
    name: 'badMagicAfterGoodFrame',
    note: '**损坏**：首帧完整但后续魔数错误 → 应抛错',
    build: () => {
      const full = zstdCompressSync(Buffer.from('x\n', 'utf-8'), CHECKSUM)
      return Buffer.concat([full, Buffer.from([0xde, 0xad, 0xbe, 0xef, 0x00, 0x00])])
    },
  },
  {
    name: 'reservedBlockType',
    note: '**M6 区分点**：保留块类型 0x03 → 应抛错（手工构造，真实 zstd 不产出）',
    build: () => {
      // 魔数 + descriptor(0x24: 单段、无校验、contentSize 1 字节) + 1 字节 contentSize
      // + 块头 3 字节：lastBlock=0, blockType=0x03(保留), size=0
      return Buffer.from([0x28, 0xb5, 0x2f, 0xfd, 0x24, 0x00, 0x06, 0x00, 0x00])
    },
  },
  {
    name: 'multiSegmentFrame',
    note: '**M5 区分点**：非单段帧（singleSegment=0）→ 帧头多 1 字节 window descriptor',
    build: () => {
      // descriptor=0x04: singleSegment=0, checksum=0, contentSizeFlag=0, dictFlag=0
      // 故：windowDescriptor 1 字节 + contentSize 0 字节
      // 块头 3 字节：lastBlock=1, blockType=0x01(RLE), size=1
      // payload 1 字节
      return Buffer.from([0x28, 0xb5, 0x2f, 0xfd, 0x04, 0x00, 0x09, 0x00, 0x00, 0xaa])
    },
  },
  {
    name: 'contentSizeFlagSet',
    note: '**M5 区分点**：contentSizeFlag=1 → contentSize 占 2 字节',
    build: () => {
      // descriptor=0x60: contentSizeFlag=1(0x40), singleSegment=0x20 → 0x60
      // 故：contentSize 2 字节（singleSegment 时无 window descriptor）
      // 块头 3 字节 + payload
      return Buffer.from([0x28, 0xb5, 0x2f, 0xfd, 0x60, 0x05, 0x00, 0x09, 0x00, 0x00, 0xaa])
    },
  },
  {
    name: 'largeFrame',
    note: '大内容帧（多块）',
    build: () => zstdCompressSync(Buffer.from('x'.repeat(100000), 'utf-8'), CHECKSUM),
  },
  {
    name: 'manySmallFrames',
    note: '10 个小帧拼接',
    build: () =>
      Buffer.concat(
        Array.from({ length: 10 }, (_, i) =>
          zstdCompressSync(Buffer.from(`line${i}\n`, 'utf-8'), CHECKSUM),
        ),
      ),
  },
]

const results: Record<string, unknown> = {}
for (const c of cases) {
  const buf = c.build()
  const entry: Record<string, unknown> = {
    note: c.note,
    // 记录字节（hex）以便 Go 侧用同一输入复现
    hex: buf.toString('hex'),
    isZstd: isZstdFrameStream(buf),
  }
  try {
    const scan = scanZstdFrames(buf)
    entry.frames = scan.frames
    entry.tornStart = scan.tornStart ?? null
    entry.error = null
  } catch (e) {
    entry.frames = null
    entry.tornStart = null
    entry.error = e instanceof Error ? e.message : String(e)
  }
  results[c.name] = entry
}

const out = {
  _note: 'hex 是输入字节；frames/tornStart/error 是 TS 真实 scanZstdFrames 的产出',
  magicLE: 0xfd2fb528,
  cases: results,
}
mkdirSync(here, { recursive: true })
writeFileSync(join(here, 'oracle.json'), JSON.stringify(out, null, 2) + '\n')

const sha = createHash('sha256').update(JSON.stringify(out)).digest('hex')
const errCount = Object.values(results).filter((r) => (r as { error: unknown }).error).length
console.error(
  `zstdframe oracle：${cases.length} 用例（${errCount} 个抛错）— sha256 ${sha.slice(0, 16)}`,
)
