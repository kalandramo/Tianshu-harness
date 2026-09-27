import type { ProFeature } from '../config/pro-license.js'

/**
 * Pro 能力目录（单一事实源）——把「收费边界」从三份漂移的列表收敛成一份。
 *
 * ## 为什么需要它
 * 同一个 Pro 在三个地方各有一份口径，且已经漂移：
 *   ① 运行时门禁：`src/config/pro-license.ts` 的 `ProFeature` 6 个 key；
 *   ② 桌面展示：`desktop/src/components/ProFeaturesList.tsx` 手写 4 项（没有 spark）；
 *   ③ 官网营销/DB：`pricingFeatures.ts` 与 `feature_permissions` 又是另一套 key
 *      （computer_control / browser_agent / api_access）。
 * 结果：官网宣传的功能桌面看不到、桌面能用的功能官网不提、预留未接线的能力
 * 被当卖点。本目录只声明**机器事实**（key / 交付面 / 是否已接线 / 是否需要
 * 运行时探测），用户语言留在各展示面的 i18n，不在共享层写文案。
 *
 * ## surface 的语义
 * `desktop` = 只有桌面端（含闭源模块）能提供；`all` = CLI 与桌面端同源提供。
 * 官网对比表必须按此严格落列：不要把 `all` 能力写成「桌面端增强」。
 *
 * ## wired 的语义
 * 是否已有**生产实现**。`chatGateway` 至今只有 schema 默认值与类型，没有接线，
 * 因此任何展示面（含官网）都不得宣传——运行时探测会把它恒报为不可用。
 */
export type ProFeatureSurface = 'desktop' | 'all'

/** 可用性探测类型：闭源模块可能不随某些构建分发，必须运行时查明。 */
export type ProModuleProbe = 'spark-preset' | 'computer-use' | null

export interface ProCatalogEntry {
  /** 与运行时 `ProFeature` 同键——禁止在目录里另造 key 名 */
  key: ProFeature
  /** 交付面 */
  surface: ProFeatureSurface
  /** 是否已有生产实现（false = 预留位，任何地方不得宣传） */
  wired: boolean
  /** 探测方式；null = 内置接线随开源运行时提供，无需探测 */
  moduleProbe: ProModuleProbe
}

export const PRO_CATALOG: readonly ProCatalogEntry[] = [
  // 桌面端旗舰：长会话推理精炼（截断 + 锚点补偿）。闭源模块缺席的构建里不可用。
  { key: 'spark', surface: 'desktop', wired: true, moduleProbe: 'spark-preset' },
  // 桌面端 GUI 自动化。闭源模块缺席的构建里不可用。
  { key: 'computerUse', surface: 'desktop', wired: true, moduleProbe: 'computer-use' },
  { key: 'teamMax', surface: 'all', wired: true, moduleProbe: null },
  { key: 'councilMultiRound', surface: 'all', wired: true, moduleProbe: null },
  { key: 'unattendedAutomation', surface: 'all', wired: true, moduleProbe: null },
  // 预留：无生产实现，恒不可用（防止官网/桌面把它当卖点）。
  { key: 'chatGateway', surface: 'desktop', wired: false, moduleProbe: null },
]

export const PRO_CATALOG_BY_KEY: Readonly<Record<ProFeature, ProCatalogEntry>> = Object.fromEntries(
  PRO_CATALOG.map((entry) => [entry.key, entry]),
) as Record<ProFeature, ProCatalogEntry>
