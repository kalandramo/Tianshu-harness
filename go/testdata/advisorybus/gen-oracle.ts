// 端到端对账：真实 oracle（src/agent/advisory-bus.ts 的 AdvisoryBus.render）
// vs Go 实现。**不做手写期望值**——一律从真实 TS 代码路径导出。
//
// **范围**：只覆盖**核心路径**（submit / 去重 / 排序 / 类别上限 / Top-N /
// TTL / XML 渲染 / ledger）。治理子系统（习惯化 / efficacy / lift / holdout /
// SR 通道 / mutex）都需注入 provider 才生效，不注入时行为即核心路径。
//
// 运行（必须在**仓库根**）：
//   node_modules/.bin/tsx go/testdata/advisorybus/gen-oracle.ts
// 产出：go/testdata/advisorybus/oracle.json + cases.json
import { writeFileSync } from 'node:fs'
import { AdvisoryBus } from '../../../src/agent/advisory-bus.js'

interface CaseSpec {
  /** 投递批次：每次 render 前 submit 的条目 */
  batches: Array<{ entries: any[]; domain?: string }>
  /** 渲染轮数（每次 render 消耗一个批次；批次用尽则不再 submit 只 render） */
  renders: number
  /** 习惯化：key → 连续忽略次数（模拟 readback 的 getIgnoredStreak） */
  streaks?: Record<string, number>
  /** lift 消费：key → 成熟 lift（null 模拟样本不足 = 中性） */
  lifts?: Record<string, number | null>
  /** holdout 抽样：抽样率 + 固定 RNG 序列（模拟确定性抽样）+ 资格 key 集 */
  holdout?: { rate: number; rng: number[]; eligible: string[] }
  /** T7 效力排序：key → { score, confidence }（null 模拟无样本）+ span 覆盖 */
  efficacy?: { signals: Record<string, { score: number; confidence: number } | null>; span?: number }
}

// 用例：覆盖去重 / 排序 / 类别上限 / 预算 / TTL / 转义 / 星域预算。
const cases: Record<string, CaseSpec> = {
  // ── 基础：单条 ──
  single: {
    batches: [{ entries: [{ key: 'a', priority: 0.6, category: 'discipline', content: 'hello' }] }],
    renders: 1,
  },

  // ── 去重：同 key 保留高 priority ──
  dedup_keeps_higher: {
    batches: [{ entries: [
      { key: 'dup', priority: 0.5, category: 'discipline', content: '低' },
      { key: 'dup', priority: 0.7, category: 'discipline', content: '高' },
    ] }],
    renders: 1,
  },

  // ── 去重：后投递的更高优先级胜出 ──
  dedup_later_wins: {
    batches: [{ entries: [
      { key: 'dup', priority: 0.8, category: 'discipline', content: '先高' },
      { key: 'dup', priority: 0.3, category: 'discipline', content: '后低' },
    ] }],
    renders: 1,
  },

  // ── 去重平手：同 key 同 priority → 先出现的胜出（TS 用严格大于）──
  dedup_same_priority: {
    batches: [{ entries: [
      { key: 'dup', priority: 0.6, category: 'discipline', content: '第一条' },
      { key: 'dup', priority: 0.6, category: 'discipline', content: '第二条' },
    ] }],
    renders: 1,
  },

  // ── 排序：按 priority 降序 ──
  sort_by_priority: {
    batches: [{ entries: [
      { key: 'low', priority: 0.3, category: 'discipline', content: 'c' },
      { key: 'high', priority: 0.8, category: 'repair', content: 'a' },
      { key: 'mid', priority: 0.5, category: 'todo', content: 'b' },
    ] }],
    renders: 1,
  },

  // ── 类别上限：每 category 最多 2 条 ──
  category_cap: {
    batches: [{ entries: [
      { key: 'd1', priority: 0.9, category: 'discipline', content: '1' },
      { key: 'd2', priority: 0.8, category: 'discipline', content: '2' },
      { key: 'd3', priority: 0.7, category: 'discipline', content: '3' },
      { key: 'd4', priority: 0.6, category: 'discipline', content: '4' },
    ] }],
    renders: 1,
  },

  // ── Top-N 预算：每轮最多 3 条（非 constitutional）──
  top_n_budget: {
    batches: [{ entries: [
      { key: 'a', priority: 0.9, category: 'discipline', content: '1' },
      { key: 'b', priority: 0.8, category: 'repair', content: '2' },
      { key: 'c', priority: 0.7, category: 'todo', content: '3' },
      { key: 'd', priority: 0.6, category: 'dedup', content: '4' },
      { key: 'e', priority: 0.5, category: 'immune', content: '5' },
    ] }],
    renders: 1,
  },

  // ── constitutional：不受上限，排在最前 ──
  constitutional_first: {
    batches: [{ entries: [
      { key: 'normal', priority: 0.95, category: 'discipline', content: '普通' },
      { key: 'const', priority: 0.9, category: 'constitutional', tier: 'constitutional', content: '宪法' },
    ] }],
    renders: 1,
  },

  // ── constitutional 多条 + 普通多条 ──
  constitutional_multi: {
    batches: [{ entries: [
      { key: 'c1', priority: 0.9, category: 'constitutional', tier: 'constitutional', content: 'C1' },
      { key: 'c2', priority: 0.85, category: 'constitutional', tier: 'constitutional', content: 'C2' },
      { key: 'n1', priority: 0.7, category: 'discipline', content: 'N1' },
      { key: 'n2', priority: 0.6, category: 'repair', content: 'N2' },
    ] }],
    renders: 1,
  },

  // ── informational：填充剩余预算 ──
  informational_fill: {
    batches: [{ entries: [
      { key: 'op1', priority: 0.7, category: 'discipline', content: 'op' },
      { key: 'info1', priority: 0.9, category: 'background', tier: 'informational', content: 'info-high' },
      { key: 'info2', priority: 0.8, category: 'monitor', tier: 'informational', content: 'info-mid' },
    ] }],
    renders: 1,
  },

  // ── 星域预算：天权/瑶光 只 1 条 ──
  domain_budget_tianquan: {
    batches: [{ domain: '天权', entries: [
      { key: 'a', priority: 0.9, category: 'discipline', content: '1' },
      { key: 'b', priority: 0.8, category: 'repair', content: '2' },
      { key: 'c', priority: 0.7, category: 'todo', content: '3' },
    ] }],
    renders: 1,
  },

  domain_budget_yaoguang: {
    batches: [{ domain: '瑶光', entries: [
      { key: 'a', priority: 0.9, category: 'discipline', content: '1' },
      { key: 'b', priority: 0.8, category: 'repair', content: '2' },
    ] }],
    renders: 1,
  },

  // ── 星域条目豁免预算（star_domain category）──
  star_domain_exempt: {
    batches: [{ entries: [
      { key: 'a', priority: 0.9, category: 'discipline', content: '1' },
      { key: 'b', priority: 0.8, category: 'repair', content: '2' },
      { key: 'c', priority: 0.7, category: 'todo', content: '3' },
      { key: 'sd', priority: 0.1, category: 'star_domain', content: '星域' },
    ] }],
    renders: 1,
  },

  // ── TTL：>1 的条目存活到下一轮 ──
  ttl_survives: {
    batches: [
      { entries: [{ key: 'persist', priority: 0.6, category: 'discipline', content: '存活', ttl: 3 }] },
      { entries: [] },
      { entries: [] },
    ],
    renders: 3,
  },

  // ── TTL 递减到 1 后消失 ──
  ttl_expires: {
    batches: [
      { entries: [{ key: 'p', priority: 0.6, category: 'discipline', content: 'X', ttl: 2 }] },
      { entries: [] },
      { entries: [] },
    ],
    renders: 3,
  },

  // ── XML 转义 ──
  xml_escape: {
    batches: [{ entries: [
      { key: 'a&b', priority: 0.6, category: 'discipline', content: '<div class="x">&amp;</div>' },
    ] }],
    renders: 1,
  },

  // ── priority 格式（toFixed(2)）──
  priority_format: {
    batches: [{ entries: [
      { key: 'a', priority: 0.6, category: 'discipline', content: 'x' },
      { key: 'b', priority: 0.555, category: 'repair', content: 'y' },
      { key: 'c', priority: 0.1, category: 'todo', content: 'z' },
    ] }],
    renders: 1,
  },

  // ── priority 边界值（判别「远离零」实现）──
  // 2.675/0.615/1.255 这三个上，自写「放大 100 倍 + 远离零」会错，
  // 而 JS toFixed 给 2.67/0.61/1.25。首版实现因此被 oracle 抓到。
  priority_edge: {
    batches: [{ entries: [
      { key: 'a', priority: 2.675, category: 'discipline', content: 'x' },
      { key: 'b', priority: 0.615, category: 'repair', content: 'y' },
      { key: 'c', priority: 1.255, category: 'todo', content: 'z' },
      { key: 'd', priority: 0.145, category: 'dedup', content: 'w' },
    ] }],
    renders: 1,
  },

  // ── 空渲染 ──
  empty: { batches: [{ entries: [] }], renders: 1 },

  // ── 同 priority 多条（次级排序无 provider → 保持稳定序）──
  same_priority: {
    batches: [{ entries: [
      { key: 'a', priority: 0.6, category: 'discipline', content: 'A' },
      { key: 'b', priority: 0.6, category: 'repair', content: 'B' },
      { key: 'c', priority: 0.6, category: 'todo', content: 'C' },
    ] }],
    renders: 1,
  },

  // ── 多轮：轮次间状态清理（entries 每轮清空，alive 保留）──
  multi_round: {
    batches: [
      { entries: [{ key: 'r1', priority: 0.6, category: 'discipline', content: '第一轮' }] },
      { entries: [{ key: 'r2', priority: 0.7, category: 'repair', content: '第二轮' }] },
    ],
    renders: 2,
  },

  // ── key 级送达冷却：注册 key 送达后 N 轮内不再渲染 ──
  // readonly-spiral 注册 3 轮冷却——第 0 轮送达后，第 1/2 轮应被吞掉（计 dropped），
  // 第 3 轮恢复。未注册的 key 不受影响。
  cooldown_registered: {
    batches: [
      { entries: [{ key: 'readonly-spiral', priority: 0.6, category: 'discipline', content: '螺旋' }] },
      { entries: [{ key: 'readonly-spiral', priority: 0.6, category: 'discipline', content: '螺旋' }] },
      { entries: [{ key: 'readonly-spiral', priority: 0.6, category: 'discipline', content: '螺旋' }] },
      { entries: [{ key: 'readonly-spiral', priority: 0.6, category: 'discipline', content: '螺旋' }] },
    ],
    renders: 4,
  },

  // ── 未注册 key 无冷却（对照）──
  cooldown_unregistered: {
    batches: [
      { entries: [{ key: 'some-other', priority: 0.6, category: 'discipline', content: 'X' }] },
      { entries: [{ key: 'some-other', priority: 0.6, category: 'discipline', content: 'X' }] },
      { entries: [{ key: 'some-other', priority: 0.6, category: 'discipline', content: 'X' }] },
    ],
    renders: 3,
  },

  // ── 冷却只影响注册 key，不牵连同批其他条目 ──
  cooldown_mixed: {
    batches: [
      { entries: [
        { key: 'readonly-spiral', priority: 0.6, category: 'discipline', content: '螺旋' },
        { key: 'normal', priority: 0.5, category: 'repair', content: '普通' },
      ] },
      { entries: [
        { key: 'readonly-spiral', priority: 0.6, category: 'discipline', content: '螺旋' },
        { key: 'normal', priority: 0.5, category: 'repair', content: '普通' },
      ] },
    ],
    renders: 2,
  },

  // ── turn-call-limit 也是注册 key（3 轮冷却）──
  cooldown_turn_call_limit: {
    batches: [
      { entries: [{ key: 'turn-call-limit', priority: 0.7, category: 'discipline', content: '限额' }] },
      { entries: [{ key: 'turn-call-limit', priority: 0.7, category: 'discipline', content: '限额' }] },
    ],
    renders: 2,
  },

  // ── virtue-encouragement 注册 5 轮冷却 ──
  cooldown_virtue_5: {
    batches: [
      { entries: [{ key: 'virtue-encouragement', priority: 0.4, category: 'encouragement', content: '表扬' }] },
      { entries: [{ key: 'virtue-encouragement', priority: 0.4, category: 'encouragement', content: '表扬' }] },
      { entries: [{ key: 'virtue-encouragement', priority: 0.4, category: 'encouragement', content: '表扬' }] },
      { entries: [{ key: 'virtue-encouragement', priority: 0.4, category: 'encouragement', content: '表扬' }] },
      { entries: [{ key: 'virtue-encouragement', priority: 0.4, category: 'encouragement', content: '表扬' }] },
      { entries: [{ key: 'virtue-encouragement', priority: 0.4, category: 'encouragement', content: '表扬' }] },
    ],
    renders: 6,
  },

  // ── mutex：winner 在场时 loser 让位 ──
  mutex_self_verify_wins: {
    batches: [{ entries: [
      { key: 'self-verify', priority: 0.58, category: 'discipline', content: '有验证债' },
      { key: 'virtue-encouragement', priority: 0.4, category: 'encouragement', content: '干得好' },
    ] }],
    renders: 1,
  },

  // ── mutex：winner 不在场时 loser 正常渲染 ──
  mutex_winner_absent: {
    batches: [{ entries: [
      { key: 'virtue-encouragement', priority: 0.4, category: 'encouragement', content: '干得好' },
    ] }],
    renders: 1,
  },

  // ── mutex：lossy-observation 胜过 readonly-spiral ──
  mutex_lossy_wins: {
    batches: [{ entries: [
      { key: 'lossy-observation', priority: 0.6, category: 'discipline', content: '观测有损' },
      { key: 'readonly-spiral', priority: 0.55, category: 'discipline', content: '开始行动' },
    ] }],
    renders: 1,
  },

  // ── mutex：ccr-天权-P3 胜过表扬 ──
  mutex_ccr_wins: {
    batches: [{ entries: [
      { key: 'ccr-天权-P3', priority: 0.55, category: 'star_domain', content: '改道' },
      { key: 'virtue-encouragement', priority: 0.4, category: 'encouragement', content: '干得好' },
    ] }],
    renders: 1,
  },

  // ── mutex：两个 loser 同时在场都被丢弃 ──
  mutex_two_losers: {
    batches: [{ entries: [
      { key: 'self-verify', priority: 0.58, category: 'discipline', content: '债' },
      { key: 'virtue-encouragement', priority: 0.4, category: 'encouragement', content: '表扬1' },
      { key: 'readonly-spiral', priority: 0.55, category: 'discipline', content: '行动' },
      { key: 'lossy-observation', priority: 0.6, category: 'discipline', content: '有损' },
    ] }],
    renders: 1,
  },

  // ── 习惯化对抗：升级措辞（streak >= 2）──
  habituation_escalate: {
    streaks: { 'noisy': 2 },
    batches: [{ entries: [{ key: 'noisy', priority: 0.6, category: 'discipline', content: '原始内容' }] }],
    renders: 1,
  },

  // ── 习惯化：streak = 1 不升级（阈值 2）──
  habituation_below_escalate: {
    streaks: { 'noisy': 1 },
    batches: [{ entries: [{ key: 'noisy', priority: 0.6, category: 'discipline', content: '原始内容' }] }],
    renders: 1,
  },

  // ── 习惯化：静音（streak >= 3 → 静音 4 个渲染周期）──
  habituation_silence: {
    streaks: { 'noisy': 3 },
    batches: [
      { entries: [{ key: 'noisy', priority: 0.6, category: 'discipline', content: 'X' }] },
      { entries: [{ key: 'noisy', priority: 0.6, category: 'discipline', content: 'X' }] },
      { entries: [{ key: 'noisy', priority: 0.6, category: 'discipline', content: 'X' }] },
      { entries: [{ key: 'noisy', priority: 0.6, category: 'discipline', content: 'X' }] },
      { entries: [{ key: 'noisy', priority: 0.6, category: 'discipline', content: 'X' }] },
      { entries: [{ key: 'noisy', priority: 0.6, category: 'discipline', content: 'X' }] },
    ],
    renders: 6,
  },

  // ── 习惯化：constitutional 豁免静音 ──
  habituation_constitutional_exempt: {
    streaks: { 'const-key': 10 },
    batches: [{ entries: [
      { key: 'const-key', priority: 0.9, category: 'constitutional', tier: 'constitutional', content: '宪法级' },
    ] }],
    renders: 1,
  },

  // ── 习惯化：静音只影响被静音的 key，不牵连同批其他 key ──
  habituation_mixed: {
    streaks: { 'noisy': 5 },
    batches: [
      { entries: [
        { key: 'noisy', priority: 0.6, category: 'discipline', content: 'N' },
        { key: 'clean', priority: 0.5, category: 'repair', content: 'C' },
      ] },
      { entries: [
        { key: 'noisy', priority: 0.6, category: 'discipline', content: 'N' },
        { key: 'clean', priority: 0.5, category: 'repair', content: 'C' },
      ] },
    ],
    renders: 2,
  },

  // ── lift 消费：负 lift → 静音 10 周期 ──
  lift_mute_negative: {
    lifts: { 'noisy': -0.5 },
    batches: Array.from({ length: 12 }, () => ({
      entries: [{ key: 'noisy', priority: 0.6, category: 'discipline', content: 'N' }],
    })),
    renders: 12,
  },

  // ── lift：lift = 0（≤ 阈值）也静音 ──
  lift_mute_zero: {
    lifts: { 'noisy': 0 },
    batches: Array.from({ length: 12 }, () => ({
      entries: [{ key: 'noisy', priority: 0.6, category: 'discipline', content: 'N' }],
    })),
    renders: 12,
  },

  // ── lift：正 lift 不静音 ──
  lift_positive_no_mute: {
    lifts: { 'good': 0.4 },
    batches: [{ entries: [{ key: 'good', priority: 0.6, category: 'discipline', content: 'G' }] }],
    renders: 1,
  },

  // ── lift：样本不足（null）视为中性，不静音 ──
  lift_null_neutral: {
    lifts: { 'unknown': null },
    batches: [{ entries: [{ key: 'unknown', priority: 0.6, category: 'discipline', content: 'U' }] }],
    renders: 1,
  },

  // ── lift：constitutional / immediate / star_domain 三类豁免 ──
  lift_exempt_tiers: {
    lifts: { 'const': -1, 'imm': -1, 'star': -1 },
    batches: [{ entries: [
      { key: 'const', priority: 0.9, category: 'constitutional', tier: 'constitutional', content: 'C' },
      { key: 'imm', priority: 0.7, category: 'guard', immediate: true, content: 'I' },
      { key: 'star', priority: 0.5, category: 'star_domain', content: 'S' },
    ] }],
    renders: 1,
  },

  // ── lift：静音只影响被静音的 key ──
  lift_mixed: {
    lifts: { 'noisy': -0.5, 'good': 0.4 },
    batches: [
      { entries: [
        { key: 'noisy', priority: 0.6, category: 'discipline', content: 'N' },
        { key: 'good', priority: 0.5, category: 'repair', content: 'G' },
      ] },
      { entries: [
        { key: 'noisy', priority: 0.6, category: 'discipline', content: 'N' },
        { key: 'good', priority: 0.5, category: 'repair', content: 'G' },
      ] },
    ],
    renders: 2,
  },

  // ── holdout：rng 命中 → 扣留（不渲染，但进 delivered 的 shadow 桶）──
  holdout_held: {
    holdout: { rate: 0.5, rng: [0.1], eligible: ['k'] },
    batches: [{ entries: [
      { key: 'k', priority: 0.6, category: 'discipline', content: 'X', expect: { kind: 'tool_appears', tools: ['bash'] } },
    ] }],
    renders: 1,
  },

  // ── holdout：rng 未命中 → 正常渲染 ──
  holdout_not_hit: {
    holdout: { rate: 0.5, rng: [0.9], eligible: ['k'] },
    batches: [{ entries: [
      { key: 'k', priority: 0.6, category: 'discipline', content: 'X', expect: { kind: 'tool_appears', tools: ['bash'] } },
    ] }],
    renders: 1,
  },

  // ── holdout：不合格 key（历史送达不足）永不抽样 ──
  holdout_ineligible: {
    holdout: { rate: 1.0, rng: [0.0], eligible: [] },
    batches: [{ entries: [
      { key: 'k', priority: 0.6, category: 'discipline', content: 'X', expect: { kind: 'tool_appears', tools: ['bash'] } },
    ] }],
    renders: 1,
  },

  // ── holdout：无 expect 谓词不抽样（扣留无法核销 = 无度量意义）──
  holdout_no_expect: {
    holdout: { rate: 1.0, rng: [0.0], eligible: ['k'] },
    batches: [{ entries: [{ key: 'k', priority: 0.6, category: 'discipline', content: 'X' }] }],
    renders: 1,
  },

  // ── holdout：constitutional 永不扣留 ──
  holdout_constitutional_exempt: {
    holdout: { rate: 1.0, rng: [0.0], eligible: ['c'] },
    batches: [{ entries: [
      { key: 'c', priority: 0.9, category: 'constitutional', tier: 'constitutional', content: 'C', expect: { kind: 'tool_appears', tools: ['bash'] } },
    ] }],
    renders: 1,
  },

  // ── holdout：immediate 永不扣留 ──
  holdout_immediate_exempt: {
    holdout: { rate: 1.0, rng: [0.0], eligible: ['i'] },
    batches: [{ entries: [
      { key: 'i', priority: 0.7, category: 'guard', immediate: true, content: 'I', expect: { kind: 'tool_appears', tools: ['bash'] } },
    ] }],
    renders: 1,
  },

  // ── holdout：star_domain 永不扣留 ──
  holdout_star_exempt: {
    holdout: { rate: 1.0, rng: [0.0], eligible: ['s'] },
    batches: [{ entries: [
      { key: 's', priority: 0.5, category: 'star_domain', content: 'S', expect: { kind: 'tool_appears', tools: ['bash'] } },
    ] }],
    renders: 1,
  },

  // ── holdout：rate = 0 关闭抽样 ──
  holdout_rate_zero: {
    holdout: { rate: 0, rng: [0.0], eligible: ['k'] },
    batches: [{ entries: [
      { key: 'k', priority: 0.6, category: 'discipline', content: 'X', expect: { kind: 'tool_appears', tools: ['bash'] } },
    ] }],
    renders: 1,
  },

  // ── holdout：同批多条，只扣留命中的那条 ──
  holdout_mixed: {
    holdout: { rate: 0.5, rng: [0.1, 0.9], eligible: ['a', 'b'] },
    batches: [{ entries: [
      { key: 'a', priority: 0.7, category: 'discipline', content: 'A', expect: { kind: 'tool_appears', tools: ['bash'] } },
      { key: 'b', priority: 0.6, category: 'discipline', content: 'B', expect: { kind: 'tool_appears', tools: ['bash'] } },
    ] }],
    renders: 1,
  },

  // ── T7 效力排序：高效力低 priority 胜出（跨 priority 竞争）──
  efficacy_sort_upset: {
    efficacy: { signals: {
      'high': { score: 0.95, confidence: 1 },
      'low': { score: 0.05, confidence: 1 },
    } },
    batches: [{ entries: [
      { key: 'high', priority: 0.58, category: 'discipline', content: 'H' },
      { key: 'low', priority: 0.70, category: 'discipline', content: 'L' },
    ] }],
    renders: 1,
  },

  // ── 效力：无样本（null）→ 零调整，纯 priority 排序 ──
  efficacy_no_signal: {
    efficacy: { signals: {} },
    batches: [{ entries: [
      { key: 'a', priority: 0.58, category: 'discipline', content: 'A' },
      { key: 'b', priority: 0.70, category: 'discipline', content: 'B' },
    ] }],
    renders: 1,
  },

  // ── 效力：中性 score=0.5 → 零调整 ──
  efficacy_neutral_score: {
    efficacy: { signals: { 'a': { score: 0.5, confidence: 1 }, 'b': { score: 0.5, confidence: 1 } } },
    batches: [{ entries: [
      { key: 'a', priority: 0.58, category: 'discipline', content: 'A' },
      { key: 'b', priority: 0.70, category: 'discipline', content: 'B' },
    ] }],
    renders: 1,
  },

  // ── 效力：constitutional 豁免（priority 原样，不被 clamp 压到 0.79）──
  efficacy_constitutional_exempt: {
    efficacy: { signals: { 'c': { score: 0.0, confidence: 1 } } },
    batches: [{ entries: [
      { key: 'c', priority: 0.9, category: 'constitutional', tier: 'constitutional', content: 'C' },
    ] }],
    renders: 1,
  },

  // ── 效力：immediate 豁免 ──
  efficacy_immediate_exempt: {
    efficacy: { signals: { 'i': { score: 0.0, confidence: 1 } } },
    batches: [{ entries: [
      { key: 'i', priority: 0.7, category: 'guard', immediate: true, content: 'I' },
    ] }],
    renders: 1,
  },

  // ── 效力：star_domain 豁免 ──
  efficacy_star_exempt: {
    efficacy: { signals: { 's': { score: 0.0, confidence: 1 } } },
    batches: [{ entries: [
      { key: 's', priority: 0.5, category: 'star_domain', content: 'S' },
    ] }],
    renders: 1,
  },

  // ── 效力：置信度缩放（单样本 confidence=0.2 → 调整幅度小）──
  efficacy_confidence_scaled: {
    efficacy: { signals: {
      'confident': { score: 1.0, confidence: 1.0 },
      'shaky': { score: 1.0, confidence: 0.2 },
    } },
    batches: [{ entries: [
      { key: 'shaky', priority: 0.60, category: 'discipline', content: 'S' },
      { key: 'confident', priority: 0.60, category: 'discipline', content: 'C' },
    ] }],
    renders: 1,
  },

  // ── 效力：span=0 关闭调整（span 由 env 控制，见主循环）──
  efficacy_span_zero: {
    efficacy: { signals: { 'a': { score: 1.0, confidence: 1 } }, span: 0 },
    batches: [{ entries: [
      { key: 'a', priority: 0.58, category: 'discipline', content: 'A' },
      { key: 'b', priority: 0.70, category: 'discipline', content: 'B' },
    ] }],
    renders: 1,
  },

  // ── immediate 条目豁免 CVM 注入预算 ──
  immediate_exempt: {
    batches: [{ entries: [
      { key: 'i1', priority: 0.5, category: 'discipline', content: 'I1', immediate: true },
      { key: 'i2', priority: 0.4, category: 'repair', content: 'I2', immediate: true },
      { key: 'n1', priority: 0.9, category: 'todo', content: 'N1' },
      { key: 'n2', priority: 0.8, category: 'dedup', content: 'N2' },
      { key: 'n3', priority: 0.7, category: 'immune', content: 'N3' },
      { key: 'n4', priority: 0.6, category: 'mistake', content: 'N4' },
    ] }],
    renders: 1,
  },
}

interface OracleEntry {
  /** 每次 render 的输出（空串 = 无内容） */
  renders: string[]
  /** 最终 ledger delta（累计值） */
  ledger: unknown
  /** 每次 render 后 drainDelivered 的 key 列表 */
  deliveredKeys: string[][]
}

const out: Record<string, OracleEntry> = {}
for (const [name, spec] of Object.entries(cases)) {
  // efficacySpan 是 readonly + 从 env 读（无运行时注入钩子）——用例级覆盖
  if (spec.efficacy?.span !== undefined) {
    process.env.RIVET_ADVISORY_EFFICACY_SPAN = String(spec.efficacy.span)
  } else {
    delete process.env.RIVET_ADVISORY_EFFICACY_SPAN
  }
  const bus = new AdvisoryBus()
  if (spec.streaks) {
    bus.setHabituationPolicy({ getIgnoredStreak: (key: string) => spec.streaks![key] ?? 0 })
  }
  if (spec.lifts) {
    bus.setLiftProvider((key: string) => spec.lifts![key] ?? null)
  }
  if (spec.efficacy) {
    bus.setEfficacySignalProvider((key: string) => spec.efficacy!.signals[key] ?? null)
  }
  if (spec.holdout) {
    let ri = 0
    const seq = spec.holdout.rng
    bus.setHoldoutPolicy({
      rate: spec.holdout.rate,
      isEligible: (key: string) => spec.holdout!.eligible.includes(key),
      // 固定序列 RNG：耗尽后返回 1（永不命中），保证可复现
      rng: () => (ri < seq.length ? seq[ri++]! : 1),
    })
  }
  const renders: string[] = []
  const deliveredKeys: string[][] = []

  for (let i = 0; i < spec.renders; i++) {
    const batch = spec.batches[i]
    if (batch) {
      for (const e of batch.entries) bus.submit(e as never)
    }
    const domain = batch?.domain
    renders.push(bus.render(domain, i))
    deliveredKeys.push(bus.drainDelivered().map(d => d.key))
  }

  out[name] = { renders, ledger: bus.drainLedger(), deliveredKeys }
}

writeFileSync(
  new URL('oracle.json', import.meta.url),
  JSON.stringify(out, null, 2) + '\n',
)
writeFileSync(
  new URL('cases.json', import.meta.url),
  JSON.stringify(cases, null, 2) + '\n',
)
