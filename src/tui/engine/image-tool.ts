/**
 * 系统图像工具共享执行器 — 平台感知的候选命令构造与 fallback 执行、临时目录管理，
 * 供 image-attach（缩放/转码，PNG+JPEG 双族）与 term-image（只转 PNG）两条路径共用，
 * 避免两套超时/清理策略漂移。
 *
 * 产物校验按格式分开（isCompletePng / isCompleteJpeg），执行器只有一份实现：
 * 展示路径限定 PNG（runImageTool），附件路径两族都收（runImageToolAuto）——
 * 「PNG/JPEG 取更小」的选择律见 pickSmallerImage。
 *
 * 候选顺序按平台区分（见 toPngCandidates / resizeCandidates）：
 * - darwin/linux：sips（macOS 内置，Linux 上不存在会自然失败进 fallback）
 *   → ImageMagick v7（magick）→ v6（convert）。
 * - win32：magick → PowerShell + System.Drawing 兜底。不含 sips（不存在），
 *   也不含 convert——避免撞名系统工具 C:\Windows\System32\convert.exe
 *   （FAT→NTFS 转换）；PowerShell 为 Windows 自带，覆盖未装 ImageMagick 的场景。
 *   注意 System.Drawing 不支持 WebP（无 WebP 编解码器）——win32 未装 ImageMagick
 *   时 WebP 转换必然失败：所有候选跑完返回 null，调用方退回文本占位。失败
 *   不再是静默的：全部候选失败且 RIVET_DEBUG 非空时向 stderr 打一行调试输出
 *   （见 runImageTool 末尾）。
 *
 * 临时目录约定：每次转换一个 `rivet-imgtool-*` 独立目录，finally 中删除；
 * 进程崩溃/SIGKILL 残留由下一次转换时的惰性清扫兜底（mtime 超过 1 小时即删）。
 */

import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { mkdtemp, readdir, readFile, rm, stat } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const execFileAsync = promisify(execFile)

export const IMAGE_TEMP_DIR_PREFIX = 'rivet-imgtool-'
/** 残留目录惰性清扫阈值。 */
const STALE_MS = 60 * 60 * 1000

export interface ImageToolCommand {
  bin: string
  args: string[]
}

/** PowerShell 单引号字符串字面量：内部 ' 翻倍转义。 */
function psQuote(path: string): string {
  return `'${path.replace(/'/g, "''")}'`
}

/** PowerShell 兜底命令：inbox powershell.exe + System.Drawing，-Command 执行脚本。 */
function powershellCommand(script: string): ImageToolCommand {
  return { bin: 'powershell', args: ['-NoProfile', '-NonInteractive', '-Command', script] }
}

/**
 * 「任意格式 → PNG」转换候选命令（首个成功即采用）。
 * darwin/linux：sips → magick → convert；win32：magick → PowerShell
 * （convert 会撞名系统工具 convert.exe，sips 不存在，均排除）。
 */
export function toPngCandidates(
  inPath: string,
  outPath: string,
  platform: NodeJS.Platform = process.platform,
): ImageToolCommand[] {
  if (platform === 'win32') {
    return [
      { bin: 'magick', args: [inPath, `png:${outPath}`] },
      powershellCommand(
        // $ErrorActionPreference='Stop'：把 non-terminating error 变为终止性，
        // 否则脚本报错仍可能 exit 0；try/finally 保证 $img 释放
        "$ErrorActionPreference='Stop'; " +
        'Add-Type -AssemblyName System.Drawing; ' +
        '$img=$null; ' +
        `try { $img=[System.Drawing.Image]::FromFile(${psQuote(inPath)}); ` +
        `$img.Save(${psQuote(outPath)},[System.Drawing.Imaging.ImageFormat]::Png) } ` +
        'finally { if ($img) { $img.Dispose() } }',
      ),
    ]
  }
  return [
    { bin: 'sips', args: ['-s', 'format', 'png', inPath, '--out', outPath] },
    { bin: 'magick', args: [inPath, `png:${outPath}`] },
    { bin: 'convert', args: [inPath, `png:${outPath}`] },
  ]
}

/**
 * 「等比缩放到长边 ≤ maxEdge 并输出 PNG」候选命令（首个成功即采用）。
 * darwin/linux：sips → magick → convert；win32：magick → PowerShell。
 */
export function resizeCandidates(
  inPath: string,
  outPath: string,
  maxEdge: number,
  platform: NodeJS.Platform = process.platform,
): ImageToolCommand[] {
  if (platform === 'win32') {
    const script = [
      // 'Stop'：non-terminating error 转为终止性，保证失败时 exit code 非 0
      "$ErrorActionPreference='Stop'",
      'Add-Type -AssemblyName System.Drawing',
      '$img=$null;$bmp=$null;$g=$null',
      'try {',
      `$img=[System.Drawing.Image]::FromFile(${psQuote(inPath)})`,
      // 仅当长边超限时缩小（scale 封顶 1），保持宽高比
      `$scale=[Math]::Min(1.0,${maxEdge}/[Math]::Max($img.Width,$img.Height))`,
      '$w=[int][Math]::Max(1,[Math]::Round($img.Width*$scale))',
      '$h=[int][Math]::Max(1,[Math]::Round($img.Height*$scale))',
      '$bmp=New-Object System.Drawing.Bitmap($w,$h)',
      '$g=[System.Drawing.Graphics]::FromImage($bmp)',
      '$g.DrawImage($img,0,0,$w,$h)',
      `$bmp.Save(${psQuote(outPath)},[System.Drawing.Imaging.ImageFormat]::Png)`,
      '} finally {',
      // 逆序释放；空值检查防止部分初始化失败时 finally 二次抛错掩盖原始异常
      'if ($g) { $g.Dispose() }',
      'if ($bmp) { $bmp.Dispose() }',
      'if ($img) { $img.Dispose() }',
      '}',
    ].join(';')
    return [
      { bin: 'magick', args: [inPath, '-resize', `${maxEdge}x${maxEdge}>`, outPath] },
      powershellCommand(script),
    ]
  }
  return [
    { bin: 'sips', args: ['-Z', String(maxEdge), '-s', 'format', 'png', inPath, '--out', outPath] },
    { bin: 'magick', args: [inPath, '-resize', `${maxEdge}x${maxEdge}>`, outPath] },
    { bin: 'convert', args: [inPath, '-resize', `${maxEdge}x${maxEdge}>`, outPath] },
  ]
}

/**
 * JPEG 编码质量。单档而非阶梯：候选只在长边 ≤ maxEdge 的形态下编码，q85 在这档
 * 尺寸上几乎必落在体积预算内；多档 = 多付一次子进程，收益接近零。
 * （竞品的多档阶梯是为「无外部工具、进程内编码」设计的，见 Kimi JPEG_QUALITY_STEPS。）
 */
export const JPEG_QUALITY = 85

/**
 * 「等比缩放到长边 ≤ maxEdge 并输出 JPEG」候选命令（首个成功即采用）。
 * 与 resizeCandidates 同平台序，差异只在编码格式与质量（sips 走 formatOptions，
 * ImageMagick 走 -quality）。
 */
export function resizeJpegCandidates(
  inPath: string,
  outPath: string,
  maxEdge: number,
  platform: NodeJS.Platform = process.platform,
): ImageToolCommand[] {
  if (platform === 'win32') {
    const script = [
      "$ErrorActionPreference='Stop'",
      'Add-Type -AssemblyName System.Drawing',
      '$img=$null;$bmp=$null;$g=$null;$ep=$null',
      'try {',
      `$img=[System.Drawing.Image]::FromFile(${psQuote(inPath)})`,
      `$scale=[Math]::Min(1.0,${maxEdge}/[Math]::Max($img.Width,$img.Height))`,
      '$w=[int][Math]::Max(1,[Math]::Round($img.Width*$scale))',
      '$h=[int][Math]::Max(1,[Math]::Round($img.Height*$scale))',
      '$bmp=New-Object System.Drawing.Bitmap($w,$h)',
      '$g=[System.Drawing.Graphics]::FromImage($bmp)',
      '$g.DrawImage($img,0,0,$w,$h)',
      // System.Drawing 的 JPEG 编码器默认质量 75——显式带 quality 参数，别让
      // 「平台不同画质不同」成为隐性差异（同 JPEG_QUALITY）。
      "$codec=[System.Drawing.Imaging.ImageCodecInfo]::GetImageEncoders()|Where-Object{$_.MimeType -eq 'image/jpeg'}",
      '$ep=New-Object System.Drawing.Imaging.EncoderParameters(1)',
      `$ep.Param[0]=New-Object System.Drawing.Imaging.EncoderParameter([System.Drawing.Imaging.Encoder]::Quality,${JPEG_QUALITY})`,
      `$bmp.Save(${psQuote(outPath)},$codec,$ep)`,
      '} finally {',
      'if ($ep) { $ep.Dispose() }',
      'if ($g) { $g.Dispose() }',
      'if ($bmp) { $bmp.Dispose() }',
      'if ($img) { $img.Dispose() }',
      '}',
    ].join(';')
    return [
      { bin: 'magick', args: [inPath, '-resize', `${maxEdge}x${maxEdge}>`, '-quality', String(JPEG_QUALITY), outPath] },
      powershellCommand(script),
    ]
  }
  return [
    { bin: 'sips', args: ['-Z', String(maxEdge), '-s', 'format', 'jpeg', '-s', 'formatOptions', String(JPEG_QUALITY), inPath, '--out', outPath] },
    { bin: 'magick', args: [inPath, '-resize', `${maxEdge}x${maxEdge}>`, '-quality', String(JPEG_QUALITY), outPath] },
    { bin: 'convert', args: [inPath, '-resize', `${maxEdge}x${maxEdge}>`, '-quality', String(JPEG_QUALITY), outPath] },
  ]
}

/** PNG 文件签名（magic bytes）。 */
const PNG_SIGNATURE = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a])
/** 完整 IEND chunk：length 0 + 'IEND' + CRC（内容固定）。 */
const PNG_IEND_CHUNK = Buffer.from([0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82])

/**
 * PNG 完整性校验：signature（8 字节）+ 首个 chunk 是长度 13 的 IHDR
 * （宽高均为正整数）+ 文件末尾 12 字节为完整 IEND chunk。
 * 防「工具 exit 0 但只写出签名/截断 PNG」被当成可渲染图片。
 */
export function isCompletePng(buf: Buffer): boolean {
  // 最小完整 PNG：8 signature + 25 IHDR chunk（4 length + 4 type + 13 data + 4 CRC）+ 12 IEND
  if (buf.length < 8 + 25 + 12) return false
  if (!buf.subarray(0, 8).equals(PNG_SIGNATURE)) return false
  // 首个 chunk 必须是 length=13 的 IHDR，且宽高均为正整数
  if (buf.readUInt32BE(8) !== 13) return false
  if (buf.toString('latin1', 12, 16) !== 'IHDR') return false
  if (buf.readUInt32BE(16) === 0 || buf.readUInt32BE(20) === 0) return false
  return buf.subarray(buf.length - PNG_IEND_CHUNK.length).equals(PNG_IEND_CHUNK)
}

/**
 * PNG 是否带 alpha 通道。IHDR 的 color type（偏移 25）为 4（灰度+alpha）或
 * 6（RGBA）即带 alpha——这类图**不能**交给 JPEG 有损编码：alpha 无处安放，
 * 编码器会把它压成黑底，模型看到的是「图变黑了」，比不发图更误导。
 * 非 PNG 或结构不完整一律返回 false（调用方只在 mime === 'image/png' 时问）。
 * 已知窄口：调色板 PNG（type 3）的 tRNS 透明不走本判据。
 */
export function pngHasAlpha(buf: Buffer): boolean {
  if (!isCompletePng(buf)) return false
  const colorType = buf[25]
  return colorType === 4 || colorType === 6
}

/**
 * JPEG 完整性校验：SOI（FF D8 FF）+ 扫到一个宽高为正的帧头（SOF0–SOF15，
 * 排除 DHT/JPG/DAC）+ 文件末尾为 EOI（FF D9）。与 isCompletePng 同款纪律：
 * 工具 exit 0 但只写出半个文件时不能算可渲染图片。
 */
export function isCompleteJpeg(buf: Buffer): boolean {
  if (buf.length < 16) return false
  if (buf[0] !== 0xff || buf[1] !== 0xd8 || buf[2] !== 0xff) return false
  if (buf[buf.length - 2] !== 0xff || buf[buf.length - 1] !== 0xd9) return false
  return jpegFrameDimensions(buf) !== null
}

/** 按段扫描定位帧头（SOF），返回宽高；走到 SOS/段界错位/文件尾仍未见到则 null。 */
function jpegFrameDimensions(buf: Buffer): { width: number; height: number } | null {
  let i = 2
  while (i + 3 < buf.length) {
    if (buf[i] !== 0xff) return null // 段边界必须落在 marker 前缀上
    const marker = buf[i + 1]!
    if (marker === 0xff) { i += 1; continue } // 填充字节
    // 无载荷 marker：TEM(01) 与 RST/SOI/EOI(D0–D9)
    if (marker === 0x01 || (marker >= 0xd0 && marker <= 0xd9)) { i += 2; continue }
    const length = buf.readUInt16BE(i + 2)
    if (length < 2) return null
    if (marker >= 0xc0 && marker <= 0xcf && marker !== 0xc4 && marker !== 0xc8 && marker !== 0xcc) {
      if (i + 9 >= buf.length) return null
      const height = buf.readUInt16BE(i + 5)
      const width = buf.readUInt16BE(i + 7)
      return width > 0 && height > 0 ? { width, height } : null
    }
    if (marker === 0xda) return null // 已到扫描数据却没见过帧头 → 不是合法 JPEG
    i += 2 + length
  }
  return null
}

/** 图像工具产物的两种可内联格式。 */
export type ImageToolMime = 'image/png' | 'image/jpeg'

/** 校验通过的渲染产物：内容 + **由 magic 判定**的真实 MIME（不靠扩展名/调用方约定）。 */
export interface RenderedImage {
  data: Buffer
  mime: ImageToolMime
}

/**
 * 依序尝试候选命令，首个产出**完整图片**的候选返回其内容与真实 MIME；全部失败返回 null。
 *
 * 候选级隔离：每个候选把「执行 + 读回 + 校验」作为一体化尝试——先删除
 * outputPath（不存在则忽略），再 execFile 要求 exit 0，readFile 读回后校验完整性
 * （PNG：签名 + IHDR + IEND；JPEG：SOI + 帧头 + EOI，截断都不算数）。
 * 先删残片是为了避免前一候选留下的非空输出被后一候选
 * （exit 0 但没写文件）误判为自己的产出。
 *
 * `only` 限定接受的格式（展示路径必须 PNG）；不传则 PNG/JPEG 都收——附件路径
 * 需要「PNG/JPEG 取更小」，产物 MIME 必须随内容一起回传，否则会把 JPEG 字节
 * 当 PNG 贴进请求。
 *
 * 全部失败时若 RIVET_DEBUG 非空，向 stderr 打一行带原因的调试输出
 * （哪个工具、什么错误），避免静默降级不可观测。
 */
async function runCandidates(
  candidates: ImageToolCommand[],
  outputPath: string,
  timeoutMs: number,
  only?: ImageToolMime,
): Promise<RenderedImage | null> {
  let lastFailure: string | null = null
  for (const { bin, args } of candidates) {
    try {
      await rm(outputPath, { force: true })
      await execFileAsync(bin, args, { windowsHide: true, timeout: timeoutMs })
      const out = await readFile(outputPath)
      const mime: ImageToolMime | null = isCompletePng(out)
        ? 'image/png'
        : isCompleteJpeg(out)
          ? 'image/jpeg'
          : null
      if (mime && (!only || mime === only)) return { data: out, mime }
      // exit 0 但输出缺失/为空/非完整图片——尝试下一个候选
      lastFailure = `${bin}: exit 0 但未产出完整${only === 'image/png' ? ' PNG' : only === 'image/jpeg' ? ' JPEG' : '图片'}`
    } catch (err) {
      lastFailure = `${bin}: ${err instanceof Error ? err.message : String(err)}`
    }
  }
  if (lastFailure && process.env['RIVET_DEBUG']) {
    console.error(`[image-tool] 全部 ${candidates.length} 个候选失败，最后一次：${lastFailure}`)
  }
  return null
}

/**
 * PNG-only 执行器（终端内联展示 / 剪贴板、附件里的 PNG 分支）：
 * 候选族由 toPngCandidates / resizeCandidates 构造，产物必须是完整 PNG。
 */
export async function runImageTool(
  candidates: ImageToolCommand[],
  outputPath: string,
  timeoutMs = 15000,
): Promise<Buffer | null> {
  return (await runCandidates(candidates, outputPath, timeoutMs, 'image/png'))?.data ?? null
}

/**
 * 格式无关执行器（JPEG 阶梯的 JPEG 分支）：PNG/JPEG 都收，MIME 随产物回传。
 * 调用方拿到的可能是任意一种，**必须**用返回的 mime，不能按 outPath 扩展名假定。
 */
export async function runImageToolAuto(
  candidates: ImageToolCommand[],
  outputPath: string,
  timeoutMs = 15000,
): Promise<RenderedImage | null> {
  return runCandidates(candidates, outputPath, timeoutMs)
}

/**
 * 「PNG / JPEG 取更小」的选择律（Grok `re_encode_under_limit` 同款）：两条编码路径
 * 都试过之后，谁更小用谁；平局取 PNG（无损优先）。超过预算的候选直接作废——
 * 只有一个合格就用它，都不合格返回 null（调用方按软/硬语义决定保留原图还是报错）。
 */
export function pickSmallerImage(
  png: Buffer | null,
  jpeg: Buffer | null,
  maxBytes: number,
): RenderedImage | null {
  const p = png && png.length <= maxBytes ? { data: png, mime: 'image/png' as const } : null
  const j = jpeg && jpeg.length <= maxBytes ? { data: jpeg, mime: 'image/jpeg' as const } : null
  if (p && j) return j.data.length < p.data.length ? j : p
  return p ?? j
}

/** 创建本次转换的独立临时目录，并顺手触发惰性清扫（fire-and-forget）。 */
export async function makeImageTempDir(): Promise<string> {
  void sweepStaleImageTempDirs().catch(() => { /* best-effort sweep */ })
  return mkdtemp(join(tmpdir(), IMAGE_TEMP_DIR_PREFIX))
}

/** 删除转换临时目录；失败静默。 */
export async function removeImageTempDir(dir: string): Promise<void> {
  await rm(dir, { recursive: true, force: true }).catch(() => { /* best-effort cleanup */ })
}

/** 清扫超过 1 小时的残留临时目录（进程中断的兜底回收）。 */
export async function sweepStaleImageTempDirs(now = Date.now()): Promise<void> {
  let entries: string[]
  try {
    entries = await readdir(tmpdir())
  } catch {
    return
  }
  for (const entry of entries) {
    if (!entry.startsWith(IMAGE_TEMP_DIR_PREFIX)) continue
    const full = join(tmpdir(), entry)
    try {
      const st = await stat(full)
      if (now - st.mtimeMs > STALE_MS) await rm(full, { recursive: true, force: true })
    } catch {
      // 单个目录失败不阻塞其余清扫
    }
  }
}
