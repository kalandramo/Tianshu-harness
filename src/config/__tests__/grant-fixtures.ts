/**
 * 测试用 grant 签发器（Ed25519）。
 *
 * 生产签发在 `license-server`（Cloudflare Worker，私钥只在服务端）；这里用
 * Node 内置 crypto 复刻**同一契约**（`base64url(JSON).base64url(sig)`，签名
 * 消息是第一段的 ASCII 字节），所以测试跑的是真实验签路径，不是 mock。
 */
import { generateKeyPairSync, sign as cryptoSign, type KeyObject } from 'node:crypto'

import { LICENSE_PRODUCT } from '../license-keys.js'

/** 从 SPKI DER 里取出末 32 字节 raw 公钥（与 activation.rs / license-server 同格式）。 */
export function rawPublicKeyB64(publicKey: KeyObject): string {
  const der = publicKey.export({ format: 'der', type: 'spki' }) as Buffer
  return der.subarray(der.length - 32).toString('base64')
}

export function makeTestSigner(): { privateKey: KeyObject; publicKeyB64: string } {
  const { privateKey, publicKey } = generateKeyPairSync('ed25519')
  return { privateKey, publicKeyB64: rawPublicKeyB64(publicKey) }
}

export function buildGrantPayload(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  const now = Date.now()
  return {
    product: LICENSE_PRODUCT,
    deviceId: 'dev-test-0001',
    tier: 'pro',
    iat: now,
    exp: now + 86_400_000,
    lic: null,
    ...overrides,
  }
}

export function signGrant(payload: Record<string, unknown>, privateKey: KeyObject): string {
  const payloadB64 = Buffer.from(JSON.stringify(payload), 'utf8').toString('base64url')
  const sig = cryptoSign(null, Buffer.from(payloadB64, 'ascii'), privateKey)
  return `${payloadB64}.${sig.toString('base64url')}`
}

/** 一步拿到「有效 grant + 对应公钥」。 */
export function makeValidGrant(
  overrides: Record<string, unknown> = {}
): { token: string; publicKeyB64: string } {
  const { privateKey, publicKeyB64 } = makeTestSigner()
  return { token: signGrant(buildGrantPayload(overrides), privateKey), publicKeyB64 }
}
