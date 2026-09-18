// 端到端对账：真实 oracle（src/api/provider.ts）vs Go 实现。
// 导出每个已知提供商的三层合并结果，供 Go 侧逐字段比对。
//
// 运行：npx tsx go/testdata/provider/gen-oracle.ts
import { writeFileSync } from 'node:fs'
import { resolveCapabilities, WELL_KNOWN_DEFAULTS, DEFAULT_CAPABILITIES } from '../../../src/api/provider.js'

const providerNames = [
  ...Object.keys(WELL_KNOWN_DEFAULTS),
  'no-such-provider-xyz', // 未知提供商 → DEFAULT_CAPABILITIES 兜底
]

const out: Record<string, unknown> = {}
for (const name of providerNames) {
  const c = resolveCapabilities(name, undefined, undefined)
  out[name] = {
    supportsThinking: c.supportsThinking,
    thinkingBlockType: c.thinkingBlockType,
    reasoningSplit: c.reasoningSplit ?? false,
    thinkingBudgetField: c.thinkingBudgetField ?? '',
    preservedThinkingProtocol: c.preservedThinkingProtocol ?? false,
    supportsCacheControl: c.supportsCacheControl,
    stripParams: c.stripParams ?? [],
    hasToolJsonInContentBug: c.hasToolJsonInContentBug,
    effortFormat: c.effortFormat,
    prefixCacheStrategy: c.prefixCacheStrategy,
    supportsResponseFormat: c.supportsResponseFormat,
    effortCap: c.effortCap ?? null,
    hasMapUsage: typeof c.mapUsage === 'function',
  }
}

// 三层合并顺序 + 覆盖语义
out['__cuts'] = {
  providerLayer: (() => {
    const c = resolveCapabilities('deepseek', { effortFormat: 'none' }, undefined)
    return c.effortFormat
  })(),
  modelLayerWins: (() => {
    const c = resolveCapabilities('deepseek', { effortFormat: 'none' }, { effortFormat: 'output_config' })
    return c.effortFormat
  })(),
  emptyStripParamsIsNoOpinion: (() => {
    const c = resolveCapabilities('deepseek', { stripParams: [] }, undefined)
    return c.stripParams
  })(),
  explicitStripParams: (() => {
    const c = resolveCapabilities('deepseek', { stripParams: ['only_this'] }, undefined)
    return c.stripParams
  })(),
  deriveThinkingFromBlock: (() => {
    const c = resolveCapabilities('longcat', { thinkingBlock: 'enabled' }, undefined)
    return c.supportsThinking
  })(),
  deriveNoThinking: (() => {
    const c = resolveCapabilities('deepseek', { thinkingBlock: 'none', effortFormat: 'none' }, undefined)
    return c.supportsThinking
  })(),
  undeclaredFallsThrough: (() => {
    const c = resolveCapabilities('deepseek', { effortFormat: 'none' }, undefined)
    return { prefixCacheStrategy: c.prefixCacheStrategy, preservedThinkingProtocol: c.preservedThinkingProtocol }
  })(),
}

// usage 归一化（真实 oracle）
out['__usage'] = {
  deepseekNative: (() => {
    const m = WELL_KNOWN_DEFAULTS['deepseek']?.mapUsage
    return m ? m({ prompt_tokens: 100, completion_tokens: 20, prompt_cache_hit_tokens: 80, prompt_cache_miss_tokens: 20 }) : null
  })(),
  anthropicCompat: (() => {
    const m = WELL_KNOWN_DEFAULTS['deepseek']?.mapUsage
    return m ? m({ input_tokens: 100, output_tokens: 20, cache_read_input_tokens: 80, cache_creation_input_tokens: 20 }) : null
  })(),
  nestedCached: (() => {
    const m = WELL_KNOWN_DEFAULTS['deepseek']?.mapUsage
    return m ? m({ prompt_tokens: 100, completion_tokens: 20, prompt_tokens_details: { cached_tokens: 55 } }) : null
  })(),
}

void DEFAULT_CAPABILITIES

writeFileSync(
  new URL('oracle.json', import.meta.url),
  JSON.stringify(out, null, 2) + '\n',
)
