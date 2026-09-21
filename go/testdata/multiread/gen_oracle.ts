import { relative } from 'node:path'

// 对账 `handleMultiRead` 的**格式层**（不依赖 readFilePayload 的完整管线）。
//
// TS 的 handleMultiRead 调 readFilePayload（含 gitignore/office/partial 等分支），
// Go 侧未移植那些分支——故本 oracle 只锁定可对账的确定性格式：
//   1. 节头 `── <relPath> ──`（反斜杠转正斜杠）
//   2. 节间分隔 `\n\n`
//   3. 错误节 `── <display> ──\nError: <msg>`（display 只在以 cwd 开头时转相对）
//   4. UI 文案 `Read N/M files (X.X KB total)`
//   5. cap 均分 `Math.floor(cap / N)`

type C = {
  label: string
  cwd: string
  paths: string[]
  // 每文件的模拟 modelContent（替代 readFilePayload 的输出）
  modelContents: string[]
  errorIndexes: number[]
}

const cases: C[] = []
function add(c: C) { cases.push(c) }

const CWD = '/repo'

// ── 常规多读 ──
add({
  label: '两个正常文件',
  cwd: CWD,
  paths: ['/repo/a.ts', '/repo/b.ts'],
  modelContents: ['const a = 1', 'const b = 2'],
  errorIndexes: [],
})
add({
  label: '一个错误',
  cwd: CWD,
  paths: ['/repo/a.ts', '/repo/missing.ts', '/repo/c.ts'],
  modelContents: ['const a = 1', '', 'const c = 3'],
  errorIndexes: [1],
})
add({
  label: '全部错误',
  cwd: CWD,
  paths: ['/repo/x.ts', '/repo/y.ts'],
  modelContents: ['', ''],
  errorIndexes: [0, 1],
})
add({
  label: '单文件',
  cwd: CWD,
  paths: ['/repo/only.ts'],
  modelContents: ['const only = 1'],
  errorIndexes: [],
})
// ── 空路径跳过 ──
add({
  label: '含空路径',
  cwd: CWD,
  paths: ['/repo/a.ts', '   ', '/repo/b.ts'],
  modelContents: ['const a = 1', 'const b = 2'],
  errorIndexes: [],
})
// ── 超 5 个（应被 slice(0,5)）──
add({
  label: '七个文件',
  cwd: CWD,
  paths: ['/repo/1.ts', '/repo/2.ts', '/repo/3.ts', '/repo/4.ts', '/repo/5.ts', '/repo/6.ts', '/repo/7.ts'],
  modelContents: ['c1', 'c2', 'c3', 'c4', 'c5', 'c6', 'c7'],
  errorIndexes: [],
})
// ── 相对路径（不以 cwd 开头）的错误 display ──
add({
  label: '相对路径错误',
  cwd: CWD,
  paths: ['relative/path.ts'],
  modelContents: [''],
  errorIndexes: [0],
})
// ── 子目录 ──
add({
  label: '子目录文件',
  cwd: CWD,
  paths: ['/repo/src/deep/nested/a.ts'],
  modelContents: ['const deep = 1'],
  errorIndexes: [],
})

const cap = { maxChars: 120000, headChars: 72000, tailChars: 36000 }

const out = cases.map(c => {
  // 复刻 TS 的格式拼接（不调 readFilePayload）。
  const paths = c.paths.slice(0, 5)
  const perFileCap = {
    maxChars: Math.floor(cap.maxChars / paths.length),
    headChars: Math.floor(cap.headChars / paths.length),
    tailChars: Math.floor(cap.tailChars / paths.length),
  }

  const sections: string[] = []
  let totalBytes = 0
  let errors = 0
  let mi = 0

  for (const rawPath of paths) {
    const trimmed = rawPath.trim()
    if (!trimmed) continue
    if (c.errorIndexes.includes(c.paths.indexOf(rawPath))) {
      const display = trimmed.startsWith(c.cwd) ? relative(c.cwd, trimmed) : trimmed
      sections.push(`── ${display} ──\nError: simulated error`)
      errors++
      mi++
      continue
    }
    const modelContent = c.modelContents[mi] ?? ''
    mi++
    const canonicalPath = trimmed
    const relPath = relative(c.cwd, canonicalPath).replaceAll('\\', '/')
    sections.push(`── ${relPath} ──\n${modelContent}`)
    totalBytes += modelContent.length
  }

  const content = sections.join('\n\n')
  const uiContent = `Read ${paths.length - errors}/${paths.length} files (${(totalBytes / 1024).toFixed(1)} KB total)`
  return {
    label: c.label,
    cwd: c.cwd,
    paths: c.paths,
    modelContents: c.modelContents,
    errorIndexes: c.errorIndexes,
    perFileCap,
    content,
    uiContent,
  }
})
console.log(JSON.stringify(out, null, 1))
