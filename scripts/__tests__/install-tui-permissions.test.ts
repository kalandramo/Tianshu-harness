import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, writeFileSync, chmodSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve, dirname } from 'node:path'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'

// 安装脚本的 npm 全局目录预检。
//
// 缺陷背景：官方 Node 安装包把 npm 全局 prefix 放在 /usr/local（属主 root），
// 普通用户执行 `npm install -g` 必然 EACCES。install-tui.sh 原先直接调用
// `npm install -g`，失败后一律 die 成「网络问题可换官方源重跑」——把权限问题
// 误诊为网络问题，用户照做无效。
//
// 这里用 PATH 前置的 stub npm 复现：prefix 指向只读目录、install 失败。
const HERE = dirname(fileURLToPath(import.meta.url))
const SCRIPT = resolve(HERE, '..', 'install-tui.sh')
const skip = process.platform === 'win32' ? '需要 POSIX shell（Windows 走 install-tui.ps1）' : false

interface Sandbox {
  run: (args?: string[]) => { status: number | null; out: string }
  cleanup: () => void
}

function sandbox(prefixMode: 'locked' | 'writable'): Sandbox {
  const root = mkdtempSync(join(tmpdir(), 'install-tui-preflight-'))
  const stubBin = join(root, 'stub-bin')
  const prefix = join(root, prefixMode === 'locked' ? 'locked-prefix' : 'writable-prefix')
  const globalModules = join(prefix, 'lib', 'node_modules')
  mkdirSync(stubBin, { recursive: true })
  mkdirSync(globalModules, { recursive: true })

  // stub npm：报告全局 prefix；install 的成败由 STUB_NPM_INSTALL_OK 决定
  writeFileSync(
    join(stubBin, 'npm'),
    `#!/bin/sh
case "$1" in
  prefix)
    [ "$2" = "-g" ] && { printf '%s\\n' "$STUB_NPM_PREFIX"; exit 0; }
    exit 0 ;;
  ls) exit 1 ;;
  uninstall) exit 0 ;;
  install)
    if [ "$STUB_NPM_INSTALL_OK" = "1" ]; then exit 0; fi
    printf 'npm error code EACCES\\nnpm error path %s/lib/node_modules/tianshu-harness\\n' "$STUB_NPM_PREFIX" >&2
    exit 1 ;;
esac
exit 0
`,
    { mode: 0o755 },
  )

  // 只读目录 = 模拟 /usr/local 的权限形状
  if (prefixMode === 'locked') chmodSync(globalModules, 0o555)

  return {
    run(args = ['--no-launch']) {
      const r = spawnSync('bash', [SCRIPT, ...args], {
        encoding: 'utf8',
        env: {
          ...process.env,
          PATH: `${stubBin}:${process.env.PATH ?? ''}`,
          STUB_NPM_PREFIX: prefix,
          STUB_NPM_INSTALL_OK: prefixMode === 'writable' ? '1' : '0',
          NPM_CONFIG_REGISTRY: 'https://registry.npmjs.org',
        },
      })
      return { status: r.status, out: `${r.stdout ?? ''}${r.stderr ?? ''}` }
    },
    cleanup() {
      chmodSync(globalModules, 0o755)
      rmSync(root, { recursive: true, force: true })
    },
  }
}

describe('install-tui.sh — npm 全局目录预检', { skip }, () => {
  it('全局目录不可写：明确指出权限原因与修法，不再误诊为网络问题', () => {
    const sb = sandbox('locked')
    try {
      const { status, out } = sb.run()
      assert.notEqual(status, 0, '应非零退出')
      assert.match(out, /不可写/, '脚本自身应点出「全局目录不可写」')
      assert.match(out, /chown|npm-global/, '应给出可操作的修法')
      assert.doesNotMatch(out, /网络问题/, '不应把权限失败误诊为网络问题')
    } finally {
      sb.cleanup()
    }
  })

  it('全局目录可写：不误拦，正常走完安装流程', () => {
    const sb = sandbox('writable')
    try {
      const { status, out } = sb.run()
      assert.equal(status, 0, `可写时应正常结束：\n${out}`)
      assert.doesNotMatch(out, /不可写/, '可写时不应报权限问题')
    } finally {
      sb.cleanup()
    }
  })
})
