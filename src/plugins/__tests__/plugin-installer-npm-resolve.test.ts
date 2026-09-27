/**
 * `resolveNpmCommand` 的判据契约：**能跑**，而不是**文件在**。
 *
 * ## 缺陷现场（2026-09-25）
 * 打包桌面端内置的 npm 垫片**存在但跑不起来**：
 * `/Applications/Tianshu.app/Contents/Resources/node-runtime/darwin-arm64/bin/npm`
 * require 的 `../lib/cli.js` 不存在（该 runtime 的 `lib/` 下只有 `node_modules`），
 * 一执行就是 `Error: Cannot find module '../lib/cli.js'`；而系统 npm（11.19.0）正常。
 *
 * 原实现只做 `existsSync`：命中内置垫片就返回它、**不再回落**系统 npm，于是插件安装
 * 整条链路失败——`plugin-installer.test.ts` 3 条与 `plugin-api.test.ts` 2 条用例红，
 * 且失败被包成 `npm install failed: <node 内部栈>`，从错误信息根本看不出真因是
 * 「选错了一个不能跑的 npm」。
 *
 * 这两条断言跨环境稳健：内置 npm 缺席（普通开发机）时它们自动失去前提、恒真；
 * 内置 npm 在场但坏掉时它们就是 RED。
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { execSync } from 'node:child_process'
import { existsSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { resolveNpmCommand } from '../plugin-installer.js'

/** 宿主 node 旁边那份 npm（打包桌面端的布局：<nodeDir>/bin/npm）。 */
function bundledNpmPath(): string {
  return process.platform === 'win32'
    ? join(dirname(process.execPath), 'npm.cmd')
    : join(dirname(process.execPath), 'bin', 'npm')
}

test('resolveNpmCommand 返回的 npm 必须真的能跑（--version 成功）', () => {
  const cmd = resolveNpmCommand()
  let out = ''
  let failure: unknown = null
  try {
    out = execSync(`"${cmd}" --version`, { stdio: 'pipe', encoding: 'utf8', timeout: 15_000 })
  } catch (err) {
    failure = err
  }
  assert.equal(
    failure,
    null,
    `选中的 npm 跑不起来（存在 ≠ 可用）：${cmd}\n${failure instanceof Error ? failure.message : ''}`,
  )
  assert.match(out.trim(), /^\d+\.\d+\.\d+/, `npm --version 输出异常：${out}`)
})

test('内置 npm 存在但跑不通时，必须回落——不得返回它', () => {
  const bundled = bundledNpmPath()
  if (!existsSync(bundled)) return // 普通开发机/CI：无内置垫片，本用例无前提
  let bundledUsable = true
  try {
    execSync(`"${bundled}" --version`, { stdio: 'pipe', timeout: 15_000 })
  } catch {
    bundledUsable = false
  }
  if (bundledUsable) return // 内置 npm 健康 → 选它是正确的，无需断言
  assert.notEqual(
    resolveNpmCommand(),
    bundled,
    '内置 npm 存在但跑不通，仍被选中——插件安装会整体失败，且报错看不出真因',
  )
})
