import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import net from 'node:net'
import { fetchWithTimeout, isHttpResponseParseFailure } from '../fetch-timeout.js'
import { withStructuredRetry } from '../retry-engine.js'
import { classifyApiError } from '../error-classifier.js'

/**
 * 上游响应头畸形（网关/WAF 边缘节点拼接坏了）时的**端到端**兼容行为。
 *
 * 这不是构造出来的假错误：下面这个裸 TCP server 复刻的正是用户报告的那个字节
 * 形态（响应头区块里被插入一行空白字符），undici 抛的也正是那句
 * "Response does not match the HTTP/1.1 protocol (Unexpected whitespace after
 * header value)"。用例覆盖三件事：
 *   1. 这类失败会被判成 malformed_response（而不是笼统的「连接中断」）；
 *   2. **每次重试都是新 TCP 连接**——这才是「换个边缘节点」的真正机制，
 *      也是给「多试几次 + 短间隔」而不是长退避的依据；
 *   3. 上游恢复正常后请求能自愈成功，且重试预算耗尽时给出的那行自解释。
 */

const MALFORMED_HEAD = 'HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\n   \r\nCache-Control: no-cache\r\n\r\n'
const HEALTHY = 'HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nConnection: close\r\n\r\n'
  + 'data: {"choices":[{"delta":{"content":"ok"}}]}\r\n\r\ndata: [DONE]\r\n\r\n'

/** 前 `failures` 个请求回畸形响应头，之后回正常 SSE。统计 TCP 连接数。 */
async function flakyServer(failures: number) {
  const stats = { connections: 0, requests: 0 }
  const server = net.createServer((sock) => {
    stats.connections++
    sock.on('data', () => {
      stats.requests++
      sock.write(stats.requests <= failures ? MALFORMED_HEAD : HEALTHY)
      setTimeout(() => sock.end(), 20)
    })
    sock.on('error', () => { /* 客户端在解析失败后立刻断开是预期行为 */ })
  })
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()))
  const port = (server.address() as net.AddressInfo).port
  return { stats, url: `http://127.0.0.1:${port}/v1/chat/completions`, close: () => server.close() }
}

/**
 * 重试间隔压到 5ms：用例关心的是「会不会重试/是否换连接」，不是真等 800ms。
 *
 * 必须连 backoff 一起给——`overrides[c].retryDelayMs` 只在配了退避曲线时才参与
 * 计算（不配曲线时走分类器的固定间隔），只给 override 会静默按 800ms 跑。
 */
const fastPolicy = {
  backoff: { baseDelayMs: 5, maxDelayMs: 5, jitterRatio: 0 },
  overrides: { malformed_response: { retryDelayMs: 5 } },
}

describe('malformed HTTP response head — 兼容与自愈', () => {
  it('上游恢复正常后可自愈，且每次重试都是一条新 TCP 连接', async () => {
    const { stats, url, close } = await flakyServer(2)
    try {
      let attempts = 0
      const res = await withStructuredRetry(async () => {
        attempts++
        return await fetchWithTimeout(url, { method: 'POST', body: '{}' }, 3000)
      }, undefined, { policy: fastPolicy })

      assert.equal(res.status, 200)
      assert.equal(await res.text().then((t) => t.includes('"content":"ok"')), true)
      assert.equal(attempts, 3, '两次畸形 → 第三次成功')
      // 「换节点」的机制：undici 在解析失败时销毁该 socket，下一次尝试必然新建连接。
      assert.equal(stats.connections, attempts, '每次尝试一条新连接（同一条坏连接重试等于没换节点）')
    } finally { close() }
  })

  it('预算耗尽时如实失败，错误行自解释（不再是一句 parser 原文）', async () => {
    const { stats, url, close } = await flakyServer(Number.POSITIVE_INFINITY)
    try {
      let attempts = 0
      const err = await withStructuredRetry(async () => {
        attempts++
        return await fetchWithTimeout(url, { method: 'POST', body: '{}' }, 3000)
      }, undefined, { policy: fastPolicy }).then(() => null, (e: unknown) => e as Error)

      assert.ok(err, '持续畸形必须失败，不得假装成功')
      // 1 次初始 + 5 次重试 = 分类器给 malformed_response 的预算
      assert.equal(attempts, 6, `实测尝试次数 ${attempts}`)
      assert.equal(stats.connections, 6, '每次尝试都是新连接')
      assert.match(err.message, /upstream HTTP response malformed \(gateway\/WAF edge\)/)
      assert.match(err.message, /Unexpected whitespace after header value/, 'parser 原文保留，供提 issue 对照')
    } finally { close() }
  })

  it('真实链路分类：畸形响应头 → malformed_response（可重试、可换 provider）', async () => {
    const { url, close } = await flakyServer(Number.POSITIVE_INFINITY)
    try {
      const err = await fetchWithTimeout(url, { method: 'POST', body: '{}' }, 3000).then(() => null, (e: unknown) => e)
      assert.ok(err)
      const classified = classifyApiError(err)
      assert.equal(classified.category, 'malformed_response')
      assert.equal(classified.retryable, true)
      assert.equal(classified.shouldReconnect, true)
    } finally { close() }
  })
})

describe('isHttpResponseParseFailure — 判据边界', () => {
  it('认协议解析失败的各种正文', () => {
    assert.equal(isHttpResponseParseFailure('Response does not match the HTTP/1.1 protocol (Unexpected whitespace after header value)'), true)
    assert.equal(isHttpResponseParseFailure('Response does not match the HTTP/1.1 protocol (Unexpected space after start line)'), true)
    assert.equal(isHttpResponseParseFailure('HTTPParserError: invalid header token'), true)
  })

  it('不把连接类/超时失败误判成畸形响应', () => {
    assert.equal(isHttpResponseParseFailure('connect ECONNREFUSED 104.18.27.90:443'), false)
    assert.equal(isHttpResponseParseFailure('read ECONNRESET'), false)
    assert.equal(isHttpResponseParseFailure('getaddrinfo ENOTFOUND api.example.com'), false)
    assert.equal(isHttpResponseParseFailure('Request timed out: server did not respond within 60 seconds'), false)
  })
})
