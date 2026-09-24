/**
 * 许可证 / 运行时完整性清单的 Ed25519 验签公钥与产品标识（node 侧单一来源）。
 *
 * ⚠️ 三处必须一致，漂移守卫测试见 `__tests__/license-keys-drift.test.ts`：
 *   1. 本文件（sidecar / CLI 侧验签）
 *   2. `desktop/src-tauri/src/activation.rs` 的 `PUBLIC_KEY_B64`（shell 侧验签）
 *   3. `license-server` 的 `npm run genkeys` 输出（签发侧私钥对应公钥）
 *
 * 这里只放公钥。私钥只应存在于两处：授权服务器（Cloudflare secret
 * `SIGNING_KEY_PKCS8`）与离线发布机（`RIVET_RELEASE_KEY_PKCS8`，用于签
 * `integrity.json`）。任何情况下都不要把私钥写进仓库或随包分发。
 *
 * 轮换流程：`license-server/scripts/genkeys.mjs` 生成新对 → 更新本文件 +
 * `activation.rs` + 服务器 secret → 发布新版本客户端。旧 token 在过渡期内
 * 仍由旧公钥可验（如需并行，可把本常量升格为数组，当前单密钥保持简单）。
 */
export const LICENSE_PUBLIC_KEY_B64 = '6uki4r+yY9GSb7ynO//P4+TEJdz4QeXMISu/UY5lolE='

/** 产品标识 —— token payload 的 `product` 字段必须等于此值。 */
export const LICENSE_PRODUCT = 'tianshu-desktop'

/**
 * 运行时完整性清单签名消息的域分隔前缀（防止跨协议签名混淆：
 * 同一把私钥签 license token 与签 integrity manifest，各有独立前缀）。
 */
export const INTEGRITY_SIGNING_DOMAIN = 'rivet-integrity-v1:'
