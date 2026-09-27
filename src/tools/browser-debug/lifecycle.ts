/**
 * browser-debug/lifecycle — 会话死亡判定、驱逐重建（一次重试）与定位失败提示。
 *
 * 从 tool.ts 沿接缝拆出（行数棘轮）。设计见
 * docs/analysis/2026-09-25-browser_debug-归因复核与优化设计.md W2/W4：
 *   - 死亡签名只认「目标页/连接已死」，不认模型脚本异常或普通 Timeout；
 *   - 重建复用旧会话的 mode/headless/connectUrl/userDataDir 并继承 LogCapture；
 *   - 只重试一次，避免新错误引爆重试环。
 */

import type { FailureClass } from '../../agent/failure-classifier.js'
import { PLAYWRIGHT_INSTALL_HINT } from '../net/playwright-driver.js'
import { enumerateCandidates } from './ai-primitives.js'
import { formatCandidates, parseLocatorQuery, resolveCandidates } from './locator.js'
import {
  evictSession,
  getOrCreateSession,
  getSession,
  type BrowserDebugSession,
} from './session.js'
import type { BrowserDebugDriverFactory } from './driver.js'

/** 死亡错误签名——只认目标页/连接已死，不认 Failed to fetch 或普通 Timeout。 */
export function isSessionDeathError(err: unknown): boolean {
  const msg = err instanceof Error ? err.message : String(err)
  return /Target page, context or browser has been closed|browser has been disconnected|Browser has been closed/i.test(msg)
}

/** 把异常映射成结构化失败分类，供 trace/免疫/收敛消费。 */
export function errorKindFor(err: unknown): FailureClass | undefined {
  const msg = err instanceof Error ? err.message : String(err)
  if (msg.includes(PLAYWRIGHT_INSTALL_HINT)) return 'env_missing'
  if (/Timeout \d+ms exceeded/i.test(msg)) return 'timeout'
  return undefined
}

export interface SessionLifecycleDeps {
  driverFactory?: BrowserDebugDriverFactory
  profileDirFor: (sessionKey: string, sharedProfile: boolean) => string
}

export interface SessionLifecycle {
  /** 取会话；死会话按旧启动参数重建（并用 viewport/log 恢复现场）。 */
  ensureSession(
    sessionKey: string,
    headless: boolean,
    connectUrl?: string,
    viewport?: { width: number; height: number },
    sharedProfile?: boolean,
  ): Promise<BrowserDebugSession>
  /** 调用前已死的会话：驱逐并按旧参数重建；返回 null 表示当前没有会话。 */
  ensureAlive(sessionKey: string, sharedProfile: boolean): Promise<BrowserDebugSession | null>
  /** 驱逐 + 复用旧启动参数重建（withSessionRecovery 与 execute 前置重建共用）。 */
  rebuildSession(sessionKey: string, old: BrowserDebugSession, sharedProfile: boolean): Promise<BrowserDebugSession>
  /** 死亡错误 → 重建 → 只重跑一次。 */
  withSessionRecovery<T>(
    sessionKey: string,
    sharedProfile: boolean,
    initial: BrowserDebugSession,
    fn: (session: BrowserDebugSession) => Promise<T>,
  ): Promise<T>
}

export function createSessionLifecycle(deps: SessionLifecycleDeps): SessionLifecycle {
  const { driverFactory, profileDirFor } = deps

  async function rebuildSession(
    sessionKey: string,
    old: BrowserDebugSession,
    sharedProfile: boolean,
  ): Promise<BrowserDebugSession> {
    const retired = await evictSession(sessionKey, old)
    return await getOrCreateSession({
      sessionKey,
      headless: old.headless,
      userDataDir: old.userDataDir ?? profileDirFor(sessionKey, sharedProfile),
      connectUrl: old.connectUrl,
      driverFactory,
      viewport: old.driver.viewportSize() ?? undefined,
      log: old.log,
      // W2.1：旧会话的帧订阅集合交给新会话接管（面板无需重连）。
      handoffFrom: retired ?? undefined,
    })
  }

  async function ensureSession(
    sessionKey: string,
    headless: boolean,
    connectUrl?: string,
    viewport?: { width: number; height: number },
    sharedProfile = false,
  ): Promise<BrowserDebugSession> {
    const existing = getSession(sessionKey)
    const dead = existing !== null && !existing.isAlive()
    // 死会话重建：复用旧会话的启动参数（否则会把 connect 会话重建成 launch、
    // 把 headless 重建成有头），并继承日志证据。
    const session = await getOrCreateSession({
      sessionKey,
      headless: dead ? existing.headless : headless,
      userDataDir: dead && existing.userDataDir
        ? existing.userDataDir
        : profileDirFor(sessionKey, sharedProfile),
      connectUrl: connectUrl ?? (dead ? existing.connectUrl : undefined),
      driverFactory,
      viewport: viewport ?? (dead ? existing.driver.viewportSize() ?? undefined : undefined),
      log: dead ? existing.log : undefined,
    })
    // Passing the viewport to the factory sizes a fresh launch without a resize
    // flash; applying it again covers the case where the session already
    // existed, so `open` with a size always lands on that size.
    if (viewport) await session.driver.setViewport(viewport.width, viewport.height).catch(() => {})
    return session
  }

  async function ensureAlive(sessionKey: string, sharedProfile: boolean): Promise<BrowserDebugSession | null> {
    const existing = getSession(sessionKey)
    if (!existing) return null
    if (existing.isAlive()) return existing
    return await rebuildSession(sessionKey, existing, sharedProfile)
  }

  async function withSessionRecovery<T>(
    sessionKey: string,
    sharedProfile: boolean,
    initial: BrowserDebugSession,
    fn: (session: BrowserDebugSession) => Promise<T>,
  ): Promise<T> {
    try {
      return await fn(initial)
    } catch (err) {
      if (!isSessionDeathError(err)) throw err
      const rebuilt = await rebuildSession(sessionKey, initial, sharedProfile)
      return await fn(rebuilt)
    }
  }

  return { ensureSession, ensureAlive, rebuildSession, withSessionRecovery }
}

/**
 * 定位类失败包一层 timeout 提示；死亡错误原样冒泡给 withSessionRecovery。
 * 从 tool.ts 拆出（行数棘轮），调用方只需 withLocatorHint(session, selector, fn)。
 */
export async function withLocatorHint<T>(
  session: BrowserDebugSession,
  selector: string,
  fn: () => Promise<T>,
): Promise<T> {
  try {
    return await fn()
  } catch (err) {
    throw await appendLocatorHint(session, err, selector)
  }
}

/**
 * timeout 类定位失败的可操作性：附当前页面可交互元素候选，让模型一次拿到
 * 「页面现在有什么可点」而不是只看 Playwright 原文。只对 timeout 生效；
 * 死亡错误原样抛出（不能被提示包裹掉恢复签名）。
 */
export async function appendLocatorHint(
  session: BrowserDebugSession,
  err: unknown,
  selector: string,
): Promise<Error> {
  const message = err instanceof Error ? err.message : String(err)
  if (!/Timeout \d+ms exceeded/i.test(message)) return err instanceof Error ? err : new Error(message)
  try {
    const candidates = await enumerateCandidates(session.driver)
    if (candidates.length === 0) return new Error(message)
    const query = parseLocatorQuery(selector)
    const resolved = resolveCandidates(candidates, query)
    // 宽松匹配一个都没过线时，退化为"页面上有什么"清单——比只说 timeout 有用。
    const list = resolved.kind === 'match'
      ? [{ candidate: resolved.candidate, score: resolved.score }]
      : resolved.candidates.length > 0
        ? resolved.candidates.slice(0, 5)
        : candidates.slice(0, 5).map((candidate) => ({ candidate, score: 0 }))
    return new Error(
      `${message}\n当前页面可交互元素（供修正 selector）：\n${formatCandidates(list)}\n`
      + '也可改用 act {instruction:"..."} 让工具按自然语言定位。',
    )
  } catch {
    return new Error(message)
  }
}
