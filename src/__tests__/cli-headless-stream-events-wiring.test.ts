/**
 * `--stream-events` 的**接线可达性**护栏（2026-09-18，issue #208 移植）。
 *
 * ## 事故形状
 *
 * 旗标在 main.ts 顶层被解析、sink 被创建、退出钩子上也挂了 close——**只有一个
 * `sinks.push(eventStream.sink)`，而它在交互式 TUI 的装配路径里**。无头分支
 * （`-p` / `--goal`）在到达那行之前就 `process.exit` 了，于是：
 *
 * - help 文本承诺的审计面在无头下**不存在**；
 * - 且**没有任何信号**——退出码 0、stderr 干净、文件根本不出现（sink 是懒开的，
 *   首个事件才创建文件）。调用方若按 help 文本先建好文件再 `tail -f`，看到的是
 *   永远 0 字节，与"这次运行没有事件"不可区分。
 *
 * 这类"解析了但没接上"的洞，类型系统看不出来（`eventStream` 确实被用过）、
 * 单元测试也看不出来（`runHeadless` 自身没有 sink 参数时完全正常）。所以这里用
 * 源码结构断言守住"两条路径都接线 + 退出前收尾"。
 *
 * 断言刻意**粗粒度**（子串存在性 + 区段切片，不锁行号）；若这是有意重构导致
 * 本文件失败，请连同断言一起更新——不要删掉它，它守的正是回归史上真实发生过的洞。
 */

import { test, describe } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const repoRoot = join(dirname(fileURLToPath(import.meta.url)), '..', '..')
const mainSource = readFileSync(join(repoRoot, 'src', 'main.ts'), 'utf8')

/** 无头分支区段：`const isHeadless =` 判定 → 交互式 TUI 段注释。 */
function headlessRegion(): string {
  const start = mainSource.indexOf('const isHeadless = ')
  const end = mainSource.indexOf('── Interactive TUI')
  assert.ok(
    start >= 0 && end > start,
    'main.ts 结构变了：定位不到无头分支边界（若为有意重构，请同步更新本护栏）',
  )
  const region = mainSource.slice(start, end)
  // 切片非空校验：边界匹配错位时下面的断言会"因为找不到字符串而失败"，尚可归因；
  // 真正危险的是定位逻辑坏掉后拿到空段——那种假绿比失败更贵。
  assert.ok(region.length > 1000, `无头区段切片异常（${region.length} 字符），护栏定位已失效`)
  return region
}

describe('--stream-events 接线可达性', () => {
  test('无头分支把同一个 sink 交给 runHeadless', () => {
    assert.ok(
      headlessRegion().includes('eventSink: eventStream?.sink'),
      '无头分支未接线 --stream-events：旗标会被解析、sink 会被创建，但没有任何事件到达它',
    )
  })

  test('无头分支在 process.exit 前收尾 sink（缓冲写入，不 close 会丢尾段）', () => {
    assert.ok(
      headlessRegion().includes('eventStream?.close()'),
      '无头分支缺少 sink 收尾：process.exit 不会等待缓冲写入，run 尾段事件会丢',
    )
  })

  test('TUI 路径的 sinks 装配仍在（两条路径共用同一 sink 契约）', () => {
    assert.ok(
      mainSource.includes('if (eventStream) sinks.push(eventStream.sink)'),
      'TUI 路径的 --stream-events 装配不见了——两条路径都必须有接线',
    )
  })

  test('TUI 路径保留 tap handle 并在 settle 时 flush（丢 handle = 尾段缓冲无人认领）', () => {
    assert.ok(
      mainSource.includes('const tapped = sinks.length > 0'),
      'TUI 的 tap handle 又被丢掉了：delta 在 tap 内合并到 4000 字符，rejection 收场时没人 flush 就丢尾段',
    )
    assert.ok(
      mainSource.includes('tapped?.flush()'),
      'TUI 侧缺少 settle 时的 tap flush',
    )
  })

  test('runHeadless 配置类型暴露 eventSink（无头接线的前提）', () => {
    const headlessSource = readFileSync(join(repoRoot, 'src', 'headless.ts'), 'utf8')
    assert.ok(
      headlessSource.includes('eventSink?: EventSink'),
      'HeadlessRunConfig 没有 eventSink 字段——无头路径无从接线',
    )
  })
})
