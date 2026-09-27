/**
 * image-tool.ts tests：isCompletePng / isCompleteJpeg 完整性校验 + runImageTool 截断
 * fallback + PNG/JPEG 选择律 + 全失败时 RIVET_DEBUG 调试输出可观测性。
 */

import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import {
  isCompleteJpeg,
  isCompletePng,
  pickSmallerImage,
  pngHasAlpha,
  resizeCandidates,
  resizeJpegCandidates,
  runImageTool,
  runImageToolAuto,
} from '../image-tool.js'

// 1x1 transparent PNG（含完整 IHDR + IEND；IHDR color type = 6 / RGBA）
const PNG_1X1 = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAAC0lEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=='
const PNG_BUF = Buffer.from(PNG_1X1, 'base64')

/**
 * 最小 JPEG 骨架：SOI + APP0 + SOF0 + SOS（+ EOI）。没有熵数据——校验器只读段头，
 * 这样测试不依赖真实编码器。
 */
function minimalJpeg(width = 1, height = 1, opts: { eoi?: boolean } = {}): Buffer {
  const app0 = Buffer.from([
    0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46, 0x00,
    0x01, 0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00,
  ])
  const sof0 = Buffer.from([
    0xFF, 0xC0, 0x00, 0x11, 0x08, height >> 8, height & 0xFF, width >> 8, width & 0xFF,
    0x03, 0x01, 0x11, 0x00, 0x02, 0x11, 0x01, 0x03, 0x11, 0x01,
  ])
  const sos = Buffer.from([0xFF, 0xDA, 0x00, 0x0C, 0x03, 0x01, 0x00, 0x02, 0x11, 0x03, 0x11, 0x00, 0x3F, 0x00])
  const parts = [Buffer.from([0xFF, 0xD8]), app0, sof0, sos]
  if (opts.eoi !== false) parts.push(Buffer.from([0xFF, 0xD9]))
  return Buffer.concat(parts)
}

/** 用 node -e 构造假命令，不依赖 sips/ImageMagick 真实存在。 */
function fakeCmd(script: string, ...extraArgs: string[]): { bin: string; args: string[] } {
  return { bin: process.execPath, args: ['-e', script, ...extraArgs] }
}

/** 写出指定字节的假命令脚本。 */
function writeBytesScript(buf: Buffer): string {
  return `require('node:fs').writeFileSync(process.argv[1], Buffer.from('${buf.toString('base64')}','base64'))`
}

function withTempDir(fn: (dir: string) => Promise<void>): Promise<void> {
  const dir = mkdtempSync(join(tmpdir(), 'rivet-imgtool-test-'))
  return fn(dir).finally(() => { rmSync(dir, { recursive: true, force: true }) })
}

// ── isCompletePng ────────────────────────────────────────────────────────────

test('isCompletePng：仅 signature（8 字节）不算完整 PNG', () => {
  assert.equal(isCompletePng(PNG_BUF.subarray(0, 8)), false)
})

test('isCompletePng：截断 IHDR 不算完整 PNG', () => {
  // 截到 IHDR 数据中间（signature + length/type + 部分 data）
  assert.equal(isCompletePng(PNG_BUF.subarray(0, 20)), false)
  // IHDR 宽度为 0 的伪造 chunk
  const zeroWidth = Buffer.from(PNG_BUF)
  zeroWidth.writeUInt32BE(0, 16)
  assert.equal(isCompletePng(zeroWidth), false)
})

test('isCompletePng：缺 IEND 不算完整 PNG', () => {
  assert.equal(isCompletePng(PNG_BUF.subarray(0, PNG_BUF.length - 12)), false)
})

test('isCompletePng：完整 PNG（PNG_1X1）通过', () => {
  assert.equal(isCompletePng(PNG_BUF), true)
})

test('isCompletePng：非 PNG 内容不通过', () => {
  assert.equal(isCompletePng(Buffer.from('not a real image at all, definitely long enough to pass length gate........')), false)
})

// ── runImageTool：截断 PNG → fallback 下一候选 ───────────────────────────────

test('runImageTool：产出截断 PNG 时 fallback 到下一候选', async () => {
  await withTempDir(async (dir) => {
    const out = join(dir, 'out.png')
    // 第一个候选 exit 0 但只写出 signature + 截断 IHDR；第二个产出完整 PNG
    const result = await runImageTool([
      fakeCmd(writeBytesScript(PNG_BUF.subarray(0, 20)), out),
      fakeCmd(writeBytesScript(PNG_BUF), out),
    ], out)
    assert.deepEqual(result, PNG_BUF)
  })
})

// ── RIVET_DEBUG 失败可观测性 ─────────────────────────────────────────────────

async function captureConsoleError(fn: () => Promise<void>): Promise<string[]> {
  const origError = console.error
  const lines: string[] = []
  console.error = (...args: unknown[]) => { lines.push(args.map(String).join(' ')) }
  try {
    await fn()
  } finally {
    console.error = origError
  }
  return lines
}

test('runImageTool：全部失败且 RIVET_DEBUG 非空时输出一行调试信息', async () => {
  const origDebug = process.env.RIVET_DEBUG
  process.env.RIVET_DEBUG = '1'
  try {
    await withTempDir(async (dir) => {
      const out = join(dir, 'out.png')
      const lines = await captureConsoleError(async () => {
        const result = await runImageTool([fakeCmd('process.exit(1)', out)], out)
        assert.equal(result, null)
      })
      assert.equal(lines.length, 1)
      assert.ok(lines[0]?.includes('[image-tool]'))
    })
  } finally {
    if (origDebug === undefined) delete process.env.RIVET_DEBUG
    else process.env.RIVET_DEBUG = origDebug
  }
})

test('runImageTool：全部失败但无 RIVET_DEBUG 时保持静默', async () => {
  const origDebug = process.env.RIVET_DEBUG
  delete process.env.RIVET_DEBUG
  try {
    await withTempDir(async (dir) => {
      const out = join(dir, 'out.png')
      const lines = await captureConsoleError(async () => {
        const result = await runImageTool([fakeCmd('process.exit(1)', out)], out)
        assert.equal(result, null)
      })
      assert.equal(lines.length, 0)
    })
  } finally {
    if (origDebug !== undefined) process.env.RIVET_DEBUG = origDebug
  }
})

test('resizeCandidates：macOS sips 缩放时显式输出 PNG', () => {
  assert.deepEqual(resizeCandidates('/tmp/in.jpg', '/tmp/out.png', 1568, 'darwin')[0], {
    bin: 'sips',
    args: ['-Z', '1568', '-s', 'format', 'png', '/tmp/in.jpg', '--out', '/tmp/out.png'],
  })
})

// ── isCompleteJpeg ───────────────────────────────────────────────────────────

test('isCompleteJpeg：完整骨架（SOI + SOF + EOI）通过', () => {
  assert.equal(isCompleteJpeg(minimalJpeg()), true)
  assert.equal(isCompleteJpeg(minimalJpeg(640, 480)), true)
})

test('isCompleteJpeg：缺 EOI（工具写半个文件）不算完整', () => {
  assert.equal(isCompleteJpeg(minimalJpeg(1, 1, { eoi: false })), false)
})

test('isCompleteJpeg：宽高为 0 的伪造帧头不算完整', () => {
  assert.equal(isCompleteJpeg(minimalJpeg(0, 0)), false)
})

test('isCompleteJpeg：非 JPEG 内容（含 PNG 字节）不通过', () => {
  assert.equal(isCompleteJpeg(PNG_BUF), false)
  assert.equal(isCompleteJpeg(Buffer.alloc(64, 0x41)), false)
  // 只有 SOI、没有帧头
  assert.equal(isCompleteJpeg(Buffer.concat([Buffer.from([0xFF, 0xD8]), Buffer.alloc(32), Buffer.from([0xFF, 0xD9])])), false)
})

// ── pngHasAlpha ──────────────────────────────────────────────────────────────

test('pngHasAlpha：RGBA（color type 6）为真，RGB（2）为假', () => {
  assert.equal(pngHasAlpha(PNG_BUF), true)
  const rgb = Buffer.from(PNG_BUF)
  rgb[25] = 2 // IHDR color type：真值 2 = truecolor 无 alpha
  assert.equal(pngHasAlpha(rgb), false, '无 alpha 的 PNG 可以进 JPEG 阶梯')
})

test('pngHasAlpha：非 PNG / 结构不完整一律假', () => {
  assert.equal(pngHasAlpha(minimalJpeg()), false)
  assert.equal(pngHasAlpha(PNG_BUF.subarray(0, 20)), false)
})

// ── pickSmallerImage：PNG/JPEG 取更小 ────────────────────────────────────────

test('pickSmallerImage：两者都合格取更小者，平局取 PNG', () => {
  const png = Buffer.alloc(100, 1)
  const jpeg = Buffer.alloc(60, 2)
  assert.deepEqual(pickSmallerImage(png, jpeg, 1024), { data: jpeg, mime: 'image/jpeg' })
  assert.deepEqual(pickSmallerImage(png, Buffer.alloc(100, 2), 1024), { data: png, mime: 'image/png' })
})

test('pickSmallerImage：超预算的候选作废；都不合格返回 null', () => {
  const png = Buffer.alloc(100, 1)
  const jpeg = Buffer.alloc(40, 2)
  assert.deepEqual(pickSmallerImage(png, jpeg, 50), { data: jpeg, mime: 'image/jpeg' })
  assert.deepEqual(pickSmallerImage(png, null, 50), null)
  assert.deepEqual(pickSmallerImage(png, Buffer.alloc(60, 2), 50), null, '两个都超预算 = 压不动')
  assert.deepEqual(pickSmallerImage(null, jpeg, 1024), { data: jpeg, mime: 'image/jpeg' })
  assert.deepEqual(pickSmallerImage(null, null, 1024), null)
})

// ── resizeJpegCandidates ─────────────────────────────────────────────────────

test('resizeJpegCandidates：macOS sips 缩放并显式输出 JPEG（带质量档）', () => {
  assert.deepEqual(resizeJpegCandidates('/tmp/in.png', '/tmp/out.jpg', 1568, 'darwin')[0], {
    bin: 'sips',
    args: ['-Z', '1568', '-s', 'format', 'jpeg', '-s', 'formatOptions', '85', '/tmp/in.png', '--out', '/tmp/out.jpg'],
  })
})

test('resizeJpegCandidates：win32 首选 ImageMagick（sips 不存在），含 -quality', () => {
  const [first] = resizeJpegCandidates('C:\\in.png', 'C:\\out.jpg', 1568, 'win32')
  assert.equal(first?.bin, 'magick')
  assert.ok(first?.args.includes('-quality'))
})

// ── runImageTool / runImageToolAuto：格式纪律 ────────────────────────────────

test('runImageToolAuto：JPEG 产物回传真实 MIME（不得按扩展名假定为 PNG）', async () => {
  await withTempDir(async (dir) => {
    const out = join(dir, 'out.jpg')
    const jpeg = minimalJpeg(4, 4)
    const result = await runImageToolAuto([fakeCmd(writeBytesScript(jpeg), out)], out)
    assert.equal(result?.mime, 'image/jpeg')
    assert.deepEqual(result?.data, jpeg)
  })
})

test('runImageTool：PNG-only 纪律——写出 JPEG 的候选不算合格', async () => {
  await withTempDir(async (dir) => {
    const out = join(dir, 'out.png')
    const result = await runImageTool([fakeCmd(writeBytesScript(minimalJpeg()), out)], out)
    assert.equal(result, null, '展示路径要的是 PNG，JPEG 字节不能冒充')
  })
})

test('runImageToolAuto：截断 JPEG 后回落到下一候选（候选级隔离对两种格式同规）', async () => {
  await withTempDir(async (dir) => {
    const out = join(dir, 'out.jpg')
    const truncated = minimalJpeg(1, 1, { eoi: false })
    const good = minimalJpeg(8, 8)
    const result = await runImageToolAuto([
      fakeCmd(writeBytesScript(truncated), out),
      fakeCmd(writeBytesScript(good), out),
    ], out)
    assert.deepEqual(result?.data, good)
  })
})
