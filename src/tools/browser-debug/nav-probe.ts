/**
 * browser-debug/nav-probe — 导航连接失败时的本机端口探活与 scripts 端口解析。
 *
 * 从 tool.ts 沿接缝拆出（行数棘轮）。只探测，绝不启动任何服务。
 */

import { createConnection } from 'node:net'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'

/** Common dev server ports to probe when navigation fails with a connection
 *  error. Ordered by prevalence — Vite (5173), Next.js (3000), common
 *  alternatives. Only localhost — no external network. */
const DEV_PORT_CANDIDATES = [5173, 3000, 8080, 4200, 3001, 5000, 8000, 9000, 1234, 6006]

/** Try connecting to localhost:port; resolve with port number if listening,
 *  reject if not. Timeout set low so probing a dozen ports takes ~1s total. */
function probePort(port: number, timeoutMs = 150): Promise<number> {
  return new Promise((resolve, reject) => {
    const sock = createConnection(port, '127.0.0.1')
    const timer = setTimeout(() => { sock.destroy(); reject(new Error('timeout')) }, timeoutMs)
    sock.on('connect', () => { clearTimeout(timer); sock.destroy(); resolve(port) })
    sock.on('error', () => { clearTimeout(timer); sock.destroy(); reject(new Error('refused')) })
  })
}

/** Scan candidate ports in parallel, return the ones that are listening. */
export async function probeDevPorts(): Promise<number[]> {
  const results = await Promise.allSettled(DEV_PORT_CANDIDATES.map((p) => probePort(p)))
  return results
    .filter((r): r is PromiseFulfilledResult<number> => r.status === 'fulfilled')
    .map((r) => r.value)
}

/** Read package.json and extract likely dev server port numbers from scripts.
 *  Looks for `--port N`, `-p N`, `:N`（rollup/vite output）, and `PORT=N`.
 *  Returns deduplicated integer ports. */
export function parseDevPortsFromScripts(cwd: string): number[] {
  try {
    const raw = readFileSync(join(cwd, 'package.json'), 'utf-8')
    const pkg = JSON.parse(raw) as { scripts?: Record<string, string> }
    if (!pkg.scripts) return []
    const ports = new Set<number>()
    const seen = new Set<string>()
    for (const cmd of Object.values(pkg.scripts)) {
      // --port 3000 / -p 3000
      for (const m of cmd.matchAll(/(?:--port|-p)\s+(\d{2,5})/g)) {
        const p = parseInt(m[1]!, 10)
        if (!seen.has(`flag:${p}`) && p > 1 && p < 65536) { ports.add(p); seen.add(`flag:${p}`) }
      }
      // vite/rollup "localhost:5173" output line
      for (const m of cmd.matchAll(/:(\d{4,5})\b/g)) {
        const p = parseInt(m[1]!, 10)
        if (!seen.has(`colon:${p}`) && p > 1024 && p < 65536) { ports.add(p); seen.add(`colon:${p}`) }
      }
      // PORT=3000 env style
      for (const m of cmd.matchAll(/\bPORT=(\d{2,5})\b/g)) {
        const p = parseInt(m[1]!, 10)
        if (!seen.has(`env:${p}`) && p > 1 && p < 65536) { ports.add(p); seen.add(`env:${p}`) }
      }
    }
    return [...ports]
  } catch {
    return []
  }
}
