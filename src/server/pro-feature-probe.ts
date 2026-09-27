import { PRO_CATALOG } from '../agent/pro-catalog.js'
import type { ProFeature } from '../config/pro-license.js'

/**
 * Pro 能力矩阵：把「能力目录 + 许可证 + 本构建的模块存在性」合成一份可下发
 * 给 UI 的事实（`GET /config/pro-features`）。
 *
 * 三个字段各管一件事，不要再压成一个布尔：
 * - `available`：**本机构建**是否具备该能力（与许可证无关）——公开构建没有
 *   闭源模块时应为 false，UI 不能拿它做升级承诺；
 * - `licensed`：许可证是否允许（Pro 激活且未被 `config.pro.features` 关闭）——
 *   官网/桌面展示「Pro 包含什么」时用它区分免费与付费；
 * - `enabled`：实际可用 = 两者都成立。
 *
 * 核纯函数化是为了能穷举边界（闭源缺席 / 未激活 / 预留位），路由只负责注入
 * 「许可证」与「模块是否存在」两个真实依赖。
 */
export interface ProFeatureStatus {
  /** 本构建是否具备该能力（wired && 闭源模块在场） */
  available: boolean
  /** 许可证是否允许 */
  licensed: boolean
  /** 实际可用 */
  enabled: boolean
}

export type ProFeatureMatrix = Record<ProFeature, ProFeatureStatus>

export interface ProFeatureProbeDeps {
  /** 许可证判定：`isProFeatureEnabled(cfg, key)` */
  licensed(key: ProFeature): boolean
  /** 闭源模块存在性；目录里 moduleProbe=null 的能力不会调用它 */
  modulePresent(key: ProFeature): boolean
}

export function buildProFeatureMatrix(deps: ProFeatureProbeDeps): ProFeatureMatrix {
  const matrix = {} as ProFeatureMatrix
  for (const entry of PRO_CATALOG) {
    // 未接线的预留位恒不可用——即使许可证允许、官网在卖，也不能在本机构建里跑。
    const moduleOk = entry.moduleProbe === null ? true : deps.modulePresent(entry.key)
    const available = entry.wired && moduleOk
    const licensed = deps.licensed(entry.key)
    matrix[entry.key] = { available, licensed, enabled: available && licensed }
  }
  return matrix
}
