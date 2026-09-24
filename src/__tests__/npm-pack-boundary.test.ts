/**
 * npm 发布产物边界（2026-09-17）。
 *
 * `src/pro/` 是闭源实现（`sync-to-public.sh --exclude 'pro/'`，不进公开仓），
 * 由桌面端的 runtime bundle 承载。但 `package.json` 的 `files` 是 `["dist/", …]`
 * ——构建产物里的**同一份实现曾被一起发到 npm**：实测 `npm pack --dry-run` 列出
 *
 *     npm notice 181.3kB dist/pro/computer-use/index.js
 *     npm notice 7.1kB   dist/pro/index.js
 *
 * 那等于把闭源代码 minify 后当开源包分发：任何人 `npm i -g` 就拿到 computerUse
 * 实现，而 npm 侧**没有任何完整性校验**（那是桌面端 Rust 壳的能力，
 * `lib.rs:3458` + `integrity.rs:374`），改一行判定即可用，还能整包转发。
 *
 * 本文件守这条边界：**dist/pro/ 不得进 npm 包**——源码侧（sync 排除 src/pro）
 * 与产物侧（files 排除 dist/pro）同封。
 *
 * 不影响桌面端：`scripts/build-runtime-bundle.sh` 直接取 `dist/`，不经 npm
 * files 过滤，所以 runtime bundle 里 `pro/computer-use/` 照旧在。
 */
import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

interface PackageJson {
  files?: string[]
}

const pkg = JSON.parse(
  readFileSync(fileURLToPath(new URL('../../package.json', import.meta.url)), 'utf8')
) as PackageJson
const files = pkg.files ?? []

describe('npm 发布产物边界：闭源实现不得随包分发', () => {
  it('files 排除 dist/pro（否则 computerUse 实现随 npm 包公开）', () => {
    assert.ok(
      files.some((f) => /^!dist\/pro\/?$/.test(f)),
      'package.json files 缺少 "!dist/pro"——dist/pro/** 会随 npm 包分发。'
        + `当前 files=${JSON.stringify(files)}`
    )
  })

  it('排除项保持与 dist/native 同形（`!dist/…` = 构建产物中的非公开面）', () => {
    assert.ok(
      files.some((f) => f.startsWith('!dist/')),
      'files 应保留 `!dist/…` 形式的排除项'
    )
    assert.ok(files.includes('dist/'), 'files 仍以 dist/ 为白名单根')
  })
})
