/**
 * 工具输出的结构化文案标记——桌面端镜像层（browser-mirror / walkthrough-recorder
 * / ToolGroup）靠这些前缀从工具输出文本中提取结构信息。
 *
 * HARD CONSTRAINT: 零依赖叶子模块（桌面端经 src/server/ui-shared.ts 引用，
 * 任何 import 都会把内核运行时拖进前端图谱）。改文案必须两端同步——用常量
 * 共享，禁止两边各自手抄。原定义位置（browser.ts / computer-use/tool.ts）
 * re-export 以保持内核调用方不变。
 */

/**
 * 导航结果 URL 前缀——desktop browser-mirror / walkthrough-recorder 靠此前缀
 * 提取当前页。
 */
export const BROWSER_NAVIGATED_PREFIX = '已导航至'

/**
 * 截图结果 URL 前缀——同上。尾随 ` → artifact <id>` 为结构标记，不译。
 */
export const BROWSER_SCREENSHOT_OF_PREFIX = '截图于'

/**
 * 快照结果中可访问性树段落前缀——desktop browser-mirror 靠 includes 保留树文本。
 */
export const COMPUTER_USE_A11Y_TREE_PREFIX = '可访问性树'

/**
 * 截图 artifact 结构标记——三条产出路径都把 `<id>` 尾随在它之后
 *（browser-debug/tool.ts 与 browser.ts 是 `… → artifact <id>`；
 * computer-use/tool.ts 是 `(screenshot → artifact <id>)`）。**不译**。
 *
 * 此前这条规则只写在 BROWSER_SCREENSHOT_OF_PREFIX 的注释里、没有导出常量，于是
 * desktop 的 ToolGroup 与 browser-mirror **各自手抄了一份同样的正则**——正是本模块
 * 顶部硬约束（「用常量共享，禁止两边各自手抄」）所要防的形态。
 */
export const SCREENSHOT_ARTIFACT_MARKER = '→ artifact'

/**
 * 从工具结果文本取截图 artifact id（纯函数、零依赖，符合本模块叶子约束）。
 * 三条产出路径的包装不同（尾随 / 内嵌括号），但 id 都紧接标记之后，故只认标记本身。
 */
export function screenshotArtifactIdOf(text: string): string | null {
  const at = text.indexOf(SCREENSHOT_ARTIFACT_MARKER)
  if (at === -1) return null
  const rest = text.slice(at + SCREENSHOT_ARTIFACT_MARKER.length).trimStart()
  const id = /^[\w.:-]+/.exec(rest)?.[0]
  return id && id.length > 0 ? id : null
}
