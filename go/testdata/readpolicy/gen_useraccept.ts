// gen_useraccept.ts —— readpolicy 用户级验收的黄金数据生成器。
//
// **与 gen_oracle.ts 的区别**：那个用构造的 size 值；这个用 `statSync` 读
// **真实仓库文件的真实字节数**，覆盖真实的 size × 路径组合。
//
// 运行：node_modules/.bin/tsx go/testdata/readpolicy/gen_useraccept.ts > golden.json
import { statSync } from 'node:fs'
import { decideReadPolicy } from '../../../src/tools/read-policy.js'

type C = { filePath: string; sizeBytes: number; hasExplicitRange: boolean }
const cases: C[] = []

// 真实仓库文件（含真实 size）。
const REAL = [
  'src/tools/read-file.ts', 'src/artifact/summarize.ts', 'src/tools/truncation.ts',
  'src/tools/read-policy.ts', 'go/HANDOFF.md', 'package.json', 'README.md',
  'go/internal/tools/readpolicy.go', 'go/internal/artifact/summarize.go',
  'src/tui/engine/app.ts', 'desktop/src/App.tsx', 'go/PLAN.md',
]
for (const p of REAL) {
  let sz: number
  try {
    sz = statSync(p).size
  } catch {
    console.error(`跳过（不存在）: ${p}`)
    continue
  }
  cases.push({ filePath: p, sizeBytes: sz, hasExplicitRange: false })
  cases.push({ filePath: p, sizeBytes: sz, hasExplicitRange: true })
}

// 真实路径 + 合成 size（覆盖三档阈值，因为真实文件多数落在同一档）。
const SYNTH_SIZES = [0, 1, 16384, 16385, 20480, 20481, 81920, 81921, 1000000]
for (const p of ['src/tools/read-file.ts', 'go/HANDOFF.md', 'dist/bundle.js',
  'a.min.js', 'logs/app.log', 'data/events.jsonl', 'x/build/y.ts',
  'D:\\proj\\dist\\a.ts', 'C:/proj/dist/b.ts']) {
  for (const sz of SYNTH_SIZES) {
    cases.push({ filePath: p, sizeBytes: sz, hasExplicitRange: false })
  }
}

const out = cases.map(c => {
  const d = decideReadPolicy(c)
  return { ...c, kind: d.kind, action: d.action, reason: d.reason }
})
console.log(JSON.stringify(out, null, 1))
