/**
 * TUI image attachment loader — turns an on-disk image path into a base64 data URL
 * suitable for the vision model pipeline.
 *
 * Terminals can only bracketed-paste text, so users paste an image file path; this
 * module reads the file, validates the format, and optionally downscales it so the
 * payload stays under the server cap.
 *
 * 格式纪律：TUI 是进程内直调 agent.run（绕过 server 的 validateImagesPayload），
 * 所以这里必须自己守住 provider 安全格式。bmp/tiff 即使没超体积也要转 PNG——
 * provider 常态拒收这两类（400/500），会触发客户端「全量剥图重试」，用户这一轮
 * 所有图都看不到。
 *
 * 归一化走 PNG/JPEG 双阶梯（见 renderProviderImage）：照片类图重编码成 PNG 常比
 * 原图还大，只走 PNG 等于「压了但没压下去」——竞品（Grok re_encode_under_limit /
 * Kimi encodeWithinBudget）都是两条编码路径取更小。
 */

import { readFile } from 'node:fs/promises'
import { basename, extname, join } from 'node:path'
import {
  makeImageTempDir,
  pickSmallerImage,
  pngHasAlpha,
  removeImageTempDir,
  resizeCandidates,
  resizeJpegCandidates,
  runImageTool,
  runImageToolAuto,
  toPngCandidates,
  type RenderedImage,
} from './image-tool.js'

/** Provider cap: 10 MB decoded per image (matches common vision API limits). */
export const MAX_IMAGE_BYTES = 10 * 1024 * 1024
/**
 * 单图 ingest 归一化软目标——**按请求体直觉标定，而不是按单图内联上限**。
 *
 * 10MB 只保证「单张图不超过 provider 的单图内联上限」，不代表请求体能发出去：
 * 4 张 10MB ≈ 53MB base64，而多数网关 body 上限只有 4MB（见 utils/sanitize.ts）。
 * 外部标定：Grok Build 1.5MB/图（为其 50MB 推理代理留 ~25 张余量）、Kimi Code
 * 基线 3.75MB/图、xAI read_file 图片按 1.5MB 重编码。取 3.75MB：显著压低四图
 * 总量，又不过度牺牲截图清晰度。
 *
 * 有图像工具就压到该目标；压不动时保留原图（≤ 硬上限），由请求体护栏
 * （provider.maxBodyBytes）或 413 恢复路径兜底——Grok 的 ReEncodingOversized 同款。
 */
export const TARGET_IMAGE_BYTES = 3.75 * 1024 * 1024
/** Long-edge clamp. 1568px keeps token cost bounded while staying legible. */
export const MAX_EDGE = 1568
/** Max number of images per prompt (matches desktop Composer). */
export const MAX_IMAGES = 4

/** 可直接交给 provider 的格式（OpenAI ∩ Anthropic 白名单）。 */
const PROVIDER_SAFE_MIMES = new Set(['image/png', 'image/jpeg', 'image/webp', 'image/gif'])

/** True when the MIME can be handed to a vision provider without transcoding. */
export function isProviderSafeImageMime(mime: string): boolean {
  return PROVIDER_SAFE_MIMES.has(mime)
}

const IMAGE_MIMES: Record<string, string> = {
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.jpeg': 'image/jpeg',
  '.webp': 'image/webp',
  '.gif': 'image/gif',
  '.tiff': 'image/tiff',
  '.tif': 'image/tiff',
  '.bmp': 'image/bmp',
}

export interface ImageAttachment {
  /** data:image/...;base64,... */
  dataUrl: string
  mime: string
  name: string
}

export interface LoadImageOptions {
  /** 硬上限（决定是否必须压缩/拒绝）；缺省 MAX_IMAGE_BYTES。 */
  maxBytes?: number
  /** ingest 软目标；超过即尝试归一化，压不动则保留原图。缺省 TARGET_IMAGE_BYTES。 */
  targetBytes?: number
  maxEdge?: number
  /** PNG-producing resize hook; injectable so the oversized-image path is testable. */
  resizeImage?: (path: string, maxEdge: number) => Promise<Buffer | null>
  /** bmp/tiff → PNG hook (format conversion only, never upscales); injectable for tests. */
  transcodeImage?: (path: string) => Promise<Buffer | null>
  /** JPEG-producing resize hook（阶梯的另一半）; injectable for tests. */
  resizeJpeg?: (path: string, maxEdge: number) => Promise<Buffer | null>
}

/**
 * 仅按 magic bytes 识别 MIME；不识别即返回 null。
 * 不做扩展名 fallback——真实图片（png/jpeg/webp/gif/tiff/bmp）都有可靠 magic，
 * 任意内容改名 .png 不应进入转码流程。保留 filePath 参数仅为兼容既有调用签名。
 */
export function detectImageMime(buf: Buffer, _filePath: string): string | null {
  if (buf.length >= 8) {
    // PNG: 89 50 4E 47
    if (buf[0] === 0x89 && buf[1] === 0x50 && buf[2] === 0x4E && buf[3] === 0x47) {
      return 'image/png'
    }
    // JPEG: FF D8 FF
    if (buf[0] === 0xFF && buf[1] === 0xD8 && buf[2] === 0xFF) {
      return 'image/jpeg'
    }
    // WebP: RIFF....WEBP
    if (
      buf.length >= 12 &&
      buf[0] === 0x52 &&
      buf[1] === 0x49 &&
      buf[2] === 0x46 &&
      buf[3] === 0x46 &&
      buf[8] === 0x57 &&
      buf[9] === 0x45 &&
      buf[10] === 0x42 &&
      buf[11] === 0x50
    ) {
      return 'image/webp'
    }
    // GIF: GIF87a or GIF89a
    if (buf[0] === 0x47 && buf[1] === 0x49 && buf[2] === 0x46) {
      return 'image/gif'
    }
    // TIFF: II (little-endian) or MM (big-endian) at offset 0, magic 42 at offset 2-3
    if (
      (buf[0] === 0x49 && buf[1] === 0x49 && buf[2] === 0x2A && buf[3] === 0x00) ||
      (buf[0] === 0x4D && buf[1] === 0x4D && buf[2] === 0x00 && buf[3] === 0x2A)
    ) {
      return 'image/tiff'
    }
    // BMP: BM
    if (buf[0] === 0x42 && buf[1] === 0x4D) {
      return 'image/bmp'
    }
  }
  return null
}

/** Returns true if the file extension looks like a supported image. */
export function looksLikeImagePath(text: string): boolean {
  const ext = extname(text.trim()).toLowerCase()
  return ext in IMAGE_MIMES
}

async function trySystemResize(path: string, maxEdge: number): Promise<Buffer | null> {
  const dir = await makeImageTempDir()
  const outPath = join(dir, 'out.png')
  try {
    // runImageTool 一体化完成「执行 + 读回 + PNG 校验」，失败返回 null
    return await runImageTool(resizeCandidates(path, outPath, maxEdge), outPath)
  } finally {
    await removeImageTempDir(dir)
  }
}

async function trySystemTranscode(path: string): Promise<Buffer | null> {
  const dir = await makeImageTempDir()
  const outPath = join(dir, 'out.png')
  try {
    // toPngCandidates 只转格式、不缩放（resizeCandidates 的 sips -Z 会把小图放大）。
    return await runImageTool(toPngCandidates(path, outPath), outPath)
  } finally {
    await removeImageTempDir(dir)
  }
}

async function trySystemResizeJpeg(path: string, maxEdge: number): Promise<Buffer | null> {
  const dir = await makeImageTempDir()
  const outPath = join(dir, 'out.jpg')
  try {
    return (await runImageToolAuto(resizeJpegCandidates(path, outPath, maxEdge), outPath))?.data ?? null
  } finally {
    await removeImageTempDir(dir)
  }
}

/**
 * 把图片渲染成 provider 可消费的形态：**PNG 与 JPEG 各渲染一遍，取更小的那个**
 * （Grok `re_encode_under_limit` 的同款选择律：两条编码路径都试，谁小用谁，平局取
 * 无损的 PNG）。返回 null = 两种都压不动（调用方按硬/软语义决定：硬上限场景拒绝，
 * 软目标场景保留原图）。
 *
 * - transcodeOnly（bmp/tiff）：PNG 侧先走「只转格式不缩放」；转出 PNG 仍超 maxBytes
 *   （照片类 BMP/TIFF 转 PNG 会膨胀）才回落缩放路径。JPEG 侧一步到位（缩放+编码），
 *   照片类 BMP/TIFF 的重编码收益全在这里。
 * - oversize（其余格式超 maxBytes 或软目标）：两侧都缩放到 maxEdge 再编码。
 * - allowJpeg=false（源是带 alpha 的 PNG）：只保留 PNG——JPEG 存不下 alpha，
 *   编码器会把透明压成黑底，模型看到的是「图变黑了」。
 */
async function renderProviderImage(
  path: string,
  maxEdge: number,
  maxBytes: number,
  resizeImage: (path: string, maxEdge: number) => Promise<Buffer | null>,
  transcodeImage: (path: string) => Promise<Buffer | null>,
  resizeJpeg: (path: string, maxEdge: number) => Promise<Buffer | null>,
  opts: { transcodeOnly: boolean; allowJpeg: boolean },
): Promise<RenderedImage | null> {
  let png: Buffer | null = null
  if (opts.transcodeOnly) {
    const converted = await transcodeImage(path)
    if (converted && converted.length <= maxBytes) png = converted
  }
  if (!png) {
    const rendered = await resizeImage(path, maxEdge)
    if (rendered && rendered.length <= maxBytes) png = rendered
  }

  const jpeg = opts.allowJpeg ? await resizeJpeg(path, maxEdge) : null
  return pickSmallerImage(png, jpeg, maxBytes)
}

const TOOL_HINT = 'Install an image tool (sips on macOS, ImageMagick on Linux/Windows).'

/**
 * Load an image from disk and return it as a base64 data URL.
 *
 * - Validates format by magic bytes (no extension fallback).
 * - Rejects unsupported formats.
 * - bmp/tiff always transcode to PNG (provider allowlist), even when small.
 * - If the decoded file exceeds maxBytes, attempts to resize to maxEdge using
 *   system tools (sips on macOS, ImageMagick elsewhere).
 */
export async function loadImageAttachment(
  absolutePath: string,
  options: LoadImageOptions = {},
): Promise<ImageAttachment> {
  const maxBytes = options.maxBytes ?? MAX_IMAGE_BYTES
  const targetBytes = options.targetBytes ?? TARGET_IMAGE_BYTES
  const maxEdge = options.maxEdge ?? MAX_EDGE

  const raw = await readFile(absolutePath)
  let buf: Buffer = Buffer.from(raw) as Buffer
  let mime = detectImageMime(buf, absolutePath)
  if (!mime) {
    throw new Error(`Unsupported image format: ${absolutePath}`)
  }

  const transcodeOnly = !isProviderSafeImageMime(mime)
  const oversized = buf.length > maxBytes
  if (oversized || transcodeOnly || buf.length > targetBytes) {
    const rendered = await renderProviderImage(
      absolutePath,
      maxEdge,
      maxBytes,
      options.resizeImage ?? trySystemResize,
      options.transcodeImage ?? trySystemTranscode,
      options.resizeJpeg ?? trySystemResizeJpeg,
      // 带 alpha 的 PNG 不走 JPEG：透明被压成黑底比「图没压小」更糟。
      { transcodeOnly, allowJpeg: !(mime === 'image/png' && pngHasAlpha(buf)) },
    )
    if (rendered) {
      // 产物格式由 magic 判定后回传（PNG/JPEG 取更小），不得按扩展名假定。
      buf = rendered.data
      mime = rendered.mime
    } else if (oversized) {
      throw new Error(`Image too large and no image tool is available to compress it. ${TOOL_HINT}`)
    } else if (transcodeOnly) {
      throw new Error(
        `This image format (bmp/tiff) is not supported by vision providers and no image tool is available to convert it to PNG. ${TOOL_HINT}`,
      )
    } else if (process.env['RIVET_DEBUG']) {
      // 软目标未达成但仍在硬上限内：保留原图（Grok ReEncodingOversized 同款），
      // 否则「没装 ImageMagick」会把一张 4MB 的截图变成不可附加。
      console.error(`[image-attach] ${basename(absolutePath)} 未能压到 ${targetBytes} 字节，保留原图`)
    }
  }

  const b64 = buf.toString('base64')
  return {
    dataUrl: `data:${mime};base64,${b64}`,
    mime,
    name: basename(absolutePath),
  }
}
