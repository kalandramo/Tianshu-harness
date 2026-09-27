/**
 * 逐供应商档位枚举兼容（出站请求体断言）。
 *
 * 每个端点的官方 reasoning_effort 枚举不同（DeepSeek none|low|high|max、Kimi
 * low|high|max、SiliconFlow high|max、方舟 none…max……）。内部统一梯子是
 * off/low/medium/high/max，兼容由 preset/WELL_KNOWN 的 effortCap + 模型级
 * effortFormat:'none' 承担。本文件从**真实 preset 声明**出发构建客户端，
 * 断言越界档位被映射、永不直发 off、未支持型号不发该字段。
 */

import { test } from 'node:test'
import assert from 'node:assert/strict'
import { OpenAIClient, type OpenAIClientConfig } from '../openai-client.js'
import { resolveCapabilities } from '../provider.js'
import { PROVIDER_PRESETS, type ProviderPresetKey } from '../../config/provider-presets.js'
import type { OaiChatRequest } from '../oai-types.js'

async function captureBody(config: OpenAIClientConfig, request: OaiChatRequest): Promise<Record<string, unknown>> {
  const orig = globalThis.fetch
  let captured: Record<string, unknown> = {}
  globalThis.fetch = (async (_url: string, init: RequestInit) => {
    captured = JSON.parse(init.body as string)
    const stream = new ReadableStream({
      start(controller) {
        controller.enqueue(new TextEncoder().encode('data: [DONE]\n\n'))
        controller.close()
      },
    })
    return new Response(stream, { status: 200, headers: { 'content-type': 'text/event-stream' } })
  }) as unknown as typeof fetch
  try {
    const client = new OpenAIClient(config)
    const noop: import('../stream-client.js').StreamCallbacks = {
      onTextDelta: () => {},
      onThinkingDelta: () => {},
      onContentBlock: () => {},
      onStopReason: () => {},
      onError: () => {},
    }
    await client.stream(request, noop)
  } finally {
    globalThis.fetch = orig
  }
  return captured
}

/** 用真实 preset 的 provider/模型 capabilities 解析出客户端配置。 */
function configFor(providerName: ProviderPresetKey, modelId: string, reasoningEffort: string): OpenAIClientConfig {
  const preset = PROVIDER_PRESETS[providerName]
  const model = preset.provider.models.find(m => m.id === modelId)
  const caps = resolveCapabilities(providerName, preset.provider.capabilities, model?.capabilities)
  return {
    baseUrl: 'https://example.com/v1',
    apiKey: 'sk-test',
    model: modelId,
    maxTokens: 8192,
    providerName,
    thinking: 'enabled',
    thinkingBlockType: caps.thinkingBlockType,
    effortFormat: caps.effortFormat,
    effortCap: caps.effortCap,
    reasoningEffort,
  }
}

const request: OaiChatRequest = {
  model: 'probe',
  messages: [{ role: 'user', content: 'hi' }],
  max_tokens: 8192,
}

async function wireEffort(providerName: ProviderPresetKey, modelId: string, effort: string): Promise<unknown> {
  const body = await captureBody(configFor(providerName, modelId, effort), request)
  return body.reasoning_effort
}

test('deepseek：官方 none|low|high|max —— off→none、medium→high、max 原样', async () => {
  assert.equal(await wireEffort('deepseek', 'deepseek-v4-pro', 'off'), 'none')
  assert.equal(await wireEffort('deepseek', 'deepseek-v4-pro', 'medium'), 'high')
  assert.equal(await wireEffort('deepseek', 'deepseek-v4-pro', 'max'), 'max')
  assert.equal(await wireEffort('deepseek', 'deepseek-v4-pro', 'low'), 'low')
})

test('kimi：官方 low|high|max + none 关思考 —— off→none、medium→high、max 不降级', async () => {
  assert.equal(await wireEffort('kimi', 'k3', 'off'), 'none')
  assert.equal(await wireEffort('kimi', 'k3', 'medium'), 'high')
  assert.equal(await wireEffort('kimi', 'k3', 'max'), 'max')
  assert.equal(await wireEffort('kimi', 'k3', 'high'), 'high')
  assert.equal(
    await wireEffort('kimi', 'kimi-for-coding-highspeed', 'high'),
    undefined,
    'Thinking:ON 无档位模型不发 reasoning_effort',
  )
})

test('glm Coding Plan：off→none、medium→high（官方 Coding Plan 映射）', async () => {
  assert.equal(await wireEffort('glm', 'glm-5.3', 'off'), 'none')
  assert.equal(await wireEffort('glm', 'glm-5.3', 'medium'), 'high')
  assert.equal(await wireEffort('glm', 'glm-5.3', 'max'), 'max')
})

test('volc：档位通道只挂 doubao-seed-2.0-pro（支持表内），off→none、7 档透传', async () => {
  assert.equal(await wireEffort('volc', 'doubao-seed-2.0-pro', 'off'), 'none')
  assert.equal(await wireEffort('volc', 'doubao-seed-2.0-pro', 'medium'), 'medium')
  assert.equal(await wireEffort('volc', 'doubao-seed-2.0-pro', 'high'), 'high')
  assert.equal(await wireEffort('volc', 'doubao-seed-2.0-flash', 'high'), undefined, '未验证型号不发字段')
})

test('volc-plan：Ark 7 档全接受（off→none）；kimi/minimax 模型级关闭', async () => {
  assert.equal(await wireEffort('volc-plan', 'deepseek-v4-pro', 'off'), 'none')
  assert.equal(await wireEffort('volc-plan', 'deepseek-v4-pro', 'max'), 'max')
  assert.equal(await wireEffort('volc-plan', 'kimi-k3', 'high'), undefined, '未列入方舟支持表 → 模型级关闭')
  assert.equal(await wireEffort('volc-plan', 'minimax-m3', 'medium'), undefined)
})

test('siliconflow：官方 high|max —— low/medium 对齐 high；支持清单外型号不发', async () => {
  assert.equal(await wireEffort('siliconflow', 'deepseek-ai/DeepSeek-V4-Flash', 'low'), 'high')
  assert.equal(await wireEffort('siliconflow', 'deepseek-ai/DeepSeek-V4-Flash', 'medium'), 'high')
  assert.equal(await wireEffort('siliconflow', 'deepseek-ai/DeepSeek-V4-Flash', 'high'), 'high')
  assert.equal(await wireEffort('siliconflow', 'moonshotai/Kimi-K2.7-Code', 'high'), undefined)
  assert.equal(await wireEffort('siliconflow', 'Qwen/Qwen3.6-27B', 'high'), undefined)
})
