/**
 * PLAN §3——失败呈现层：分离「本机链路」与「外网提供商」，给出六类呈现粒度。
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { describeFailure } from '../failure-scope.js'

class ApiError extends Error {
  constructor(message: string, public readonly status: number) {
    super(message)
    this.name = 'ApiError'
  }
}

test('鉴权失败 → provider / auth', () => {
  const f = describeFailure(new ApiError('Unauthorized', 401))
  assert.equal(f.scope, 'provider')
  assert.equal(f.kind, 'auth')
})

test('限流 → provider / rate_limit', () => {
  const f = describeFailure(new ApiError('Rate limited', 429))
  assert.equal(f.scope, 'provider')
  assert.equal(f.kind, 'rate_limit')
})

test('服务端错误 → provider / server', () => {
  const f = describeFailure(new ApiError('Internal server error', 500))
  assert.equal(f.scope, 'provider')
  assert.equal(f.kind, 'server')
})

test('TLS 证书错误 → local / dns_proxy_tls', () => {
  const err = Object.assign(new Error('self signed certificate'), { code: 'DEPTH_ZERO_SELF_SIGNED_CERT' })
  const f = describeFailure(err)
  assert.equal(f.scope, 'local')
  assert.equal(f.kind, 'dns_proxy_tls')
})

test('DNS 解析失败（埋在 fetch cause 链里）→ local / dns_proxy_tls', () => {
  const cause = Object.assign(new Error('getaddrinfo ENOTFOUND api.example.com'), { code: 'ENOTFOUND' })
  const err = Object.assign(new Error('fetch failed'), { cause })
  const f = describeFailure(err)
  assert.equal(f.scope, 'local', 'DNS 失败是本机链路问题，不该报成上游')
  assert.equal(f.kind, 'dns_proxy_tls')
})

test('连接被拒 → local / offline', () => {
  const err = Object.assign(new Error('connect ECONNREFUSED 127.0.0.1:11434'), { code: 'ECONNREFUSED' })
  const f = describeFailure(err)
  assert.equal(f.scope, 'local')
  assert.equal(f.kind, 'offline')
})

test('代理要求鉴权（407）→ local / dns_proxy_tls', () => {
  const f = describeFailure(new ApiError('Proxy Authentication Required', 407))
  assert.equal(f.scope, 'local')
  assert.equal(f.kind, 'dns_proxy_tls')
})

test('未知错误 → provider / other（不假装知道）', () => {
  const f = describeFailure(new Error('something odd happened'))
  assert.equal(f.kind, 'other')
})
