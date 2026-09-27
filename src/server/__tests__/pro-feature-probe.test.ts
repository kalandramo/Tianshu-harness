import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { PRO_CATALOG, PRO_CATALOG_BY_KEY } from '../../agent/pro-catalog.js'
import { buildProFeatureMatrix } from '../pro-feature-probe.js'
import type { ProFeature } from '../../config/pro-license.js'

/**
 * Pro 能力目录 + 能力矩阵的纯函数测试。
 *
 * 这一层是「官网/桌面/运行时口径对齐」的地基，最容易静默出错的是三件事：
 *   ① 目录与运行时 `ProFeature` 漂移（新增了收费能力但目录没登记）；
 *   ② 把「本构建没有的能力」报成可用（公开构建没有闭源模块，UI 却承诺）；
 *   ③ 把「预留位」当卖点（chatGateway 只有 schema 默认值，没有接线）。
 */

const RUNTIME_KEYS: ProFeature[] = [
  'computerUse',
  'chatGateway',
  'teamMax',
  'councilMultiRound',
  'unattendedAutomation',
  'spark',
]

describe('Pro 能力目录：单一事实源', () => {
  it('与运行时 ProFeature 穷举对齐，无重复无缺漏', () => {
    const catalogKeys = PRO_CATALOG.map((entry) => entry.key)
    assert.equal(new Set(catalogKeys).size, catalogKeys.length, '目录不得有重复 key')
    assert.deepEqual([...catalogKeys].sort(), [...RUNTIME_KEYS].sort(), '新增收费能力必须同步登记目录')
  })

  it('每个 key 都能按 key 取到条目（路由/展示面的查询契约）', () => {
    for (const key of RUNTIME_KEYS) {
      assert.equal(PRO_CATALOG_BY_KEY[key]?.key, key)
    }
  })

  it('预留位 wired=false：任何展示面都不得宣传它', () => {
    assert.equal(PRO_CATALOG_BY_KEY.chatGateway.wired, false, 'chatGateway 无生产实现')
  })

  it('闭源能力声明了探测方式；内置能力不需要探测', () => {
    assert.equal(PRO_CATALOG_BY_KEY.spark.moduleProbe, 'spark-preset')
    assert.equal(PRO_CATALOG_BY_KEY.computerUse.moduleProbe, 'computer-use')
    assert.equal(PRO_CATALOG_BY_KEY.teamMax.moduleProbe, null)
  })
})

describe('能力矩阵：available / licensed / enabled 三轴', () => {
  const deps = (licensed: boolean, present: boolean) => ({
    licensed: () => licensed,
    modulePresent: () => present,
  })

  it('未激活：licensed 与 enabled 全 false，available 仍按构建如实报告', () => {
    const matrix = buildProFeatureMatrix(deps(false, true))
    for (const key of RUNTIME_KEYS) {
      assert.equal(matrix[key].licensed, false, `${key} 未激活不得 licensed`)
      assert.equal(matrix[key].enabled, false, `${key} 未激活不得 enabled`)
    }
    assert.equal(matrix.teamMax.available, true, '内置能力与许可证无关')
  })

  it('Pro 激活且模块在场：除预留位外 enabled=true', () => {
    const matrix = buildProFeatureMatrix(deps(true, true))
    for (const key of ['spark', 'computerUse', 'teamMax', 'councilMultiRound', 'unattendedAutomation'] as const) {
      assert.equal(matrix[key].enabled, true, `${key} 应可用`)
    }
    assert.equal(matrix.chatGateway.enabled, false, '预留位即使 licensed 也不得启用')
  })

  it('闭源模块缺席：spark / computerUse 报 available=false（即便有 Pro）', () => {
    const matrix = buildProFeatureMatrix(deps(true, false))
    for (const key of ['spark', 'computerUse'] as const) {
      assert.equal(matrix[key].available, false)
      assert.equal(matrix[key].enabled, false)
    }
    assert.equal(matrix.teamMax.enabled, true, '内置能力不受闭源探测影响')
  })

  it('modulePresent 只对声明了探测方式的能力被询问（内置能力零探测成本）', () => {
    const asked: ProFeature[] = []
    const matrix = buildProFeatureMatrix({
      licensed: () => true,
      modulePresent: (key) => {
        asked.push(key)
        return true
      },
    })
    assert.deepEqual(asked.sort(), ['computerUse', 'spark'])
    assert.equal(matrix.unattendedAutomation.available, true)
  })
})
