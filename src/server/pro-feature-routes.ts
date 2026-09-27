/**
 * `GET /config/pro-features` —— Pro 能力矩阵（口径对齐的单一查询口）。
 *
 * 与 `GET /config/computer-use` 的分工：那个是 computer_use 的深度状态
 * （平台、系统权限、授权应用），本路由只回答「有哪些 Pro 能力、本构建是否
 * 具备、许可证是否允许、实际是否可用」。桌面 UI 的 Pro 清单/权益面据此渲染，
 * 不再手写第二份功能表；官网对齐时也以本路由的 key 为准。
 *
 * 子模块化原因与 config-routes-keys 同款：`config-routes.ts` 是点名巨石
 * （source-budgets ceiling），按接缝外提而不是继续膨胀。
 */
import { loadConfig } from '../config/manager.js'
import { isProFeatureEnabled, resolveProLicense } from '../config/pro-license.js'
import { proModulePresent } from '../api/pro-registry.js'
import { computerUseModulePresent } from '../tools/computer-use/bridge.js'
import { PRO_CATALOG_BY_KEY, type ProModuleProbe } from '../agent/pro-catalog.js'
import { buildProFeatureMatrix } from './pro-feature-probe.js'
import { isAuthorizedRequest } from './auth.js'
import type { RouteHandler } from './index.js'

/**
 * moduleProbe 枚举 → 存在性探测实现。
 *
 * 两条纪律：① 探测走**产物/磁盘**，绝不问 `proRegistry`——注册挂在付费闸门
 * 之后（`src/pro/index.ts`），未激活时注册表恒空，拿它当判据会把「本构建有没有」
 * 混成「用户买没买」；② 新增闭源能力必须同时登记 pro-catalog 与本表——漏登时
 * 矩阵不会询问它（moduleProbe=null → available 只由 wired 决定）→ 恒报不可用，
 * 方向是 fail-closed，不会把没有的能力报成有。
 */
const MODULE_PROBES: Record<NonNullable<ProModuleProbe>, () => boolean> = {
  'spark-preset': proModulePresent,
  'computer-use': computerUseModulePresent,
}

function withAuth(handler: RouteHandler, apiToken?: string): RouteHandler {
  return async (body, params, headers, res) => {
    if (!isAuthorizedRequest({ body, headers }, apiToken)) {
      return { status: 401, body: { error: 'Unauthorized' } }
    }
    return handler(body, params, headers, res)
  }
}

export function buildProFeatureRoutes(apiToken?: string): Record<string, RouteHandler> {
  return {
    'GET /config/pro-features': withAuth(() => {
      const cfg = loadConfig()
      const license = resolveProLicense(cfg)
      const features = buildProFeatureMatrix({
        licensed: (key) => isProFeatureEnabled(cfg, key),
        modulePresent: (key) => {
          // 表驱动分派：探测方式的事实源是 pro-catalog 的 moduleProbe 字段，
          // 这里不再按 key 硬编码（双源会漂移——新增能力漏改分支时静默报不可用）。
          const probe = PRO_CATALOG_BY_KEY[key].moduleProbe
          if (!probe) return false // 内置能力：矩阵不询问（见 buildProFeatureMatrix）
          return MODULE_PROBES[probe]()
        },
      })
      return {
        status: 200,
        body: {
          active: license.enabled,
          tier: license.tier ?? null,
          reason: license.reason ?? 'unknown',
          features,
        },
      }
    }, apiToken),
  }
}
