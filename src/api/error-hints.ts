/**
 * API 错误 → 可操作提示。
 *
 * 从 `openai-client.ts` 沿接缝拆出（2026-09-24）：该文件是点名巨石（ceiling 只降
 * 不升），而这一族是自包含的纯字符串处理——不碰客户端状态、不依赖流式循环，只把
 * provider 上下文字段读进来判断「哪家、什么问题、怎么办」。拆出后原文件回到 ceiling
 * 以内，后续给错误提示加分支也不再往巨石里塞。
 */
import { MAX_JSON_BODY_BYTES } from '../utils/sanitize.js'

export interface ApiErrorProviderContext {
  /** Provider name for feature gating (e.g. 'deepseek', 'glm') */
  providerName?: string
  baseUrl?: string
  /** Env var holding the API key — named in 401/403 hints so users know where to look. */
  apiKeyEnv?: string
}

/**
 * 可识别错误的行动指引。原始报错（如 "Insufficient Balance"）只陈述现象，
 * 用户得自己猜是哪家的账户、去哪充值——在报错后追加一行中文提示，直接给
 * 结论：哪家、余额不足、充值入口、临时退路（/model 换 provider）。
 */
function apiErrorHint(code: string, message: string, provider?: ApiErrorProviderContext): string {
  const probe = `${code} ${typeof message === 'string' ? message : ''}`
  // 请求体被判为非法 JSON（provider 网关的 serde 报错原样透传，如
  // "Failed to parse the request body as JSON: messages[N].content unexpected end
  // of hex escape"）：这是**我们发出去的体**在上游被按字节切断，不是模型、不是
  // 密钥、也不是余额问题。用户看到的只是一句英文解析错误——给结论 + 出路。
  if (/parse the request body|unexpected end of hex escape|as JSON:|invalid json/i.test(probe)) {
    const knob = provider?.providerName
      ? `provider.providers.${provider.providerName}.maxBodyBytes`
      : 'provider.providers.<name>.maxBodyBytes'
    return (
      '\n提示：请求体被上游判为非法 JSON（多为对话体量超限被按字节截断）。用 /compact 压缩本会话或新开会话继续；' +
      '若 baseUrl 走第三方中转，中转常有更小的 body 上限。发送前体积护栏默认关闭——可在该 provider 配置里设 ' +
      `${knob}（字节，如 ${MAX_JSON_BODY_BYTES}）启用：超限时自动截断历史工具输出，避免这类 400。`
    )
  }
  if (!/insufficient[ _-]?(balance|quota)|余额不足|额度不足/i.test(probe)) return ''

  const where = `${provider?.providerName ?? ''} ${provider?.baseUrl ?? ''}`.toLowerCase()
  const BILLING: Array<[RegExp, string, string]> = [
    [/deepseek/, 'DeepSeek', 'https://platform.deepseek.com/top_up'],
    [/siliconflow|硅基/, 'SiliconFlow', 'https://cloud.siliconflow.cn'],
    [/bigmodel|zhipu|智谱|glm/, '智谱 GLM', 'https://www.bigmodel.cn'],
    [/minimax/, 'MiniMax', 'https://platform.minimaxi.com'],
    [/moonshot|kimi/, 'Kimi', 'https://platform.moonshot.cn'],
  ]
  for (const [re, name, url] of BILLING) {
    if (re.test(where)) {
      return `\n提示：${name} 账户余额不足，充值后重试：${url} —— 或用 /model 临时切换到其他 provider。`
    }
  }
  return '\n提示：当前 provider 账户余额不足，请充值后重试，或用 /model 切换到其他 provider。'
}

/**
 * 按 HTTP 状态码追加可操作提示：401/403 指明 key 的环境变量名（用户知道
 * 去哪检查），404 指向 `rivet provider models`（核对模型 id 是否拼错/已改名）。
 */
function statusHint(status: number, provider?: ApiErrorProviderContext): string {
  if (status === 401 || status === 403) {
    const envPart = provider?.apiKeyEnv
      ? `——请检查环境变量 ${provider.apiKeyEnv} 是否已导出且未过期`
      : '——请检查 API key 是否正确'
    return `\n提示：鉴权失败（HTTP ${status}）${envPart}，或用 /connect 重新配置。`
  }
  if (status === 404) {
    return '\n提示：404 通常是模型 id 拼错或端点路径不对——运行 `rivet provider models <provider>` 核对端点实际提供的模型 id。'
  }
  return ''
}

export function parseOpenAIError(status: number, body: string, provider?: ApiErrorProviderContext): string {
  try {
    const parsed = JSON.parse(body)
    const code = parsed.error?.code ?? parsed.error?.type ?? `HTTP ${status}`
    const message = parsed.error?.message ?? body
    return `OpenAI API error (${code}): ${message}${apiErrorHint(String(code), String(message), provider)}${statusHint(status, provider)}`
  } catch {
    return `OpenAI API error (HTTP ${status}): ${body}${statusHint(status, provider)}`
  }
}
