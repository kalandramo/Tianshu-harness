/**
 * image-attach.ts tests.
 */

import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, writeFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { detectImageMime, looksLikeImagePath, loadImageAttachment } from '../image-attach.js'

// 1x1 transparent PNG
const PNG_B64 = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAAC0lEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=='

function withTempPng() {
  const dir = mkdtempSync(join(tmpdir(), 'rivet-img-'))
  const path = join(dir, 'test.png')
  writeFileSync(path, Buffer.from(PNG_B64, 'base64'))
  return {
    path,
    cleanup: () => { rmSync(dir, { recursive: true, force: true }) },
  }
}

test('detectImageMime recognizes PNG', () => {
  const buf = Buffer.from(PNG_B64, 'base64')
  assert.equal(detectImageMime(buf, '/foo/bar.png'), 'image/png')
})

test('detectImageMime returns null when magic is unrecognized (no extension fallback)', () => {
  const buf = Buffer.from('not a real image')
  assert.equal(detectImageMime(buf, '/foo/bar.jpg'), null)
})

test('looksLikeImagePath recognizes supported extensions', () => {
  assert.equal(looksLikeImagePath('/tmp/shot.png'), true)
  assert.equal(looksLikeImagePath('/tmp/shot.JPG'), true)
  assert.equal(looksLikeImagePath('/tmp/shot.webp'), true)
  assert.equal(looksLikeImagePath('/tmp/scan.tiff'), true)
  assert.equal(looksLikeImagePath('/tmp/scan.TIF'), true)
  assert.equal(looksLikeImagePath('/tmp/scan.BMP'), true)
  assert.equal(looksLikeImagePath('/tmp/shot.txt'), false)
})

test('loadImageAttachment loads a valid PNG into a data URL', async () => {
  const { path, cleanup } = withTempPng()
  try {
    const attachment = await loadImageAttachment(path)
    assert.ok(attachment.dataUrl.startsWith('data:image/png;base64,'))
    assert.equal(attachment.mime, 'image/png')
    assert.equal(attachment.name, 'test.png')
  } finally {
    cleanup()
  }
})

test('loadImageAttachment rejects unsupported formats', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'rivet-img-'))
  const path = join(dir, 'test.txt')
  writeFileSync(path, 'hello world')
  try {
    await assert.rejects(loadImageAttachment(path), /Unsupported image format/)
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})

test('loadImageAttachment advertises resized JPEG bytes as PNG', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'rivet-img-'))
  const path = join(dir, 'large.jpg')
  const oversizedJpeg = Buffer.concat([
    Buffer.from([0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46]),
    Buffer.alloc(192),
  ])
  writeFileSync(path, oversizedJpeg)
  let resizeCalls = 0
  try {
    const attachment = await loadImageAttachment(path, {
      maxBytes: 100,
      maxEdge: 32,
      resizeImage: async () => {
        resizeCalls++
        return Buffer.from(PNG_B64, 'base64')
      },
      resizeJpeg: async () => null,
    })
    const encoded = attachment.dataUrl.split(',')[1]!
    const payload = Buffer.from(encoded, 'base64')

    assert.equal(resizeCalls, 1)
    assert.equal(attachment.mime, 'image/png')
    assert.ok(attachment.dataUrl.startsWith('data:image/png;base64,'))
    assert.equal(detectImageMime(payload, path), 'image/png')
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})

test('loadImageAttachment 把 bmp/tiff 一律转 PNG（provider 不支持的原样格式不得出境）', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'rivet-img-'))
  const bmpPath = join(dir, 'shot.bmp')
  // BMP magic 'BM' + padding：detectImageMime 只认 magic，不看文件长度
  writeFileSync(bmpPath, Buffer.concat([Buffer.from('BM'), Buffer.alloc(62)]))
  let transcodeCalls = 0
  let resizeCalls = 0
  try {
    const attachment = await loadImageAttachment(bmpPath, {
      transcodeImage: async () => {
        transcodeCalls++
        return Buffer.from(PNG_B64, 'base64')
      },
      resizeImage: async () => {
        resizeCalls++
        return null
      },
      resizeJpeg: async () => null,
    })
    assert.equal(transcodeCalls, 1, '小体积 bmp 也必须走转码（不是只在超限时）')
    assert.equal(resizeCalls, 0, '转码成功后不应再走缩放')
    assert.equal(attachment.mime, 'image/png')
    assert.ok(attachment.dataUrl.startsWith('data:image/png;base64,'))
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})

test('loadImageAttachment 转码 PNG 仍超 maxBytes → 回落缩放路径', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'rivet-img-'))
  const tiffPath = join(dir, 'photo.tiff')
  writeFileSync(tiffPath, Buffer.from('49492a000800000000000000', 'hex'))
  let transcodeCalls = 0
  let resizeCalls = 0
  try {
    const attachment = await loadImageAttachment(tiffPath, {
      maxBytes: 100,
      transcodeImage: async () => {
        transcodeCalls++
        return Buffer.alloc(256, 0x89) // 转出 PNG 超 maxBytes
      },
      resizeImage: async () => {
        resizeCalls++
        return Buffer.from(PNG_B64, 'base64')
      },
      resizeJpeg: async () => null,
    })
    assert.equal(transcodeCalls, 1)
    assert.equal(resizeCalls, 1, '转码产物超限必须回落缩放，不能直接放行')
    assert.equal(attachment.mime, 'image/png')
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})

test('loadImageAttachment 转码 bmp/tiff 失败 → 明确报错，不得回落原格式', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'rivet-img-'))
  const tiffPath = join(dir, 'scan.tiff')
  writeFileSync(tiffPath, Buffer.from('49492a000800000000000000', 'hex'))
  try {
    await assert.rejects(
      loadImageAttachment(tiffPath, {
        transcodeImage: async () => null,
        resizeImage: async () => null,
        resizeJpeg: async () => null,
      }),
      /bmp\/tiff|convert it to PNG/,
      '无图像工具时必须拒绝，而不是把 tiff 原样发给 provider',
    )
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})

test('loadImageAttachment 不转码 provider 安全的小图（不做无谓的系统工具调用）', async () => {
  const { path, cleanup } = withTempPng()
  let resizeCalls = 0
  try {
    const attachment = await loadImageAttachment(path, {
      resizeImage: async () => {
        resizeCalls++
        return null
      },
    })
    assert.equal(attachment.mime, 'image/png')
    assert.equal(resizeCalls, 0, '小体积 png 不应触发转码/缩放')
  } finally {
    cleanup()
  }
})

test('loadImageAttachment 超过软目标（未超硬上限）也归一化到 PNG', async () => {
  // 4 张 10MB 图 ≈ 53MB base64，很多网关 body 上限只有 4MB——竞品（Grok 1.5MB /
  // Kimi 3.75MB）都在 ingest 时把单图压到远低于硬上限。
  const dir = mkdtempSync(join(tmpdir(), 'rivet-img-'))
  const path = join(dir, 'screenshot.jpg')
  const buf = Buffer.concat([
    Buffer.from([0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46]),
    Buffer.alloc(192),
  ])
  writeFileSync(path, buf)
  let resizeCalls = 0
  try {
    const attachment = await loadImageAttachment(path, {
      maxBytes: 10 * 1024 * 1024,
      targetBytes: 100,
      resizeImage: async () => {
        resizeCalls++
        return Buffer.from(PNG_B64, 'base64')
      },
      resizeJpeg: async () => null,
    })
    assert.equal(resizeCalls, 1, '超软目标即触发归一化')
    assert.equal(attachment.mime, 'image/png')
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})

test('loadImageAttachment 软目标压不动 → 保留原图（≤ 硬上限），不拒绝用户', async () => {
  // Grok ReEncodingOversized 语义：没装图像工具不该让一张 4MB 截图变成不可附加；
  // 总量问题交给请求体护栏（provider.maxBodyBytes）兜底。
  const dir = mkdtempSync(join(tmpdir(), 'rivet-img-'))
  const path = join(dir, 'photo.jpg')
  writeFileSync(path, Buffer.concat([
    Buffer.from([0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46]),
    Buffer.alloc(192),
  ]))
  try {
    const attachment = await loadImageAttachment(path, {
      maxBytes: 10 * 1024 * 1024,
      targetBytes: 100,
      resizeImage: async () => null,
      resizeJpeg: async () => null,
    })
    assert.equal(attachment.mime, 'image/jpeg', '压不动时保留原格式，而不是抛错')
    assert.ok(attachment.dataUrl.startsWith('data:image/jpeg;base64,'))
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})

test('loadImageAttachment 硬上限超限且压不动 → 仍然拒绝（软/硬语义不能混）', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'rivet-img-'))
  const path = join(dir, 'huge.jpg')
  writeFileSync(path, Buffer.concat([
    Buffer.from([0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46]),
    Buffer.alloc(192),
  ]))
  try {
    await assert.rejects(
      loadImageAttachment(path, { maxBytes: 100, resizeImage: async () => null, resizeJpeg: async () => null }),
      /Image too large/,
    )
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})

// ── PNG/JPEG 取更小阶梯（Grok re_encode_under_limit 同款选择律）─────────────────

/** 假 JPEG 源（8 字节 magic，detectImageMime 只认 magic）。 */
function fakeJpegSource(size = 200): Buffer {
  return Buffer.concat([
    Buffer.from([0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46]),
    Buffer.alloc(Math.max(0, size - 8)),
  ])
}

test('阶梯：JPEG 明显更小 → 取 JPEG（照片类图重编码成 PNG 会膨胀，只走 PNG 等于没压）', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'rivet-img-'))
  const path = join(dir, 'photo.jpg')
  writeFileSync(path, fakeJpegSource())
  try {
    const attachment = await loadImageAttachment(path, {
      maxBytes: 10 * 1024 * 1024,
      targetBytes: 100,
      resizeImage: async () => Buffer.alloc(180, 1), // 合格的 PNG，但更大
      resizeJpeg: async () => Buffer.alloc(60, 2),
    })
    assert.equal(attachment.mime, 'image/jpeg', '两条编码路径取更小者')
    assert.ok(attachment.dataUrl.startsWith('data:image/jpeg;base64,'))
    assert.equal(Buffer.from(attachment.dataUrl.split(',')[1]!, 'base64').length, 60)
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})

test('阶梯：字节相同 → 取 PNG（无损优先）', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'rivet-img-'))
  const path = join(dir, 'shot.jpg')
  writeFileSync(path, fakeJpegSource())
  try {
    const attachment = await loadImageAttachment(path, {
      maxBytes: 10 * 1024 * 1024,
      targetBytes: 100,
      resizeImage: async () => Buffer.alloc(64, 1),
      resizeJpeg: async () => Buffer.alloc(64, 2),
    })
    assert.equal(attachment.mime, 'image/png', '平局不能白丢无损画质')
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})

test('阶梯：JPEG 产物超硬上限 → 回落到合格的 PNG', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'rivet-img-'))
  const path = join(dir, 'shot.jpg')
  writeFileSync(path, fakeJpegSource())
  try {
    const attachment = await loadImageAttachment(path, {
      maxBytes: 100,
      resizeImage: async () => Buffer.from(PNG_B64, 'base64'), // 70 字节，合格
      resizeJpeg: async () => Buffer.alloc(4096, 2), // 超预算，作废
    })
    assert.equal(attachment.mime, 'image/png')
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})

test('阶梯：带 alpha 的 PNG 不交给 JPEG（透明被压成黑底比「没压小」更糟）', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'rivet-img-'))
  const path = join(dir, 'transparent.png')
  writeFileSync(path, Buffer.from(PNG_B64, 'base64')) // color type 6（RGBA）
  let jpegCalls = 0
  try {
    const attachment = await loadImageAttachment(path, {
      targetBytes: 10, // 强制进归一化路径
      resizeImage: async () => Buffer.from(PNG_B64, 'base64'),
      resizeJpeg: async () => {
        jpegCalls++
        return Buffer.alloc(8, 2)
      },
    })
    assert.equal(jpegCalls, 0, '有 alpha 时不得尝试 JPEG')
    assert.equal(attachment.mime, 'image/png')
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})

test('阶梯：bmp/tiff 转码场景也参与取更小（照片类 BMP 转 JPEG 收益全在这）', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'rivet-img-'))
  const bmpPath = join(dir, 'photo.bmp')
  writeFileSync(bmpPath, Buffer.concat([Buffer.from('BM'), Buffer.alloc(400)]))
  try {
    const attachment = await loadImageAttachment(bmpPath, {
      transcodeImage: async () => Buffer.alloc(300, 1), // 转出 PNG，仍比 JPEG 大
      resizeImage: async () => null,
      resizeJpeg: async () => Buffer.alloc(90, 2),
    })
    assert.equal(attachment.mime, 'image/jpeg', 'provider 白名单内取更小者，不必死守 PNG')
    assert.ok(attachment.dataUrl.startsWith('data:image/jpeg;base64,'))
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})
