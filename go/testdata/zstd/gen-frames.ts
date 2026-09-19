/**
 * zstd 帧 oracle 生成器 —— 跨版本兼容的 golden。
 *
 * 生成：npx tsx go/testdata/zstd/gen-frames.ts
 *
 * ## 为什么这个 oracle 特别重要
 *
 * 会话 transcript 的 zstd 帧是**跨版本读写**的载体：Go 版要能读 TS 版写的
 * 文件，也要能被 TS 版读。判据是「互解」而不是「同字节」——压缩器实现差异
 * 允许帧字节不同（实测 Node 用 singleSegment、Go 默认不用，descriptor 位
 * 不同），但必须互相可解。
 *
 * 本生成器用**真实 Node zstdCompressSync**（与 src/agent/session-transcript-codec.ts
 * 同一调用与参数）产出帧，供 Go 侧解码对账。
 */
import { writeFileSync, mkdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createHash } from 'node:crypto'
import { constants, zstdCompressSync, zstdDecompressSync } from 'node:zlib'

const here = dirname(fileURLToPath(import.meta.url))

// 与 session-transcript-codec.ts 的 CHECKSUM_OPTIONS 逐字一致
const CHECKSUM_OPTIONS = {
  params: { [constants.ZSTD_c_checksumFlag]: 1 },
}

const frames: Record<string, { text: string; hex: string }> = {
  empty: { text: '', hex: '' }, // 空文本不产帧（encodeBatch 的约定）
  simple: { text: 'hello zstd\n', hex: '' },
  chinese: { text: '中文内容\n第二行\n', hex: '' },
  emoji: { text: '😀 emoji 🎉\n', hex: '' },
  jsonl: {
    text: '{"role":"user","content":"hi"}|a1b2c3d4e5f60718\n' +
          '{"role":"assistant","content":"ok"}|1234567890abcdef\n',
    hex: '',
  },
  large: { text: 'x'.repeat(5000) + '\n', hex: '' },
}

for (const [name, f] of Object.entries(frames)) {
  if (f.text === '') {
    f.hex = ''
    continue
  }
  f.hex = zstdCompressSync(Buffer.from(f.text, 'utf-8'), CHECKSUM_OPTIONS).toString('hex')
}

// 拼接样本：两帧连写（模拟多次 appendBatch）
const first = '第一帧内容\n'
const second = '第二帧内容\n'
const f1 = zstdCompressSync(Buffer.from(first, 'utf-8'), CHECKSUM_OPTIONS)
const f2 = zstdCompressSync(Buffer.from(second, 'utf-8'), CHECKSUM_OPTIONS)
const concat = Buffer.concat([f1, f2])

// 自检：Node 能解回自己（确保 oracle 本身可信）
const selfCheck = zstdDecompressSync(f1).toString('utf-8')
if (selfCheck !== first) {
  throw new Error(`oracle 自检失败：${JSON.stringify(selfCheck)}`)
}

const out = {
  frames,
  concatHex: concat.toString('hex'),
  concatText: first + second,
  firstFrameText: first,
  // 记录 Node 的 zstd 参数（供 Go 侧核对）
  checksumFlag: constants.ZSTD_c_checksumFlag,
}
mkdirSync(here, { recursive: true })
writeFileSync(join(here, 'frames.json'), JSON.stringify(out, null, 2) + '\n')
const sha = createHash('sha256').update(JSON.stringify(out)).digest('hex')
console.error(`zstd oracle：${Object.keys(frames).length} 帧 + 1 拼接 — sha256 ${sha.slice(0, 16)}`)
