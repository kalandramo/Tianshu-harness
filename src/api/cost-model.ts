/**
 * Provider cost model — how a provider bills, independent of its cache type.
 *
 * Two orthogonal axes drive compaction policy:
 *   - cache type (exact-prefix / none …) → governs cache *correctness/latency*
 *     (breaking an exact-prefix cache forces an expensive re-prefill).
 *   - cost model (this file) → governs whether tokens cost *money*.
 *
 * They are NOT the same. DeepSeek is per-token AND exact-prefix, so its cache
 * deferral saves real money. GLM/MiMo are exact-prefix but billed by a flat
 * coding-plan / token-plan subscription — their cache still matters for
 * re-prefill latency, but NOT for cost. Codex/Claude are OAuth subscriptions
 * with no persistent prefix cache at all.
 *
 * Used by the turn-0 quality-compaction path: subscription providers may compact
 * more eagerly for a leaner context (cost is flat), while per-token exact-prefix
 * providers keep deferring to protect paid cache.
 */

export type CostModel = 'per-token' | 'subscription'

/** Providers billed by a flat subscription / coding-plan (tokens are not metered). */
const SUBSCRIPTION_PROVIDERS = new Set<string>([
  'glm', // 智谱 coding plan (open.bigmodel.cn/api/coding/...)
  'mimo', // Xiaomi MiMo token-plan (token-plan-cn.xiaomimimo.com)
  'codex', // ChatGPT subscription via OAuth
  'claude', // Claude Max/Pro via OAuth
  'volc-plan', // 火山方舟 Agent Plan（AFP 套餐额度，非按 token；issue #272）
  'volc-plan-anthropic', // 同一订阅的 Messages 端点
])

/** Providers billed per API token (cache hits save real money). */
const PER_TOKEN_PROVIDERS = new Set<string>([
  'deepseek',
  'mimo-api', // Xiaomi pay-per-use API (api.xiaomimimo.com)
  'minimax',
  'openai',
  'anthropic',
  'google',
  'qwen',
  'kimi',
  'vllm',
  'opencode-go',
  'siliconflow', // aggregator — per-token
  'dashscope', // 阿里通义千问 — per-token
  'stepfun', // 阶跃星辰 StepFun — per-token（官方开放平台按量计费，非订阅制）
  'openrouter', // aggregator — per-token
  'relay', // self-hosted one-api/new-api relay — per-token (default assumption)
])

export interface CostModelHints {
  /** Auth type from the provider config; 'oauth' implies a subscription. */
  authType?: string
  /** Provider baseUrl; subscription endpoints (coding-plan / token-plan / 火山 plan)
   *  imply a subscription — 端点计费属性，比 provider 名更具体。 */
  baseUrl?: string
}

/** 订阅制端点的 baseUrl 特征（小写子串匹配）。 */
const SUBSCRIPTION_BASE_URL_MARKERS = ['/coding/', 'token-plan', '/api/plan/'] as const

function isSubscriptionBaseUrl(baseUrl: string | undefined): boolean {
  if (!baseUrl) return false
  const url = baseUrl.toLowerCase()
  return SUBSCRIPTION_BASE_URL_MARKERS.some(marker => url.includes(marker))
}

/**
 * Classify how a provider bills.
 *
 * Order matters and is deliberate:
 *   1. 订阅制 provider 名（glm / mimo / codex / claude / 火山 plan）；
 *   2. **订阅制端点 URL**——同一 provider 名可能横跨订阅与按量两套端点：内置
 *      `kimi` 预设走 `api.kimi.com/coding`（Kimi Code 会员订阅额度），而 Moonshot
 *      开放平台按量端点是同名 'kimi'；只看名字无法区分，baseUrl 是更具体的事实。
 *      已有行为（自定义 provider 贴订阅 URL）不变，只把判定提前到按量名单之前。
 *   3. 按量 provider 名；4. oauth 兜底（保持"名字优先于 oauth 提示"的现状）；
 *   5. 未知默认按量（保守：不为未知端点放松缓存保护）。
 */
export function classifyCostModel(providerName: string | undefined, hints: CostModelHints = {}): CostModel {
  const name = (providerName ?? '').trim().toLowerCase()
  if (SUBSCRIPTION_PROVIDERS.has(name)) return 'subscription'
  if (isSubscriptionBaseUrl(hints.baseUrl)) return 'subscription'
  if (PER_TOKEN_PROVIDERS.has(name)) return 'per-token'
  if (hints.authType === 'oauth') return 'subscription'
  return 'per-token'
}

/** True when the provider does NOT bill per API token (flat subscription / coding-plan). */
export function isCostInsensitiveProvider(providerName: string | undefined, hints: CostModelHints = {}): boolean {
  return classifyCostModel(providerName, hints) === 'subscription'
}
