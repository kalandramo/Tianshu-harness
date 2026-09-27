/** A terminal stream failure: replaying this partial reasoning can reinforce the loop. */
export class ReasoningRepetitionError extends Error {
  constructor() {
    super('DeepSeek reasoning stream stopped: sustained short-phrase repetition detected. Start a fresh conversation or switch models before retrying.')
    this.name = 'ReasoningRepetitionError'
  }
}

const WINDOW_LINES = 128
const MAX_LINE_CHARS = 32
/**
 * 判定口径（2026-09-24 放宽）：**≤3 个**不同短句占到窗口的 **≥95%**。
 *
 * 放宽的理由是实机数据：4.1 flash 线（`deepseek-flash` / `deepseek-v4-flash` /
 * `deepseek-v4.1-flash-expires-on-0910`）在长对话里会成段输出「重复短句」式推理，
 * 别的 agent 也遇到过——那是这个模型的文风，不代表这一轮没有产出，硬停等于把整轮
 * 工作丢掉。用户明确要求提高阈值。
 *
 * 为什么是「减短语种类」而不是「加大窗口」：
 * - 本仓自己的语义是「卡死」= 一句话在原地打转（1-3 个短语），而不是「短时间内
 *   反复用几句口头禅」。窗口加大到 256 只会把触发推迟一倍，且会让「3 句轮转」
 *   这种真卡死形态漏过（测试里那条 240 行的循环就不再触发）。
 * - 减到 3 之后，**4 句及以上轮转的复读不再拦截**——正是要放过的形态；
 *   覆盖率 90%→95% 再放过「主流但偶尔夹一句新话」的形态。
 * - 健康推理的余量仍然巨大：`步骤 N：…` 那种长行计入 null，实测覆盖率约 50%。
 *
 * 残留风险（有意接受）：1-3 句真正原地打转仍会中止——那才是这个守卫存在的理由，
 * 且中止时必须整轮重来（重复的推理回放会强化循环，见类注释）。
 */
const MAX_DISTINCT_PHRASES = 3
const MIN_REPEATED_LINES = Math.ceil(WINDOW_LINES * 0.95)

/**
 * 命中复读判定后的**纠正指令**（软重试用，2026-09-24）。
 *
 * 为什么不是直接重发同一个请求：退化推理一旦回放，模型会接着那段继续打转
 * （类注释里「replaying this partial reasoning can reinforce the loop」）。
 * 所以软重试必须同时做两件事——**丢掉那段退化推理** + 附上这句纠正，
 * 让第二次尝试换一条思路，而不是把同一个循环再喂一遍。
 *
 * 作为请求尾部的 user 消息追加（只改本次 attempt 的 wire 副本，不进口历史）：
 * 前缀字节不变、缓存断点不退，代价只有这一小段文本。
 * 注入点已申报：`src/prompt/injection-surfaces.ts` 的 reminder.reasoning-repeat-correction。
 */
export const REASONING_REPETITION_CORRECTION =
  '<system-reminder>\n'
  + '[reasoning-repeat] 你上一轮的思考陷入了重复：同一句话被反复输出。请换一种方式继续——'
  + '要么用不同的措辞推进一个具体的新步骤，要么直接给出结论；不要再次重复同一句。\n'
  + '</system-reminder>'

/**
 * Bounded, chunk-boundary-independent detector for thinking-only short-line loops.
 * Require three or fewer short phrases to occupy >=95% of 128 non-empty lines.
 * Length alone is never a reason to stop healthy reasoning. Long/novel lines
 * count against repetition; whitespace-only lines do not inflate the window.
 */
export class ReasoningRepetitionGuard {
  private line = ''
  private longLine = false
  private window: Array<string | null> = []

  push(delta: string): void {
    for (const char of delta) {
      if (char !== '\n') {
        if (!this.longLine) {
          this.line += char
          if (this.line.length > MAX_LINE_CHARS) this.longLine = true
        }
        continue
      }
      const line = this.line.trim()
      if (this.longLine || line) {
        this.window.push(this.longLine ? null : line)
        if (this.window.length > WINDOW_LINES) this.window.shift()
        if (this.window.length === WINDOW_LINES) {
          const counts = new Map<string, number>()
          for (const entry of this.window) {
            if (entry !== null) counts.set(entry, (counts.get(entry) ?? 0) + 1)
          }
          const repeated = [...counts.values()].sort((a, b) => b - a).slice(0, MAX_DISTINCT_PHRASES)
            .reduce((sum, count) => sum + count, 0)
          if (repeated >= MIN_REPEATED_LINES) throw new ReasoningRepetitionError()
        }
      }
      this.line = ''
      this.longLine = false
    }
  }
}
