/**
 * endpoint-map — configurable probe-endpoint path mapping.
 *
 * The connectivity probe needs two URLs per provider: the model list
 * (GET …/models) and a minimal completion (POST …/chat/completions). Where
 * the "/v1" version segment lives differs per deployment:
 *   openai-style : base_url = https://api.openai.com/v1   → append paths directly
 *   oneapi-style : base_url = http://localhost:3000/api   → paths carry /v1
 * Users also paste full request URLs (…/v1/chat/completions) into the base-URL
 * field — normalizeBaseUrl() strips those tails so we never double-append
 * ("/chat/completions/chat/completions") or probe "…/chat/completions/models".
 */

export interface EndpointPaths {
  /** Path appended to the base URL for GET model list. */
  models: string
  /** Path appended to the base URL for POST minimal completion. */
  chat: string
  /** Path appended to the base URL for POST Responses API completion
   *  (protocol 'openai-responses', issue #239). */
  responses: string
  /** Path appended to the base URL for POST image generation (issue #8). */
  images: string
}

/** Unified default for OpenAI-compatible endpoints (unknown providers included). */
export const DEFAULT_ENDPOINT_PATHS: EndpointPaths = {
  models: '/models',
  chat: '/chat/completions',
  responses: '/responses',
  images: '/images/generations',
}

/**
 * Per-provider overrides keyed by provider/preset name. Absent entries fall
 * back to DEFAULT_ENDPOINT_PATHS. Paths are relative to the NORMALIZED base
 * URL (version segment stripped, see resolveProbeEndpoints).
 */
export const PROVIDER_ENDPOINT_MAP: Record<string, Partial<EndpointPaths>> = {
  // All built-in presets are version-in-base OpenAI-compatible — defaults fit.
  // Add entries here for exotic deployments (azure query params, etc.).
}

/**
 * 火山方舟订阅制端点没有 GET /models（issue #272 根因）：Agent Plan
 * `/api/plan/v3` 与 Coding Plan `/api/coding/v3` 的 OpenAI 兼容面只提供
 * chat/completions 与 responses，模型目录走控制台 / 控制面 OpenAPI。连接测试打
 * 到 `/models` 会 404，把「能对话的 Key」误判成「baseUrl 填错」，provider 直接
 * 卡在保存前。
 *
 * 双判据：
 *  - provider 名 → 覆盖内置预设（自定义名不命中）；
 *  - base URL → 覆盖照官方文档手填该地址的自定义 provider（issue #272 用户的
 *    实际路径）。
 * 命中后探测降级为最小补全，需要调用方给 probeModel（预设 defaultModelId /
 * 用户手填模型 / 已存 provider 首个模型）。base URL 判据刻意只认官方域名+路径，
 * 避免误伤「只是碰巧 404 /models」的普通网关（那条走 probeForTestKey 的 404
 * 兜底，语义更保守）。
 */
const NO_MODELS_LIST_PROVIDERS = new Set(['volc-plan', 'volc-plan-anthropic'])
const NO_MODELS_LIST_BASE_URL_PATTERNS: readonly RegExp[] = [
  // OpenAI 面 /api/plan/v3、Coding Plan /api/coding/v3、Anthropic 面 /api/plan
  /^https:\/\/ark\.cn-beijing\.volces\.com\/api\/(?:plan|coding)(?:\/v3)?$/i,
]

/** 该端点是否有 OpenAI 形态的 GET /models 模型列表。 */
export function hasModelsListEndpoint(providerName: string | undefined, baseUrl: string): boolean {
  if (providerName && NO_MODELS_LIST_PROVIDERS.has(providerName)) return false
  const base = normalizeBaseUrl(baseUrl)
  return !NO_MODELS_LIST_BASE_URL_PATTERNS.some(pattern => pattern.test(base))
}

/** Tails users paste from curl/docs that are request paths, not the base URL.
 *  Longest first; the version segment (/v1) stays — it belongs to the base. */
const STRIPPABLE_SUFFIXES = [
  // issue #8：注册生图 provider 时，用户最常粘贴的就是完整生图端点 URL
  // （从 provider 文档的 curl 示例里复制）。不剥这个尾巴，后续拼接会得到
  // …/images/generations/models → 404，而报错只提 "path may be wrong"。
  '/images/generations',
  '/chat/completions',
  '/completions',
  '/responses',
  '/messages',
  '/models',
  '/embeddings',
]

/** Strip trailing slashes and known request-path tails from a user-supplied base URL. */
export function normalizeBaseUrl(raw: string): string {
  let url = raw.trim().replace(/\/+$/, '')
  for (const suffix of STRIPPABLE_SUFFIXES) {
    if (url.toLowerCase().endsWith(suffix)) {
      url = url.slice(0, -suffix.length).replace(/\/+$/, '')
      break
    }
  }
  return url
}

export interface ResolvedProbeEndpoints {
  /** Normalized base (no trailing slash, no request-path tail). */
  base: string
  modelsUrl: string
  chatUrl: string
  /** Responses API endpoint — used by the 'openai-responses' onboarding probe. */
  responsesUrl: string
  /** Image-generation endpoint — used by the image-gen onboarding probe (issue #8). */
  imagesUrl: string
}

/**
 * Resolve the probe URLs for a base URL. `providerName` selects an override
 * from PROVIDER_ENDPOINT_MAP; anything unknown (custom providers included)
 * uses the OpenAI-compatible default. When the base carries no version
 * segment (oneapi-style "/api"), "/v1" is inserted before the paths.
 */
export function resolveProbeEndpoints(baseUrl: string, providerName?: string): ResolvedProbeEndpoints {
  const base = normalizeBaseUrl(baseUrl)
  const override = providerName ? PROVIDER_ENDPOINT_MAP[providerName] : undefined
  const paths: EndpointPaths = { ...DEFAULT_ENDPOINT_PATHS, ...override }
  const versioned = /\/v\d+$/i.test(base)
  const prefix = versioned ? '' : '/v1'
  return {
    base,
    modelsUrl: `${base}${prefix}${paths.models}`,
    chatUrl: `${base}${prefix}${paths.chat}`,
    responsesUrl: `${base}${prefix}${paths.responses}`,
    imagesUrl: `${base}${prefix}${paths.images}`,
  }
}
