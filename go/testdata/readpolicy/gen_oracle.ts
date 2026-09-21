import { decideReadPolicy } from '../../../src/tools/read-policy.js'

type C = { filePath: string; sizeBytes: number; hasExplicitRange: boolean }

const cases: C[] = []
function add(filePath: string, sizeBytes: number, hasExplicitRange = false) {
  cases.push({ filePath, sizeBytes, hasExplicitRange })
}

// ── 真实仓库路径（正斜杠）──
for (const p of [
  'src/tools/read-file.ts', 'src/artifact/summarize.ts', 'go/HANDOFF.md',
  'package.json', 'README.md', 'docs/architecture-overview.md',
  'src/tui/engine/app.ts', 'desktop/src/App.tsx', 'go/internal/tools/readpolicy.go',
]) add(p, 500)

// ── Windows 反斜杠路径（关键：generated 检测不命中）──
add('D:\\proj\\dist\\a.ts', 100)
add('D:\\proj\\build\\b.js', 100)
add('C:\\x\\coverage\\c.ts', 100)
add('D:\\proj\\.next\\d.ts', 100)
add('D:\\proj\\src\\a.ts', 100)

// ── 正斜杠 generated ──
for (const p of ['dist/a.ts', './dist/a.ts', 'x/dist/y/a.ts', 'x/DIST/a.js',
  'build/a.ts', 'coverage/a.ts', '.next/a.ts', 'x/.next/y/a.ts', 'a/dist', 'xdist/a.ts']) {
  add(p, 100)
}

// ── minified ──
for (const p of ['a.min.js', 'a.min.css', 'a.MIN.JS', 'x/a.min.js', 'a.min.ts']) add(p, 100)

// ── jsonl / log ──
for (const p of ['a.jsonl', 'a.ndjson', 'a.jsonl.1', 'a.ndjson.99', 'a.jsonl.x',
  'a.log', 'a.out', 'a.err', 'a.trace', 'a.log.2', 'a.LOG', 'x/y/a.jsonl']) {
  add(p, 100)
}

// ── source 扩展名全覆盖 ──
for (const e of ['ts', 'tsx', 'js', 'jsx', 'mjs', 'cjs', 'json', 'md', 'css',
  'scss', 'html', 'yml', 'yaml']) {
  add(`a.${e}`, 100)
}

// ── unknown ──
for (const p of ['a.txt', 'a.rs', 'a.go', 'a.py', 'noext', 'a.', '.hidden', 'a.TXT']) add(p, 100)

// ── 阈值边界（三个常量的两侧）──
for (const sz of [16383, 16384, 16385, 20479, 20480, 20481, 81919, 81920, 81921, 0, 1]) {
  add('a.ts', sz)
  add('a.log', sz)
  add('dist/a.ts', sz)
}

// ── hasExplicitRange 优先 ──
add('dist/a.ts', 999999, true)
add('a.min.js', 999999, true)
add('a.log', 999999, true)
add('a.ts', 999999, true)

// ── 组合：log 且超 guard 且 generated？──
add('dist/a.log', 999999)
add('a.min.jsonl', 999999)

const out = cases.map(c => {
  const d = decideReadPolicy(c)
  return { ...c, kind: d.kind, action: d.action, reason: d.reason,
    previewLines: d.previewLines, maxRangeLines: d.maxRangeLines }
})
console.log(JSON.stringify(out, null, 1))
