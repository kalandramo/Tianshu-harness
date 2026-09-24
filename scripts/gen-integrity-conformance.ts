#!/usr/bin/env tsx
/**
 * 维护工具：生成跨语言一致性 fixture（`desktop/integrity-conformance/fixtures/conformance.json`）。
 *
 * 只在**摘要/验签格式合法变更**时手动重跑，产物入库；日常 CI 直接消费 fixture：
 *   - JS 侧 `src/config/__tests__/runtime-integrity.test.ts`
 *   - Rust 侧 `desktop/integrity-conformance/tests/conformance.rs`
 * 两边都把 fixture 里的文件树在临时目录里铺开、各自算摘要、各自验签，然后与
 * fixture 里写死的期望值比对。任何一边改了枚举规则/摘要格式/签名消息，两边
 * 都会红——这是防止「改了一边忘了另一边」的唯一可靠手段。
 *
 * 用法：npx tsx scripts/gen-integrity-conformance.ts
 */
import { createPrivateKey, createPublicKey, sign as cryptoSign } from 'node:crypto'
import { mkdtempSync, mkdirSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import {
  INTEGRITY_MANIFEST_VERSION,
  computeBundle,
  integritySigningMessage,
  verifyIntegrityManifest,
  type IntegrityManifest,
  type IntegrityMode,
} from '../src/config/runtime-integrity.js'
import { LICENSE_PRODUCT } from '../src/config/license-keys.js'

/** ⚠️ 仅用于 fixture 的测试密钥，绝不用于生产签名（生产私钥在离线发布机 / Cloudflare secret）。 */
const TEST_PRIVATE_KEY_PKCS8_B64 = 'MC4CAQAwBQYDK2VwBCIEILAasN057vYDNimrWTJw/lYPeOOJY7Wt9d3g40fGMV/o'
const TEST_PUBLIC_KEY_B64 = 'XqroVueTnIF32xlQLj0QMW5bUiApHMU0GiyZa26VeXs='

const MODE: IntegrityMode = 'code'
const CREATED_AT = 1730000000000

/** 固定文件树：既覆盖参与摘要的文件，也覆盖每一条排除规则。 */
const FILES: Record<string, string> = {
  'main.js': 'console.log("rivet main")\n',
  'cli/entry.js': 'export const entry = 1\n',
  'chunk-ABCD1234.js': 'export const proLicense = () => false\n',
  'agent/loop.js': 'export function loop() {}\n',
  'package.json': '{"type":"module"}\n', // 排除：运行时自愈会改写
  'node_modules/pkg/index.js': 'module.exports = 1\n', // 排除（code 模式）
  'tmp/scratch.js': 'x\n', // 排除：tmp 目录
  'logs/run.log': 'x\n', // 排除：logs 目录 / .log
  '.DS_Store': 'junk\n', // 排除：编辑器垃圾
  'a/.cache/c.js': 'x\n', // 排除：.cache 段
  'debug.log': 'x\n', // 排除：.log 后缀
  'note.tmp': 'x\n', // 排除：.tmp 后缀
}

function rawPublicKeyB64FromPrivate(key: ReturnType<typeof createPrivateKey>): string {
  const spki = createPublicKey(key).export({ format: 'der', type: 'spki' }) as Buffer
  return spki.subarray(spki.length - 32).toString('base64')
}

const privateKey = createPrivateKey({
  key: Buffer.from(TEST_PRIVATE_KEY_PKCS8_B64, 'base64'),
  format: 'der',
  type: 'pkcs8',
})
const derivedPublic = rawPublicKeyB64FromPrivate(privateKey)
if (derivedPublic !== TEST_PUBLIC_KEY_B64) {
  throw new Error(`fixture 测试密钥与公钥不匹配：derived=${derivedPublic}`)
}

// 1) 在临时目录铺开文件树，走真实扫描路径（不能用内存 map 直接算，
//    否则测不出枚举规则）。
const stage = mkdtempSync(join(tmpdir(), 'rivet-integrity-fixture-'))
for (const [rel, content] of Object.entries(FILES)) {
  const abs = join(stage, rel)
  mkdirSync(dirname(abs), { recursive: true })
  writeFileSync(abs, content)
}

const { files, bundleHash, fileCount } = computeBundle(stage, MODE)

// 2) 用测试私钥签一份清单（与发布管线完全相同的消息格式）。
const base = {
  v: INTEGRITY_MANIFEST_VERSION,
  product: LICENSE_PRODUCT,
  mode: MODE,
  createdAt: CREATED_AT,
  bundleHash,
  fileCount,
  files,
}
const sig = cryptoSign(null, Buffer.from(integritySigningMessage(base), 'utf8'), privateKey).toString('base64url')
const manifest: IntegrityManifest = { ...base, sig }

// 3) 自检：验签 + 重算摘要必须通过，且必须能指出被改动的文件。
const ok = verifyIntegrityManifest(stage, { mode: MODE, publicKeyB64: TEST_PUBLIC_KEY_B64, manifest })
if (!ok.ok) throw new Error(`fixture 清单自检失败：${ok.reason}`)
writeFileSync(join(stage, 'main.js'), 'console.log("tampered")\n')
const tampered = verifyIntegrityManifest(stage, { mode: MODE, publicKeyB64: TEST_PUBLIC_KEY_B64, manifest })
if (tampered.ok || tampered.reason !== 'digest_mismatch' || !tampered.changed?.includes('~main.js')) {
  throw new Error(`fixture 篡改检测自检失败：${tampered.reason} ${JSON.stringify(tampered.changed)}`)
}

const fixture = {
  $comment:
    '自动生成，勿手改：npx tsx scripts/gen-integrity-conformance.ts。JS 与 Rust 两侧都必须复现 expectedBundleHash / expectedFileCount / manifest 验签结论。',
  $warning:
    'testPrivateKeyPkcs8B64 是**公开的测试密钥**，只为让两侧测试独立复现签名/验签，绝不用于生产。生产私钥只存在于 Cloudflare secret（SIGNING_KEY_PKCS8）与离线发布机（RIVET_RELEASE_KEY_PKCS8）。若发现它出现在 src/config/license-keys.ts 或 activation.rs 的 PUBLIC_KEY_B64，属配置事故。',
  mode: MODE,
  testPublicKeyB64: TEST_PUBLIC_KEY_B64,
  testPrivateKeyPkcs8B64: TEST_PRIVATE_KEY_PKCS8_B64,
  files: FILES,
  expectedFileCount: fileCount,
  expectedBundleHash: bundleHash,
  expectedIncludedPaths: Object.keys(files).sort(),
  expectedTamperedPath: '~main.js',
  manifest,
}

const here = dirname(fileURLToPath(import.meta.url))
const outPath = join(here, '..', 'desktop', 'integrity-conformance', 'fixtures', 'conformance.json')
writeFileSync(outPath, `${JSON.stringify(fixture, null, 2)}\n`)

console.log(`[conformance] ${outPath}`)
console.log(`  mode=${MODE} fileCount=${fileCount} bundleHash=${bundleHash}`)
console.log(`  included: ${Object.keys(files).sort().join(', ')}`)
console.log('  自检通过：正常树验签通过；篡改 main.js 后判 digest_mismatch 且定位到 ~main.js。')
