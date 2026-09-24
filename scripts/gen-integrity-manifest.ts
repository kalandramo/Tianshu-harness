#!/usr/bin/env tsx
/**
 * 生成并签名运行时完整性清单（`rivet-runtime/integrity.json`）。
 *
 * 在**桌面端发布管线**里跑，位置必须在混淆之后、Tauri 打包之前：
 *
 * ```bash
 * # desktop/ 目录下（tauri.conf.json beforeBuildCommand 已接入）
 * npx tsx ../scripts/gen-integrity-manifest.ts --dir ../dist --mode code
 * # 或手动
 * RIVET_RELEASE_KEY_PKCS8=<base64> npx tsx scripts/gen-integrity-manifest.ts --dir dist
 * ```
 *
 * 私钥来源优先级：`--key` > `--key-file` > 环境变量 `RIVET_RELEASE_KEY_PKCS8`。
 * **没有私钥就报错退出**——绝不写未签名清单：壳会判 `bad_signature`，等于把
 * 全体付费用户冻结成 Basic（宁可发布失败，不要静默降级）。
 *
 * 私钥应与授权服务器 `SIGNING_KEY_PKCS8` 同源（同一把 Ed25519 密钥对），
 * 这样壳里那一份 `PUBLIC_KEY_B64` 同时能验许可证 token 与本清单；两处签名
 * 消息带不同域前缀（`rivet-integrity-v1:`）防跨协议混淆。
 */
import { createPrivateKey, createPublicKey, sign as cryptoSign } from 'node:crypto'
import { readFileSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'

import {
  INTEGRITY_MANIFEST_FILENAME,
  INTEGRITY_MANIFEST_VERSION,
  assertAsciiPaths,
  computeBundle,
  integritySigningMessage,
  verifyIntegrityManifest,
  type IntegrityManifest,
  type IntegrityMode,
} from '../src/config/runtime-integrity.js'
import { LICENSE_PRODUCT } from '../src/config/license-keys.js'

interface Args {
  dir: string
  mode: IntegrityMode
  out?: string
  key?: string
  keyFile?: string
  printPublicKey: boolean
  createdAt?: number
}

function parseArgs(argv: string[]): Args {
  const args: Args = { dir: 'dist', mode: 'code', printPublicKey: false }
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i]
    const next = (): string => {
      const v = argv[++i]
      if (v === undefined) throw new Error(`缺少参数值：${a}`)
      return v
    }
    switch (a) {
      case '--dir':
        args.dir = next()
        break
      case '--mode': {
        const m = next()
        if (m !== 'code' && m !== 'full') throw new Error(`--mode 只能是 code|full，收到 ${m}`)
        args.mode = m
        break
      }
      case '--out':
        args.out = next()
        break
      case '--key':
        args.key = next()
        break
      case '--key-file':
        args.keyFile = next()
        break
      case '--print-public-key':
        args.printPublicKey = true
        break
      case '--created-at':
        args.createdAt = Number(next())
        break
      case '--help':
      case '-h':
        console.log(
          [
            '用法: tsx scripts/gen-integrity-manifest.ts [--dir dist] [--mode code|full] [--out path]',
            '                                     [--key <pkcs8-base64> | --key-file <path>] [--print-public-key]',
            '',
            '私钥也可经环境变量 RIVET_RELEASE_KEY_PKCS8 提供。',
          ].join('\n')
        )
        process.exit(0)
        break
      default:
        throw new Error(`未知参数：${a}`)
    }
  }
  return args
}

function loadPrivateKeyPkcs8(args: Args): { der: Buffer; source: string } {
  if (args.key) return { der: Buffer.from(args.key.trim(), 'base64'), source: '--key' }
  if (args.keyFile) {
    return { der: Buffer.from(readFileSync(args.keyFile, 'utf8').trim(), 'base64'), source: `--key-file ${args.keyFile}` }
  }
  const env = process.env.RIVET_RELEASE_KEY_PKCS8
  if (env && env.trim()) return { der: Buffer.from(env.trim(), 'base64'), source: 'RIVET_RELEASE_KEY_PKCS8' }
  throw new Error(
    '缺少签名私钥：请设置 RIVET_RELEASE_KEY_PKCS8（或 --key / --key-file）。' +
      '拒绝生成未签名清单——壳会判 bad_signature 并让所有用户降级 Basic。'
  )
}

function rawPublicKeyB64FromPrivate(key: ReturnType<typeof createPrivateKey>): string {
  const spki = createPublicKey(key).export({ format: 'der', type: 'spki' }) as Buffer
  // Ed25519 SPKI 固定 12 字节前缀，末 32 字节是 raw key。
  const raw = spki.subarray(spki.length - 32)
  return raw.toString('base64')
}

const args = parseArgs(process.argv.slice(2))
const dir = resolve(args.dir)

const { der, source } = loadPrivateKeyPkcs8(args)
let privateKey: ReturnType<typeof createPrivateKey>
try {
  privateKey = createPrivateKey({ key: der, format: 'der', type: 'pkcs8' })
} catch (err) {
  throw new Error(`私钥解析失败（来源 ${source}，期望 PKCS#8 DER 的 base64）：${(err as Error).message}`)
}

const { files, bundleHash, fileCount } = computeBundle(dir, args.mode)
assertAsciiPaths(files)
if (fileCount === 0) throw new Error(`目录里没有可校验文件（dir=${dir}, mode=${args.mode}）——拒绝生成空清单`)

const publicKeyB64 = rawPublicKeyB64FromPrivate(privateKey)
if (args.printPublicKey) {
  console.log(`PUBLIC KEY (raw 32 bytes, base64): ${publicKeyB64}`)
  console.log('对照 src/config/license-keys.ts 的 LICENSE_PUBLIC_KEY_B64 与 activation.rs 的 PUBLIC_KEY_B64，三者必须一致。')
}

const base = {
  v: INTEGRITY_MANIFEST_VERSION,
  product: LICENSE_PRODUCT,
  mode: args.mode,
  createdAt: args.createdAt ?? Date.now(),
  bundleHash,
  fileCount,
  files,
}
const message = Buffer.from(integritySigningMessage(base), 'utf8')
const sig = cryptoSign(null, message, privateKey).toString('base64url')

const manifest: IntegrityManifest = { ...base, sig }
const outPath = resolve(args.out ?? join(dir, INTEGRITY_MANIFEST_FILENAME))
writeFileSync(outPath, `${JSON.stringify(manifest, null, 2)}\n`)

// 写完立刻用「验签 → 重算摘要」的完整路径自检：发布管线里失败要当场炸掉，
// 而不是等用户装上后发现全员 Basic。
const check = verifyIntegrityManifest(dir, { mode: args.mode, publicKeyB64 })
if (!check.ok) {
  throw new Error(`清单自检失败：${check.reason}（expected=${check.expectedHash} actual=${check.actualHash}）`)
}

console.log(`[integrity] ${outPath}`)
console.log(`  mode=${args.mode} files=${fileCount} bundleHash=${bundleHash}`)
console.log(`  签名私钥来源：${source}；公钥指纹：${publicKeyB64}`)
console.log('  自检通过（验签 + 重算摘要一致）。')