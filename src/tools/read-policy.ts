export type ReadPolicyKind = 'source' | 'log' | 'jsonl' | 'generated' | 'minified' | 'unknown'
export type ReadPolicyAction = 'full' | 'full-with-hint' | 'partial' | 'preview' | 'reject-with-range'

export interface ReadPolicyInput {
  filePath: string
  sizeBytes: number
  hasExplicitRange: boolean
  /**
   * 本次读的模型预算（字符数，来自 `computeModelReadCap(...).maxChars`）。
   *
   * 传了就由它决定 source/unknown 的 PARTIAL 门（且永不低于历史 80KB）；
   * 不传保持 80KB 历史门——既有调用方与用例行为逐字不变。
   *
   * 单位说明：调用方手上只有 `stat.size`（字节），而预算按字符计。UTF-8 下
   * 字节 ≥ 字符，故该近似只会让门更早触发（偏保守），不会放宽；调用方的
   * `content.length <= cap.maxChars` 短路负责兜住这层偏差，避免真的折叠。
   */
  budgetChars?: number
}

export interface ReadPolicyDecision {
  kind: ReadPolicyKind
  action: ReadPolicyAction
  reason: string
  previewLines: number
  maxRangeLines: number
}

const LOG_PREVIEW_GUARD_BYTES = 16 * 1024
/** 日志 preview 上限（2MB）：preview 分支要把文件全量读进内存再截头尾，超过这个
 *  尺寸就不是"给个预览"而是"把堆吃干"——改判 reject，让调用方先 grep 定位再带
 *  range 读。有了它，read_file 的硬门才可以对日志类型豁免。 */
const MAX_LOG_PREVIEW_BYTES = 2 * 1024 * 1024
const DEFAULT_PREVIEW_LINES = 80
const DEFAULT_MAX_RANGE_LINES = 200

/** ~20KB — source files below this are returned in full without hints. */
const SOURCE_SMALL_BYTES = 20 * 1024

/** 历史 PARTIAL 门（80KB）——没有窗口预算时的兜底，同时是预算门的**下限**。
 *  小窗口的 cap（如 64K 窗口仅 8K）直接当门会把 20KB 文件从 full-with-hint
 *  拽进 partial：那是收紧，不是对齐。 */
const SOURCE_LARGE_BYTES = 80 * 1024

function classifyPath(filePath: string): ReadPolicyKind {
  const lower = filePath.toLowerCase()
  if (/\.(?:jsonl|ndjson)(?:\.\d+)?$/.test(lower)) return 'jsonl'
  if (/\.(?:log|out|err|trace)(?:\.\d+)?$/.test(lower)) return 'log'
  if (/\.min\.(?:js|css)$/.test(lower)) return 'minified'
  if (/(?:^|\/)(?:dist|build|coverage|\.next)\//.test(lower)) return 'generated'
  if (/\.(?:ts|tsx|js|jsx|mjs|cjs|json|md|css|scss|html|yml|yaml)$/.test(lower)) return 'source'
  return 'unknown'
}

export function decideReadPolicy(input: ReadPolicyInput): ReadPolicyDecision {
  const kind = classifyPath(input.filePath)
  const base = { kind, previewLines: DEFAULT_PREVIEW_LINES, maxRangeLines: DEFAULT_MAX_RANGE_LINES }
  // PARTIAL 门以窗口预算为准（下限 80KB）：1M 窗口的 cap 是 120K 字符，继续用
  // 80KB 静态门会让 80–120KB 的文件走一次无谓的 partial 判定。
  const largeBytes = Math.max(SOURCE_LARGE_BYTES, input.budgetChars ?? 0)

  if (input.hasExplicitRange) {
    return { ...base, action: 'full', reason: 'explicit range requested' }
  }
  if ((kind === 'log' || kind === 'jsonl') && input.sizeBytes > LOG_PREVIEW_GUARD_BYTES) {
    if (input.sizeBytes <= MAX_LOG_PREVIEW_BYTES) {
      return { ...base, action: 'preview', reason: 'log-like file over preview guard' }
    }
    return { ...base, action: 'reject-with-range', reason: `log-like file over ${MAX_LOG_PREVIEW_BYTES / 1024 / 1024}MB — grep to locate the region first` }
  }
  if (kind === 'generated' || kind === 'minified') {
    return { ...base, action: 'reject-with-range', reason: 'generated or minified file requires an explicit range' }
  }

  if (kind === 'source' || kind === 'unknown') {
    if (input.sizeBytes > largeBytes) {
      return { ...base, action: 'partial', reason: 'file size exceeds the read budget — returning first page with navigation hints' }
    }
    if (input.sizeBytes > SOURCE_SMALL_BYTES) {
      return { ...base, action: 'full-with-hint', reason: 'medium source file — full read with editing hints' }
    }
  }

  return { ...base, action: 'full', reason: 'safe default read' }
}
