import { mkdirSync, readFileSync, writeFileSync, renameSync, unlinkSync } from 'node:fs'
import { join } from 'node:path'

import { createSecretCipher, decodeSecret, encodeSecret, type SecretCipher } from './secure-store.js'

export interface TokenData {
  accessToken: string
  refreshToken?: string
  expiresAt: number
  accountId?: string
  /**
   * 账号星籍缓存（仅 `account` store 使用；provider store 恒缺席）。
   *
   * 放在凭据文件里而不是单独文件，是因为它与登录态**同生命周期**：登出
   * `clear()` 一并消失，不会留下孤儿身份（星籍是账号的属性，账号没了它没有意义）。
   * 可选字段 —— `load()` 是 `JSON.parse` 无形状校验，旧文件天然读得进来。
   *
   * ⚠️ `save()` 是全量写：任何"只写 identity"的调用都会把 accessToken 抹掉。
   * 写入一律走 `saveAccountIdentity()`，不要手搓。
   */
  identity?: {
    stellarId: string
    primaryDomain: string
    title: string
    /** 取到时刻（ms）。超过 TTL 视为陈旧，由调用方决定是否后台刷新。 */
    fetchedAt: number
  }
}

/**
 * 凭据落盘存储：`<baseDir>/<provider>.json`。
 *
 * 自 v3.21.1 起内容为 **AES-256-GCM 信封**（数据密钥托管在 OS 密钥库或本机
 * 密钥文件，见 `secure-store.ts`），不再是明文 JSON —— 旧明文文件仍可读取，
 * 下次 `save()` 自动升级。`mode: 0o600` 保留作纵深防御（POSIX 生效；Windows
 * 上不生效，由加密信封兜底）。
 *
 * 为什么加密：refresh token 是长期凭据，而 `~/.rivet` 常落在会被云同步/备份/
 * 其它进程读到的地方；拿到文件 ≈ 拿到账号。详见
 * docs/security/pro-hardening.md 向量⑥。
 */
export class TokenStore {
  private filePath: string
  private cipher: SecretCipher

  constructor(private baseDir: string, provider: string, cipher?: SecretCipher) {
    this.filePath = join(baseDir, `${provider}.json`)
    this.cipher = cipher ?? createSecretCipher(baseDir)
  }

  load(): TokenData | null {
    try {
      const raw = readFileSync(this.filePath, 'utf-8')
      const plain = decodeSecret(this.cipher, raw)
      if (plain === null) return null
      return JSON.parse(plain) as TokenData
    } catch {
      return null
    }
  }

  save(data: TokenData): void {
    mkdirSync(this.baseDir, { recursive: true })
    const tmpPath = `${this.filePath}.tmp`
    writeFileSync(tmpPath, encodeSecret(this.cipher, JSON.stringify(data, null, 2)), { mode: 0o600 })
    renameSync(tmpPath, this.filePath)
  }

  clear(): void {
    try { unlinkSync(this.filePath) } catch {}
  }
}
