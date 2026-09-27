import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { decideReadPolicy } from '../read-policy.js'

describe('decideReadPolicy', () => {
  it('previews log-like files over the guard size when no explicit range is provided', () => {
    const decision = decideReadPolicy({ filePath: '/repo/logs/app.log', sizeBytes: 20_000, hasExplicitRange: false })
    assert.equal(decision.kind, 'log')
    assert.equal(decision.action, 'preview')
    assert.equal(decision.previewLines, 80)
    assert.equal(decision.maxRangeLines, 200)
  })

  it('allows explicit ranges for JSONL files', () => {
    const decision = decideReadPolicy({ filePath: '/repo/logs/app.jsonl', sizeBytes: 20_000, hasExplicitRange: true })
    assert.equal(decision.kind, 'jsonl')
    assert.equal(decision.action, 'full')
  })

  it('allows normal source files below the hard size guard', () => {
    const decision = decideReadPolicy({ filePath: '/repo/src/app.ts', sizeBytes: 20_000, hasExplicitRange: false })
    assert.equal(decision.kind, 'source')
    assert.equal(decision.action, 'full')
  })

  it('rejects generated minified files unless a range is explicit', () => {
    const decision = decideReadPolicy({ filePath: '/repo/dist/app.min.js', sizeBytes: 20_000, hasExplicitRange: false })
    assert.equal(decision.kind, 'minified')
    assert.equal(decision.action, 'reject-with-range')
  })

  // 2026-09-23: PARTIAL 门由窗口预算决定，80KB 只是下限。
  it('uses the supplied read budget as the PARTIAL door (1M window: 120K > 80K)', () => {
    const sizeBytes = 90 * 1024 // 越历史 80KB 门，但在 1M 窗口的 120K 预算内
    const withBudget = decideReadPolicy({ filePath: '/repo/src/big.ts', sizeBytes, hasExplicitRange: false, budgetChars: 120_000 })
    assert.equal(withBudget.action, 'full-with-hint', '预算装得下就不该判 partial')

    const withoutBudget = decideReadPolicy({ filePath: '/repo/src/big.ts', sizeBytes, hasExplicitRange: false })
    assert.equal(withoutBudget.action, 'partial', '不传预算时保持 80KB 历史门')
  })

  it('never tightens the door below the historical 80KB on small windows', () => {
    // 64K 窗口的 cap 只有 8K；直接拿它当门会把 20KB 文件从 full-with-hint
    // 拽进 partial——那是收紧，不是对齐。
    const decision = decideReadPolicy({ filePath: '/repo/src/app.ts', sizeBytes: 20_000, hasExplicitRange: false, budgetChars: 8_000 })
    assert.equal(decision.action, 'full', '小预算不得把门收到 80KB 以下')
  })

  // 2026-09-23: 日志类从 read_file 硬门豁免，靠这里的两档兜住内存——
  // preview 要全量读入再截头尾，所以必须有上限。
  it('gives a bounded preview for logs within the preview ceiling', () => {
    const decision = decideReadPolicy({ filePath: '/repo/logs/app.log', sizeBytes: 1_000_000, hasExplicitRange: false, budgetChars: 120_000 })
    assert.equal(decision.action, 'preview', '1MB 日志仍在 preview 上限内（读得进内存）')
  })

  it('rejects logs past the preview ceiling rather than letting the hard guard throw', () => {
    const decision = decideReadPolicy({ filePath: '/repo/logs/big.log', sizeBytes: 5 * 1024 * 1024, hasExplicitRange: false, budgetChars: 120_000 })
    assert.equal(decision.action, 'reject-with-range', '超上限的日志不给 preview')
    assert.match(decision.reason, /grep to locate/, '拒绝理由要教模型先 grep 定位')
  })
})
