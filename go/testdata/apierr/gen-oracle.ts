// error-classifier oracle：用真实 TS 分类器跑一组错误样本，导出分类结果。
//
// 运行：node_modules/.bin/tsx go/testdata/apierr/gen-oracle.ts
// 产出：go/testdata/apierr/oracle.json
import { writeFileSync } from 'node:fs'
import { classifyApiError, errorRecoveryGuidance, parseRetryAfterMs } from '../../../src/api/error-classifier.js'

/** 构造带 status 的错误（模拟 ApiError）。 */
function httpErr(status: number, msg: string, extra?: Record<string, unknown>): Error {
  const e = new Error(msg) as Error & Record<string, unknown>
  e.status = status
  if (extra) Object.assign(e, extra)
  return e
}

const cases: Array<{ name: string; error: unknown }> = [
  // 状态码分支
  { name: 'status_429', error: httpErr(429, 'Too many requests') },
  { name: 'status_529', error: httpErr(529, 'Overloaded') },
  { name: 'status_503', error: httpErr(503, 'Service Unavailable') },
  { name: 'status_500', error: httpErr(500, 'Internal error') },
  { name: 'status_502', error: httpErr(502, 'Bad gateway') },
  { name: 'status_408', error: httpErr(408, 'Request timeout') },
  { name: 'status_425', error: httpErr(425, 'Too Early') },
  { name: 'status_401', error: httpErr(401, 'Unauthorized') },
  { name: 'status_403', error: httpErr(403, 'Forbidden') },
  { name: 'status_404', error: httpErr(404, 'Not found') },
  { name: 'status_400', error: httpErr(400, 'Bad request') },
  { name: 'status_501', error: httpErr(501, 'Not implemented') },

  // 413 三分支
  { name: 'status_413_had_images', error: httpErr(413, 'Payload too large', { payloadHadImages: true }) },
  { name: 'status_413_no_images', error: httpErr(413, 'Payload too large', { payloadHadImages: false }) },
  { name: 'status_413_unknown', error: httpErr(413, 'Payload too large') },

  // Retry-After 覆盖
  { name: 'retry_after_override', error: httpErr(429, 'Rate limited', { retryAfterMs: 7000 }) },

  // 图片处理错误（400 包裹）
  { name: 'image_process_400', error: httpErr(400, 'Could not process image: too large') },
  { name: 'image_process_500', error: httpErr(500, 'upstream: 400 invalid image format') },

  // 非 SSE 200
  { name: 'non_sse', error: Object.assign(new Error('Endpoint returned a non-SSE 200 response.'), { nonSse: true }) },

  // 模式分支（无 status）
  { name: 'network_econnrefused', error: new Error('fetch failed: ECONNREFUSED') },
  { name: 'network_econnreset', error: new Error('ECONNRESET') },
  { name: 'network_enotfound', error: new Error('getaddrinfo ENOTFOUND api.example.com') },
  { name: 'timeout_msg', error: new Error('Request timeout after 30000ms') },
  { name: 'overloaded_msg', error: new Error('Service temporarily unavailable') },
  { name: 'too_busy', error: new Error('Server too busy') },
  { name: 'empty_stream', error: new Error('empty_stream: upstream closed') },
  { name: 'context_overflow', error: new Error('prompt is too long: 200000 tokens') },
  { name: 'context_length_exceeded', error: new Error('context_length_exceeded') },
  { name: 'stream_parse', error: new Error('invalid SSE event received') },
  { name: 'unknown_fallback', error: new Error('something completely unexpected') },

  // 消息里带状态码（无 status 字段）
  { name: 'status_in_message', error: new Error('Codex API error (429): rate limited') },
  { name: 'status_in_message_503', error: new Error('upstream returned (503)') },
]

const out: Record<string, unknown> = {}
for (const c of cases) {
  const r = classifyApiError(c.error)
  out[c.name] = {
    retryable: r.retryable,
    retryDelayMs: r.retryDelayMs,
    shouldReconnect: r.shouldReconnect,
    category: r.category,
    maxRetries: r.maxRetries,
    stripImages: r.stripImages ?? false,
    retryDelayFromServer: r.retryDelayFromServer ?? false,
  }
}

// Retry-After 解析
out['__retryAfter'] = {
  numeric_seconds: parseRetryAfterMs('5'),
  numeric_zero: parseRetryAfterMs('0'),
  unparseable: parseRetryAfterMs('not-a-number') ?? null,
  fractional: parseRetryAfterMs('1.5'),
}

// 恢复指引（按类别）
out['__guidance'] = {
  rate_limit: errorRecoveryGuidance(httpErr(429, 'x')),
  auth_error: errorRecoveryGuidance(httpErr(401, 'x')),
  client_error: errorRecoveryGuidance(httpErr(404, 'x')),
  context_overflow: errorRecoveryGuidance(new Error('prompt is too long')),
  unknown: errorRecoveryGuidance(new Error('weird thing')),
}

writeFileSync(new URL('oracle.json', import.meta.url), JSON.stringify(out, null, 2) + '\n')
