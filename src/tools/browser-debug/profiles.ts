/**
 * browser-debug/profiles — 多会话 userDataDir 隔离与陈旧 profile 清理。
 *
 * 历史问题（2026-09-25 归因复核 §1.4）：所有 sessionKey 共用
 * `~/.rivet/browser-debug-profile`，并行会话启动时第二个 Chrome 会把 open
 * 转发给既有实例后退出，报 launchPersistentContext: Target page...closed。
 * 现在默认按 sessionKey 隔离；共享是显式选项（shared_profile / env）。
 */

import { dirname, join } from 'node:path'
import { readdirSync, rmSync, statSync } from 'node:fs'
import { rivetHome } from '../../config/paths.js'
import { DEFAULT_SESSION_KEY, listSessionKeys } from './session.js'

/** 历史共享 profile（跨会话登录态）——只给 __default__ 或显式共享者用。 */
export function legacyUserDataDir(): string {
  return join(rivetHome(), 'browser-debug-profile')
}

/** sessionKey → 文件系统安全的目录名（含中文/斜杠的 sessionId 也要能落盘）。 */
export function safeProfileName(sessionKey: string): string {
  const safe = sessionKey.replace(/[^a-zA-Z0-9._-]/g, '_').slice(0, 64)
  return safe || 'session'
}

export function defaultUserDataDir(sessionKey: string, shared = false): string {
  if (shared || sessionKey === DEFAULT_SESSION_KEY) return legacyUserDataDir()
  return join(rivetHome(), 'browser-debug-profiles', safeProfileName(sessionKey))
}

/** 每个会话一份 profile 会占磁盘（几十~几百 MB）——保留最近 20 个、删 7 天未用的。
 *  正在使用的目录删除会失败（Chrome 持有文件），跳过即可；活跃 sessionKey 也显式跳过。 */
const PROFILE_MAX_COUNT = 20
const PROFILE_MAX_AGE_MS = 7 * 24 * 60 * 60 * 1000

export function pruneStaleProfiles(root: string): void {
  try {
    const active = new Set(listSessionKeys().map((key) => safeProfileName(key)))
    const entries = readdirSync(root, { withFileTypes: true })
      .filter((e) => e.isDirectory() && !active.has(e.name))
      .map((e) => {
        const path = join(root, e.name)
        return { path, mtime: statSync(path).mtimeMs }
      })
      .sort((a, b) => b.mtime - a.mtime)
    for (let i = 0; i < entries.length; i++) {
      const entry = entries[i]!
      if (i < PROFILE_MAX_COUNT && Date.now() - entry.mtime <= PROFILE_MAX_AGE_MS) continue
      try { rmSync(entry.path, { recursive: true, force: true }) } catch { /* in use — skip */ }
    }
  } catch {
    /* best-effort：清理失败绝不影响 open */
  }
}

/** profile 目录解析器：测试注入优先；否则按 sessionKey 隔离（shared 时回退历史共享目录）。 */
export function createProfileDirResolver(override?: () => string): (sessionKey: string, sharedProfile: boolean) => string {
  return (sessionKey, sharedProfile) => {
    if (override) return override()
    const dir = defaultUserDataDir(sessionKey, sharedProfile)
    if (!sharedProfile && sessionKey !== DEFAULT_SESSION_KEY) pruneStaleProfiles(dirname(dir))
    return dir
  }
}
