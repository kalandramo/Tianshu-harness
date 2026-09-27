/**
 * 多行图片路径粘贴 —— 一次粘贴一批路径，全部作为附件挂上。
 *
 * 背景：终端只能 bracketed-paste 文本，用户贴图的方式是贴「图片文件路径」。旧实现只认
 * 「整段粘贴恰好是单行路径」（`!text.includes('\n')`），于是从 Finder 多选、编辑器里
 * 复制一列路径、脚本输出一串文件名这些最自然的批量操作，全部退化成「把路径当文本塞进
 * 输入框」——用户要么一张张重贴，要么手动删掉那堆文本。
 *
 * 判据（保守优先，宁可退回文本粘贴也不吞用户内容）：
 *   ① **整段都是路径**才算图片粘贴——每个非空行都得是图片扩展名；混了别的话
 *      （`看这张 /tmp/a.png`）就整段按文本粘贴，绝不「挑出路径然后把其余行丢掉」。
 *   ② 行内可带成对引号与反斜杠转义空格（终端拖拽/`Copy as Pathname` 的两种形态）。
 *   ③ 全员失败才回退文本（保留旧的单图行为）；部分成功只报失败项——路径文本混进
 *      prompt 只会干扰模型。
 *   ④ 槽位不足（MAX_IMAGES）时按顺序加载到满，其余报「已跳过」而不是静默丢弃。
 */

import { loadImageAttachment } from './image-attach.js'
import { looksLikeImagePath } from './image-attach.js'

/** 加载器：路径 → data URL；失败抛错。可注入（单测不碰文件系统/图像工具）。 */
export type ImagePathLoader = (absolutePath: string) => Promise<string>

const defaultLoader: ImagePathLoader = async (absolutePath) => (await loadImageAttachment(absolutePath)).dataUrl

/**
 * 去掉终端粘贴常见的两种包裹：成对引号（`"…"` / `'…'`）与反斜杠转义空格
 * （`/Users/x/My\ Shot.png`）。只处理这两类——不做 shell 反转义，避免把
 * Windows 路径里的反斜杠吃掉。
 */
function unwrapPastedPath(line: string): string {
  let out = line.trim()
  const first = out[0]
  if ((first === '"' || first === "'") && out.length > 1 && out.endsWith(first)) {
    out = out.slice(1, -1)
  }
  return out.replace(/\\ /g, ' ').trim()
}

/**
 * 把粘贴文本解析成图片路径列表。
 * 返回 null = 这不是「一整段图片路径」的粘贴（调用方按普通文本处理）。
 * 返回 [] 不会被返回——没有非空行也按 null 处理。
 */
export function parseImagePathPaste(text: string): string[] | null {
  const lines = text
    .split(/\r?\n/)
    .map(unwrapPastedPath)
    .filter((line) => line.length > 0)
  if (lines.length === 0) return null
  if (!lines.every((line) => looksLikeImagePath(line))) return null
  return lines
}

export interface LoadPastedImagesOptions {
  /** 还能附加几张（= MAX_IMAGES − 已有附件数）。 */
  slots: number
  /** 测试注入；缺省走真实 image-attach（读文件 + 必要时归一化）。 */
  load?: ImagePathLoader
}

export interface ImagePasteOutcome {
  /** 成功加载的 data URL，按粘贴顺序（已按槽位截断）。 */
  dataUrls: string[]
  /** 因槽位不足而未尝试的路径数。 */
  skipped: number
  /** 加载失败的路径与原因（顺序同粘贴顺序）。 */
  failures: Array<{ path: string; message: string }>
}

/**
 * 按顺序加载粘贴进来的路径。加载是**串行**的：每次都可能 spawn 图像工具，
 * 并发跑几条只会抢 CPU，而条数上限本来就只有 MAX_IMAGES。
 */
export async function loadPastedImages(
  paths: string[],
  opts: LoadPastedImagesOptions,
): Promise<ImagePasteOutcome> {
  const load = opts.load ?? defaultLoader
  const slots = Math.max(0, opts.slots)
  const take = paths.slice(0, slots)
  const dataUrls: string[] = []
  const failures: ImagePasteOutcome['failures'] = []
  for (const path of take) {
    try {
      dataUrls.push(await load(path))
    } catch (err) {
      failures.push({ path, message: err instanceof Error ? err.message : String(err) })
    }
  }
  return { dataUrls, skipped: paths.length - take.length, failures }
}

/**
 * 生成给人看的告警行（不含颜色/主题——渲染归 TUI）。空数组 = 无需提示。
 * 单条失败沿用旧文案（`⚠ 图片加载失败: …`），多条时点名前几个文件。
 */
export function formatImagePasteNotices(outcome: ImagePasteOutcome, maxImages: number): string[] {
  const notices: string[] = []
  if (outcome.skipped > 0) {
    notices.push(`⚠ 最多附加 ${maxImages} 张图片，本次已跳过 ${outcome.skipped} 张`)
  }
  if (outcome.failures.length === 1) {
    notices.push(`⚠ 图片加载失败: ${outcome.failures[0]!.message}`)
  } else if (outcome.failures.length > 1) {
    const names = outcome.failures.slice(0, 3).map((f) => basenameOf(f.path))
    const more = outcome.failures.length > names.length ? ` 等 ${outcome.failures.length} 个` : ''
    notices.push(`⚠ ${outcome.failures.length} 张图片加载失败（${names.join('、')}${more}）: ${outcome.failures[0]!.message}`)
  }
  return notices
}

/** 只在告警里用：取路径最后一段，避免把整条长路径刷满状态区。 */
function basenameOf(path: string): string {
  const cut = Math.max(path.lastIndexOf('/'), path.lastIndexOf('\\'))
  return cut >= 0 ? path.slice(cut + 1) : path
}
