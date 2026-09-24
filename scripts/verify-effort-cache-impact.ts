#!/usr/bin/env tsx
/**
 * 冰鉴 effort 缓存影响验证脚本
 *
 * 回答两个问题，都用受控实验（唯一自变量 reasoning_effort，其余全固定）：
 *
 *   1. --inject  该模型的档位是否改写输入前缀？改写多少 token？
 *      极小 messages 下逐档位读 usage.prompt_tokens，档位间差值即服务端注入量。
 *      注入量相同的档位落入同一「注入组」，互相之间可命中缓存。
 *
 *   2. --cache   跨组换档是否导致前缀缓存全量 miss？
 *      恒定 messages（逐字节相同）下按序列换档，读每轮的 prompt_cache_hit_tokens。
 *
 * 背景结论（2026-09-19 实测，详见 docs/analysis/2026-09-19-effort-cache-controlled-experiment.md）：
 *   deepseek-v4-pro   : low=0 / medium=high=不发字段=+79 / max=+92 → 跨组换档 0% 命中
 *   deepseek-v4-flash : 各档 +0                                 → 换档零代价（但 effort 生效 +145%）
 *   deepseek-flash    : 各档 +0，且 effort 几乎不生效（+8%）
 * 即「碎不碎」取决于该模型该档位是否注入前缀，与 effort 是否生效是两个独立维度。
 *
 * 用法：
 *   npx tsx scripts/verify-effort-cache-impact.ts --inject --provider deepseek --models deepseek-v4-pro,deepseek-v4-flash
 *   npx tsx scripts/verify-effort-cache-impact.ts --cache  --provider deepseek --model deepseek-v4-pro
 *   npx tsx scripts/verify-effort-cache-impact.ts --inject --provider opencode-go
 *
 * 凭据解析（按序）：
 *   环境变量 <PROVIDER>_API_KEY → 该 provider 在 ~/.rivet/config.json 里声明的 apiKeyEnv
 *   → provider.keyRef（secrets.json）→ provider.apiKey
 *   baseUrl 取 config 里的 provider.baseUrl，可用 <PROVIDER>_BASE_URL 覆盖。
 *   未传 --models 时取该 provider 在 config 里声明的前两个模型 id。
 */

import { readFileSync } from 'node:fs'
import { homedir } from 'node:os'
import { join } from 'node:path'
import { setTimeout as sleep } from 'node:timers/promises'
import { readSecret } from '../src/config/secrets-store.js'
import { stableStringify } from '../src/api/stable-json.js'
import { resolveProviderWire } from '../src/api/provider-catalog.js'
import { callerIdentityHeaders, PROCESS_SESSION_ID } from '../src/api/caller-identity.js'

type Effort = string | null

const EFFORTS: Effort[] = ['low', 'medium', 'high', 'max', null]

// ── 参数 ────────────────────────────────────────────────────────

function argOf(flag: string): string | undefined {
  const i = process.argv.indexOf(flag)
  return i >= 0 ? process.argv[i + 1] : undefined
}
const MODE_INJECT = process.argv.includes('--inject')
const MODE_CACHE = process.argv.includes('--cache')
const MODE_ALL = !MODE_INJECT && !MODE_CACHE
const PROVIDER = argOf('--provider') ?? 'deepseek'

// ── 凭据 ────────────────────────────────────────────────────────

interface Creds { apiKey: string; baseUrl: string }

function resolveCreds(providerName: string): Creds {
  const upper = providerName.toUpperCase().replace(/-/g, '_')
  let apiKey = process.env[`${upper}_API_KEY`]
  let baseUrl = process.env[`${upper}_BASE_URL`]
  let declaredModels: string[] = []
  try {
    const cfg = JSON.parse(readFileSync(join(homedir(), '.rivet', 'config.json'), 'utf-8')) as Record<string, any>
    const p = cfg?.['provider']?.['providers']?.[providerName] as Record<string, any> | undefined
    if (p) {
      // apiKeyEnv 优先级高于约定式 <NAME>_API_KEY：config 里显式声明的名字才是权威
      if (!apiKey && p['apiKeyEnv']) apiKey = process.env[String(p['apiKeyEnv'])]
      if (!apiKey && p['keyRef']) apiKey = readSecret(String(p['keyRef']))
      if (!apiKey && p['apiKey']) apiKey = String(p['apiKey'])
      if (!baseUrl && p['baseUrl']) baseUrl = String(p['baseUrl'])
      declaredModels = (p['models'] ?? []).map((m: Record<string, any>) => String(m['id'])).filter((id: string) => !id.includes('expires'))
    }
  } catch { /* 配置不可读时回退到纯环境变量路径 */ }
  if (!apiKey) {
    console.error(`❌ 未解析到 ${providerName} 的凭据。设置 ${upper}_API_KEY，或确认 ~/.rivet/config.json 里该 provider 有 keyRef/apiKeyEnv`)
    process.exit(1)
  }
  if (!baseUrl) {
    console.error(`❌ ${providerName} 没有 baseUrl（config 与环境变量都没有）`)
    process.exit(1)
  }
  ;(globalThis as Record<string, any>)['__declaredModels'] = declaredModels
  return { apiKey, baseUrl }
}

const { apiKey, baseUrl } = resolveCreds(PROVIDER)
// 身份头走生产同一条规则（catalog 条目 + host 规则），不自建 header 名映射：
// opencode.ai 这类站点缺 x-opencode-session 会直接 400 MissingSessionID。
const WIRE = resolveProviderWire(PROVIDER, baseUrl)
const AUTH: Record<string, string> = {
  'Content-Type': 'application/json',
  Authorization: `Bearer ${apiKey}`,
  ...callerIdentityHeaders(WIRE, PROCESS_SESSION_ID),
}

const DECLARED: string[] = (globalThis as Record<string, any>)['__declaredModels'] ?? []

const INJECT_MODELS = (argOf('--models') ?? DECLARED.slice(0, 2).join(',')).split(',').map(s => s.trim()).filter(Boolean)
const CACHE_MODEL = argOf('--model') ?? DECLARED[0] ?? ''

if (INJECT_MODELS.length === 0 && (MODE_INJECT || MODE_ALL)) {
  console.error(`❌ 无法确定要测的模型：config 里 ${PROVIDER} 没有 models 声明，请显式传 --models`)
  process.exit(1)
}

interface AskResult { status: number; json: Record<string, any>; err?: string; elapsed: number }

async function ask(body: Record<string, unknown>, timeoutMs = 600_000): Promise<AskResult> {
  const t0 = Date.now()
  try {
    const res = await fetch(`${baseUrl}/chat/completions`, {
      method: 'POST',
      headers: AUTH,
      body: stableStringify(body),
      signal: AbortSignal.timeout(timeoutMs),
    })
    const txt = await res.text()
    const elapsed = Date.now() - t0
    if (!res.ok) return { status: res.status, json: {}, err: txt.slice(0, 200), elapsed }
    return { status: 200, json: JSON.parse(txt) as Record<string, any>, elapsed }
  } catch (e) {
    return { status: 0, json: {}, err: (e as Error).message.slice(0, 200), elapsed: Date.now() - t0 }
  }
}

/** usage 字段跨 provider 有差异：cache 命中读 prompt_cache_hit_tokens，
 *  部分站点用 prompt_tokens_details.cached_tokens（OpenAI 风格）或 anthropic 风格。 */
function readUsage(u: Record<string, any>): { input: number; hit: number; miss: number; reasoning: number } {
  const input = u['prompt_tokens'] ?? u['input_tokens'] ?? 0
  const hit = u['prompt_cache_hit_tokens'] ?? u['prompt_tokens_details']?.['cached_tokens'] ?? 0
  const miss = u['prompt_cache_miss_tokens']
  return {
    input,
    hit,
    miss: miss ?? Math.max(0, input - hit),
    reasoning: u['completion_tokens_details']?.['reasoning_tokens'] ?? u['usage']?.['reasoning_tokens'] ?? 0,
  }
}

// ── Step 1：档位 → 注入量 ───────────────────────────────────────

async function runInject(): Promise<void> {
  console.log(`=== ${PROVIDER} 档位 → input token（极小 messages，档位间差值 = 前缀注入量）===\n`)
  console.log('model                effort   status  input  reasoning     ms')
  for (const model of INJECT_MODELS) {
    const rows: Array<{ effort: Effort; input: number }> = []
    for (const effort of EFFORTS) {
      const body: Record<string, unknown> = {
        model,
        messages: [{ role: 'user', content: 'Reply with exactly: OK' }],
        max_tokens: 512, stream: false, thinking: { type: 'enabled' },
      }
      if (effort !== null) body['reasoning_effort'] = effort
      const r = await ask(body)
      const u = readUsage(r.json['usage'] ?? {})
      if (r.status === 200) rows.push({ effort, input: u.input })
      console.log(`${model.padEnd(20)} ${String(effort).padEnd(8)} ${String(r.status).padEnd(6)}  ${String(u.input).padEnd(5)}  ${String(u.reasoning).padEnd(10)}  ${r.elapsed} ${r.err ? r.err.slice(0, 70) : ''}`)
      await sleep(800)
    }
    if (rows.length === 0) { console.log(`  → ${model}: 全部档位无有效响应，无法判定\n`); continue }
    // 以 low 为基准（外部实验同口径：low 无注入）
    const base = rows.find(r => r.effort === 'low')?.input ?? rows[0]!.input
    console.log('  → ' + model + ' 注入量(low=基准): ' + rows.map(r => `${r.effort ?? 'none'}=${r.input - base >= 0 ? '+' : ''}${r.input - base}`).join('  '))
    const groups = new Map<number, string[]>()
    for (const r of rows) {
      const k = r.input - base
      groups.set(k, [...(groups.get(k) ?? []), r.effort ?? 'none'])
    }
    console.log('  → 注入组: ' + [...groups.entries()].sort((a, b) => a[0] - b[0]).map(([k, names]) => `{${names.join(',')}}@${k >= 0 ? '+' : ''}${k}`).join('  '))
    console.log(`  → ${groups.size > 1 ? '⚠️ 多组：跨组换档会全量 miss' : '✅ 单组：换档无缓存代价'}\n`)
  }
}

// ── Step 2：恒定 messages 下的换档缓存实验 ──────────────────────

const CACHE_SEQ: Effort[] = ['high', 'high', 'high', 'max', 'max', 'high', 'medium', 'medium', 'high']

async function runCache(): Promise<void> {
  if (!CACHE_MODEL) {
    console.error('❌ --cache 需要 --model（或该 provider 在 config 里声明了 models）')
    process.exit(1)
  }
  const messages = [
    { role: 'system', content: 'You are a terse assistant.\n\n# Reference\n' + 'The quick brown fox jumps over the lazy dog. '.repeat(400) },
    { role: 'user', content: 'Question: what breaks a prefix cache?' },
    { role: 'assistant', content: 'Answer: any change to the bytes of the prefix, including server-side injected instructions.' },
    { role: 'user', content: 'Reply with exactly: OK' },
  ]
  const messagesJson = stableStringify(messages)
  console.log(`=== ${PROVIDER}/${CACHE_MODEL} 恒定 messages（${messagesJson.length} chars）下的档位切换 ===\n`)
  console.log('seq  effort  status  input   hit     miss   hitRate     ms  verdict')
  let prev: Effort = CACHE_SEQ[0]!
  for (let i = 0; i < CACHE_SEQ.length; i++) {
    const effort = CACHE_SEQ[i]!
    const body: Record<string, unknown> = {
      model: CACHE_MODEL, messages: JSON.parse(messagesJson),
      max_tokens: 512, stream: false, thinking: { type: 'enabled' },
    }
    if (effort !== null) body['reasoning_effort'] = effort
    const r = await ask(body)
    const u = readUsage(r.json['usage'] ?? {})
    const rate = u.hit + u.miss > 0 ? +(u.hit / (u.hit + u.miss) * 100).toFixed(1) : -1
    const switched = i > 0 && effort !== prev
    const verdict = r.status !== 200
      ? `ERR ${r.err?.slice(0, 60) ?? ''}`
      : !switched ? '同档'
        : rate < 5 ? '★ 换档后全量 miss'
          : rate < 50 ? '部分碎'
            : '换档后仍命中'
    console.log(`${String(i + 1).padStart(3)}  ${String(effort).padEnd(6)}  ${String(r.status).padEnd(6)}  ${String(u.input).padEnd(5)}  ${String(u.hit).padEnd(6)}  ${String(u.miss).padEnd(6)}  ${String(rate).padEnd(8)}  ${String(r.elapsed).padEnd(6)}  ${verdict}`)
    prev = effort
    await sleep(1500)
  }
}

if (MODE_INJECT || MODE_ALL) await runInject()
if (MODE_CACHE || MODE_ALL) await runCache()
