import { buildFocusedReadView } from '../../../src/tools/focused-read.js'

// 对账 focused-read 的**确定性子逻辑**：tokenizeFocus（间接，经内容反映）、
// scoreLine / mergeRanges（经 ranges 反映）、renderFocusedContent（经 content）。
//
// 注意：TS 的 structuralSkeleton 依赖 foldCode；Go 侧首版不做 fold（用回退），
// 故 oracle 里的"无匹配"用例只对账 header/footer 形态，不对账骨架内容。

type C = {
  label: string
  filePath: string
  content: string
  focus: string
  maxChars: number
  maxMatches: number
  contextLines: number
}

const cases: C[] = []
function add(c: Partial<C> & { label: string; content: string; focus: string }) {
  cases.push({
    filePath: 'src/a.ts', maxChars: 8000, maxMatches: 8, contextLines: 2, ...c,
  } as C)
}

// ── 真实感内容 ──
const TS_SOURCE = [
  "import { foo } from './foo'",
  "import { bar } from './bar'",
  "",
  "export function commitAction(payload: Payload) {",
  "  const result = validate(payload)",
  "  return result",
  "}",
  "",
  "export class SessionManager {",
  "  private sessions: Map<string, Session> = new Map()",
  "",
  "  registerSession(id: string) {",
  "    this.sessions.set(id, createSession(id))",
  "  }",
  "}",
  "",
  "function helperThatNobodyCaresAbout() {",
  "  return 42",
  "}",
].join('\n')

add({ label: 'cnts-匹配英文标识符', content: TS_SOURCE, focus: 'commitAction' })
add({ label: 'cnts-匹配多个 token', content: TS_SOURCE, focus: 'session manager' })
add({ label: 'cnts-无匹配', content: TS_SOURCE, focus: 'zzzznonexistent' })
add({ label: 'cnts-停用词', content: TS_SOURCE, focus: 'the code' })
add({ label: 'cnts-空 focus', content: TS_SOURCE, focus: '' })
add({ label: 'cnts-中文分词', content: TS_SOURCE, focus: '会话管理' })
add({ label: 'cnts-下划线标识符', content: TS_SOURCE, focus: 'helperThatNobodyCaresAbout' })
add({ label: 'cnts-maxMatches=1', content: TS_SOURCE, focus: 'session', maxMatches: 1 })
add({ label: 'cnts-contextLines=0', content: TS_SOURCE, focus: 'session', contextLines: 0 })
add({ label: 'cnts-contextLines=8', content: TS_SOURCE, focus: 'session', contextLines: 8 })
add({ label: 'cnts-小 maxChars', content: TS_SOURCE, focus: 'session', maxChars: 800 })
add({ label: 'cnts-极长 focus', content: TS_SOURCE, focus: 'a'.repeat(300) })

// ── 边界：空内容 / 单行 ──
add({ label: 'cnts-空内容', content: '', focus: 'x' })
add({ label: 'cnts-单行', content: 'const x = 1', focus: 'x' })
add({ label: 'cnts-只空白', content: '\n\n\n', focus: 'x' })

// ── 边界：中文内容 ──
add({ label: 'cnts-中文内容', content: '第一行无关\n第二行目标函数\n第三行无关', focus: '目标' })

// ── 边界：结构性行加分（STRUCTURAL_LINE）──
add({ label: 'cnts-结构行', content: TS_SOURCE, focus: 'export' })

// ── 边界：focus 超长（MAX_FOCUS_LENGTH=240 截断）──
add({ label: 'cnts-超长 focus', content: TS_SOURCE, focus: 'x'.repeat(500) + ' commitAction' })

const out = cases.map(c => {
  const r = buildFocusedReadView({
    filePath: c.filePath,
    content: c.content,
    focus: c.focus,
    maxChars: c.maxChars,
    maxMatches: c.maxMatches,
    contextLines: c.contextLines,
  })
  return {
    label: c.label,
    filePath: c.filePath,
    // **改名**：TS 侧输入与输出都叫 content，Go 侧结构体需两个不同 tag。
    inputContent: c.content,
    focus: c.focus,
    maxChars: c.maxChars,
    maxMatches: c.maxMatches,
    contextLines: c.contextLines,
    // ranges 是确定性核心（score/merge 的产物）。
    ranges: r.ranges,
    matchedLines: r.matchedLines,
    omittedLines: r.omittedLines,
    matched: r.matched,
    outputContent: r.content,
    // content 是否走了"无匹配"分支（Go 侧该分支的骨架不同，需区分）。
    noMatchBranch: !r.matched,
  }
})
console.log(JSON.stringify(out, null, 1))
