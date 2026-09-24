/**
 * 契约层模型池口径——只在 keys 池里的模型必须能被解析，顶层快照里已删的不许再被解析。
 *
 * 背景：src/config/provider-keys.ts 头注释规定「消费方一律走 contractModels」——
 * keys 池是模型的事实源（key 级增删只写 keys[i].models，顶层 provider.models 是迁移
 * 那一刻的快照）。而 resolveProviderForModel 此前只读顶层快照，于是：
 *
 *   - 「只在 key 池里」的模型一律 "not found in any provider"——`/model` 切换与
 *     startup resume 都走这个函数；
 *   - 「顶层还留着、key 池里已删」的模型反而解析成功——用户删了模型却还在用。
 *
 * 同口径的第二个消费方是 createAgentRuntime（它的池内比失配后会位置性回退到
 * models[0]，把请求打到另一个档——400 / 429 的来源）。
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { resolveProviderForModel } from '../bootstrap.js'
import type { BootstrapContext } from '../bootstrap.js'

function makeCtx(providers: Record<string, unknown>, currentName = 'p1'): BootstrapContext {
  return {
    config: { provider: { providers } },
    provider: { name: currentName },
  } as unknown as BootstrapContext
}

test('只在 keys 池里的模型可被解析（顶层快照不含它）', () => {
  const ctx = makeCtx({
    p1: {
      name: 'p1',
      apiKey: 'k1',
      // 顶层是迁移时的快照：用户后来在 key 上新增的模型不在其中。
      models: [{ id: 'stale-top-level' }],
      keys: [{ id: 'default', models: [{ id: 'only-in-key', contextWindow: 200_000 }] }],
    },
  })

  const resolved = resolveProviderForModel(ctx, 'only-in-key')
  assert.ok(resolved && !('error' in resolved), 'key 池里的模型必须解析得到')
  assert.equal(resolved.providerName, 'p1')
  assert.equal(resolved.modelId, 'only-in-key')
  assert.equal(resolved.contextWindow, 200_000)
})

test('顶层快照里还在、key 池里已删的模型不得再被解析', () => {
  const ctx = makeCtx({
    p1: {
      name: 'p1',
      apiKey: 'k1',
      models: [{ id: 'deleted-model' }],
      keys: [{ id: 'default', models: [{ id: 'kept' }] }],
    },
  })

  assert.equal(
    resolveProviderForModel(ctx, 'deleted-model'),
    null,
    '顶层快照是历史遗留，不该让用户已删的模型继续可用',
  )
  assert.ok(resolveProviderForModel(ctx, 'kept'), 'key 池里的模型仍应可解析')
})

test('多 key 池时并集内任一 key 的模型都可解析', () => {
  const ctx = makeCtx({
    p1: {
      name: 'p1',
      apiKey: 'k1',
      models: [],
      keys: [
        { id: 'default', models: [{ id: 'first-key-model' }] },
        { id: 'backup', models: [{ id: 'second-key-model' }] },
      ],
    },
  })

  assert.equal(
    (resolveProviderForModel(ctx, 'second-key-model') as { modelId?: string } | null)?.modelId,
    'second-key-model',
  )
})
