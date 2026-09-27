/**
 * 工具结果摘要 —— 从 `tool-pipeline.ts` 沿接缝拆出（2026-09-23）。
 *
 * 职责单一：把动辄上千行的工具输出压成一行「这是什么、多长、关键信息在哪」，
 * 供 artifact 拦截路径把正文换成引用时，仍留给模型一条可读的替代说明——否则
 * 模型只知道「内容被存到磁盘了」，不知道刚才那次调用有没有报错、跑了几个用例。
 *
 * 与管线其余部分零耦合（纯字符串处理，不碰 deps、不读会话状态），因此独立成模块：
 * `tool-pipeline.ts` 是点名巨石（见 `scripts/source-budgets.manifest.json` 的
 * ceiling），增长必须沿接缝拆分而不是继续膨胀。本次拆分落地后同 PR 收紧该 ceiling。
 */

/** 从错误输出里挑最有诊断价值的几行（上限约 600 字符）。 */
export function extractErrorHead(content: string): string {
  const lines = content.split('\n')
  // Prioritize lines with error/fail keywords — use word boundaries to avoid matching identifiers like errorHandler
  const errorLines = lines.filter(l => /\b(?:error|Error|FAIL|AssertionError|TypeError|ReferenceError)\b|expect\(/.test(l))
  if (errorLines.length > 0) {
    return errorLines.slice(0, 8).map(l => l.trim().slice(0, 120)).join('\n')
  }
  // Fallback: last 8 lines (often contain the summary)
  return lines.slice(-8).map(l => l.trim().slice(0, 120)).join('\n')
}

/** 一行摘要：按工具挑各自最该被看见的那行（测试汇总 / 改动文件 / 命令本身…）。 */
export function generateToolSummary(content: string, toolName: string, input: Record<string, unknown>): string {
  const lines = content.split('\n')
  const lineCount = lines.length
  const charCount = content.length

  switch (toolName) {
    case 'run_tests': {
      // Extract test summary from content
      const testLine = lines.find(l => /tests?\s*(?:pass|passed|fail|failed)|total/i.test(l))
        ?? lines.find(l => /\d+\s+pass/i.test(l))
      const errorLines = lines.filter(l => /error|Error|FAIL/i.test(l)).slice(0, 2)
      const parts = [`[run_tests] ${lineCount} lines.`]
      if (testLine) parts.push(testLine.trim())
      if (errorLines.length > 0) parts.push(`Errors: ${errorLines.map(l => l.trim().slice(0, 60)).join('; ')}`)
      return parts.join(' ')
    }
    case 'diff': {
      const files = lines.filter(l => l.startsWith('diff --git')).map(l => {
        const m = l.match(/b\/(.+)$/)
        return m ? m[1] : ''
      }).filter(Boolean)
      return `[diff] ${files.length} files changed, ${lineCount} lines. Files: ${files.slice(0, 5).join(', ')}${files.length > 5 ? ` (+${files.length - 5})` : ''}`
    }
    case 'glob': {
      const matches = lines.filter(l => l.trim())
      const pattern = typeof input.pattern === 'string' ? input.pattern : '?'
      return `[glob "${pattern}"] ${matches.length} files found. First: ${matches.slice(0, 3).join(', ')}${matches.length > 3 ? ` (+${matches.length - 3})` : ''}`
    }
    case 'web_fetch': {
      const url = typeof input.url === 'string' ? input.url : '?'
      return `[web_fetch ${url}] ${charCount} chars, ${lineCount} lines fetched.`
    }
    case 'repo_map': {
      return `[repo_map] ${lineCount} lines. ${lines.find(l => /\d+ files/.test(l))?.trim() ?? `${lineCount} entries`}`
    }
    case 'inspect_project': {
      return `[inspect_project] ${lineCount} lines of project analysis.`
    }
    case 'bash': {
      const cmd = typeof input.command === 'string' ? input.command.slice(0, 80) : '?'
      // Detect test/typecheck output
      if (/\b(tsc|typecheck|type-check)\b/.test(cmd)) {
        const errorCount = lines.filter(l => /error TS\d+/.test(l)).length
        return `[bash typecheck] ${errorCount} errors, ${lineCount} lines. cmd: ${cmd}`
      }
      if (/\b(test|jest|vitest|mocha|pytest)\b/.test(cmd)) {
        const passLine = lines.find(l => /pass|fail|tests?\s+\d+/i.test(l))?.trim().slice(0, 80) ?? ''
        return `[bash test] ${lineCount} lines. ${passLine} cmd: ${cmd}`
      }
      return `[bash] ${charCount} chars, ${lineCount} lines. cmd: ${cmd}`
    }
    default: {
      // Generic: first meaningful line + stats
      const firstLine = lines.find(l => l.trim().length > 10)?.trim().slice(0, 80) ?? ''
      return `[${toolName}] ${charCount} chars, ${lineCount} lines. ${firstLine}`
    }
  }
}
