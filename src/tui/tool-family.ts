export type ToolFamily = 'read' | 'write' | 'run' | 'find' | 'other'

/**
 * 工具家族元数据。仅 `family`（截断/diff 分支判定）与 `verb`（卡片标题动词）
 * 参与渲染。曾有的 `glyph` 字段在 CC 对标的 tool-card 中未使用（卡片统一 `●`），
 * 已移除以避免误导。
 */
export interface ToolFamilyInfo {
  family: ToolFamily
  verb: string
}

const TOOL_MAP: Record<string, ToolFamilyInfo> = {
  read_file:       { family: 'read',  verb: 'read'     },
  glob:            { family: 'find',  verb: 'find'     },
  grep:            { family: 'find',  verb: 'search'   },
  bash:            { family: 'run',   verb: 'run'      },
  edit_file:       { family: 'write', verb: 'patch'    },
  write_file:      { family: 'write', verb: 'write'    },
  apply_patch:     { family: 'write', verb: 'patch'    },
  run_tests:       { family: 'run',   verb: 'test'     },
  delegate_task:   { family: 'run',   verb: 'delegate' },
  delegate_batch:  { family: 'run',   verb: 'batch'    },
  team_orchestrate:{ family: 'run',   verb: 'team'     },
  git:             { family: 'run',   verb: 'git'      },
  undo:            { family: 'write', verb: 'undo'     },
  web_fetch:       { family: 'read',  verb: 'fetch'    },
  inspect_project: { family: 'find',  verb: 'inspect'  },
  repo_map:        { family: 'find',  verb: 'map'      },
  todo:            { family: 'other', verb: 'todo'     },
  recall:          { family: 'find',  verb: 'recall'   },
  ask_user_question: { family: 'other', verb: 'ask'    },
  browser_debug:   { family: 'other', verb: 'browse'   },
}

const DEFAULT: ToolFamilyInfo = { family: 'other', verb: 'tool' }

export function getToolFamily(toolName: string): ToolFamilyInfo {
  // own-property 查询而非 `TOOL_MAP[name] ?? DEFAULT`：原型链成员
  // （'constructor'/'toString'…）会绕过 ?? 取出一个函数，verb/family 全是 undefined。
  return Object.hasOwn(TOOL_MAP, toolName) ? (TOOL_MAP[toolName] ?? DEFAULT) : DEFAULT
}

/** 是否在已知工具表内——未知工具（mcp__* 等）的卡片标题应如实用工具名而非「Tool」。 */
export function isKnownTool(toolName: string): boolean {
  // 必须用 Object.hasOwn：`in` 含原型链，会把 'constructor'/'toString'/'valueOf'
  // 判成已知工具 → 标题走 toolTitleVerb → getToolFamily 拿不到 verb → charAt 抛 TypeError。
  return Object.hasOwn(TOOL_MAP, toolName)
}

export function getGroupSummary(tools: ReadonlyArray<{ toolName?: string }>): string {
  const counts = new Map<string, number>()
  for (const t of tools) {
    const name = t.toolName ?? 'unknown'
    counts.set(name, (counts.get(name) ?? 0) + 1)
  }
  const parts = [...counts.entries()].map(([name, count]) => `${name} x${count}`)
  const total = tools.length
  const label = total === 1 ? 'tool call' : 'tool calls'
  return `${total} ${label}: ${parts.join(', ')}`
}
