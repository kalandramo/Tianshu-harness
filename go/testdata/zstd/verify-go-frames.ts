/**
 * 验证 Go 产出的 zstd 帧能被 Node 解码（跨版本兼容的另一半）。
 *
 * 运行：
 *   cd go && go test ./internal/session/ -run TestEmitGoFramesForNode
 *   npx tsx go/testdata/zstd/verify-go-frames.ts
 *
 * 为什么需要它：Go→Node 的方向无法在 Go 测试里验证（需要 Node 的
 * zstdDecompressSync）。这个脚本补上那一半，构成完整的双向兼容证据。
 */
import { readFileSync } from 'node:fs'
import { zstdDecompressSync } from 'node:zlib'

const data = JSON.parse(readFileSync('/tmp/go-frames.json', 'utf-8'))
let pass = 0
let fail = 0

for (const [name, hex] of Object.entries(data.frames as Record<string, string>)) {
  if (name === 'concat') continue
  const want = (data.expect as Record<string, string>)[name]
  try {
    const got = zstdDecompressSync(Buffer.from(hex, 'hex')).toString('utf-8')
    if (got === want) {
      console.log(`  ✅ ${name}`)
      pass++
    } else {
      console.log(`  ❌ ${name}: 内容不符`)
      fail++
    }
  } catch (e) {
    console.log(`  ❌ ${name}: 解压失败 ${String(e).slice(0, 80)}`)
    fail++
  }
}

// 拼接
try {
  const buf = Buffer.from(data.frames.concat as string, 'hex')
  // 拼接帧需要逐帧解（Node 的 zstdDecompressSync 只解第一帧）
  const { zstdDecompressSync: d } = await import('node:zlib')
  const first = d(buf.subarray(0, buf.length)).toString('utf-8')
  console.log(`  ${first === '第一帧\n' ? '✅' : '❌'} concat(首帧)`)
  first === '第一帧\n' ? pass++ : fail++
} catch (e) {
  console.log(`  ❌ concat: ${String(e).slice(0, 80)}`)
  fail++
}

console.log(`\nGo→Node 兼容：${pass} 通过 / ${fail} 失败`)
process.exit(fail > 0 ? 1 : 0)
