import { test } from 'node:test'
import assert from 'node:assert/strict'
import { classifyCostModel, isCostInsensitiveProvider } from '../cost-model.js'

test('subscription providers classify as subscription', () => {
  for (const name of ['glm', 'mimo', 'codex', 'claude', 'volc-plan', 'volc-plan-anthropic']) {
    assert.equal(classifyCostModel(name), 'subscription', name)
    assert.equal(isCostInsensitiveProvider(name), true, name)
  }
})

test('per-token providers classify as per-token', () => {
  for (const name of ['deepseek', 'mimo-api', 'minimax', 'openai', 'anthropic', 'qwen', 'kimi', 'vllm']) {
    assert.equal(classifyCostModel(name), 'per-token', name)
    assert.equal(isCostInsensitiveProvider(name), false, name)
  }
})

test('classification is case-insensitive and trims', () => {
  assert.equal(classifyCostModel('  GLM  '), 'subscription')
  assert.equal(classifyCostModel('DeepSeek'), 'per-token')
})

test('unknown provider defaults to per-token (conservative)', () => {
  assert.equal(classifyCostModel('some-custom-llm'), 'per-token')
  assert.equal(classifyCostModel(undefined), 'per-token')
  assert.equal(classifyCostModel(''), 'per-token')
})

test('oauth auth hint promotes unknown provider to subscription', () => {
  assert.equal(classifyCostModel('some-custom-llm', { authType: 'oauth' }), 'subscription')
  assert.equal(isCostInsensitiveProvider('some-custom-llm', { authType: 'oauth' }), true)
})

test('coding-plan / token-plan baseUrl hint promotes to subscription', () => {
  assert.equal(classifyCostModel('custom', { baseUrl: 'https://x.example.com/api/coding/v1' }), 'subscription')
  assert.equal(classifyCostModel('custom', { baseUrl: 'https://token-plan-cn.example.com/v1' }), 'subscription')
  // 火山方舟 Agent Plan：自定义 provider 直接粘贴官方 Base URL（issue #272）
  assert.equal(classifyCostModel('custom', { baseUrl: 'https://ark.cn-beijing.volces.com/api/plan/v3' }), 'subscription')
  assert.equal(classifyCostModel('custom', { baseUrl: 'https://api.example.com/v1' }), 'per-token')
})

test('订阅端点 URL 比 provider 名更具体：kimi Code 订阅 vs Moonshot 按量', () => {
  // Kimi Code 预设定价清零、按订阅额度计费——同名开放平台端点仍按量
  assert.equal(classifyCostModel('kimi', { baseUrl: 'https://api.kimi.com/coding/v1' }), 'subscription')
  assert.equal(isCostInsensitiveProvider('kimi', { baseUrl: 'https://api.kimi.com/coding/v1' }), true)
  assert.equal(classifyCostModel('kimi', { baseUrl: 'https://api.moonshot.cn/v1' }), 'per-token')
  assert.equal(classifyCostModel('kimi'), 'per-token', '无 baseUrl 时保持按量名单现状')
})

test('known provider name wins over oauth hint', () => {
  // deepseek is per-token even if someone passes an oauth hint
  assert.equal(classifyCostModel('deepseek', { authType: 'oauth' }), 'per-token')
})
