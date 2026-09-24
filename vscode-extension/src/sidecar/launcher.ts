/**
 * E1 sidecar 启动器：spawn `rivet serve` → 健康检查。
 *
 * 每工作区一个实例；127.0.0.1 + 随机 Bearer token（fail-closed，与 server
 * 侧 RIVET_SERVER_TOKEN 语义对齐）。P0 实现运行时三级探测的 ①（PATH 上的
 * rivet）与 ③（settings 指定路径）；②自包含运行时下载在 P3。
 *
 * 探测策略：不用 `--version` 探活——CLI 的 TTY 守卫（T9）会在非 TTY 环境把
 * 顶层命令挡下（实测 exit 1）；`serve` 子命令是桌面端非 TTY spawn 的既有
 * 路径不受影响。因此直接 spawn serve：ENOENT → cli-not-found，起不来 →
 * 健康检查超时。
 */
import { spawn, type ChildProcess } from 'node:child_process'
import { randomBytes } from 'node:crypto'
import { createServer } from 'node:net'
import * as os from 'node:os'

export interface SidecarHandle {
  port: number
  token: string
  baseUrl: string
  /** 结束进程（幂等）。 */
  dispose: () => void
  /** 进程退出时回调（异常退出用于 UI 提示 + 重启入口）。 */
  onExit: (cb: (code: number | null) => void) => void
}

export interface LauncherOptions {
  cwd: string
  /** settings 指定的 CLI 路径（三级探测③），空串/未设置回退 PATH 上的 rivet。 */
  cliPath?: string
  /** settings 指定端口，0 = 自动选空闲端口。 */
  port?: number
  /** 日志行回调（接 OutputChannel）。 */
  onLog?: (line: string) => void
}

export type LaunchFailReason = 'cli-not-found' | 'spawn-failed' | 'health-timeout'

export class SidecarLaunchError extends Error {
  readonly reason: LaunchFailReason
  constructor(message: string, reason: LaunchFailReason) {
    super(message)
    this.reason = reason
  }
}

function pickFreePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const srv = createServer()
    srv.listen(0, '127.0.0.1', () => {
      const addr = srv.address()
      if (addr && typeof addr === 'object') {
        const port = addr.port
        srv.close(() => resolve(port))
      } else {
        srv.close(() => reject(new Error('failed to allocate port')))
      }
    })
    srv.on('error', reject)
  })
}

async function waitHealthy(
  baseUrl: string,
  token: string,
  child: ChildProcess,
  getSpawnError: () => Error | undefined,
  timeoutMs = 20_000,
): Promise<void> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const spawnErr = getSpawnError()
    if (spawnErr) {
      const code = (spawnErr as NodeJS.ErrnoException).code
      if (code === 'ENOENT') {
        throw new SidecarLaunchError(
          '未找到 rivet CLI。请先安装（npm i -g tianshu-harness），或在设置 tianshu.cliPath 中指定路径。',
          'cli-not-found',
        )
      }
      throw new SidecarLaunchError(`sidecar 启动失败: ${spawnErr.message}`, 'spawn-failed')
    }
    if (child.exitCode !== null) {
      throw new SidecarLaunchError(`sidecar 启动失败（exit ${child.exitCode}）`, 'spawn-failed')
    }
    try {
      const res = await fetch(`${baseUrl}/health`, {
        headers: { authorization: `Bearer ${token}` },
        signal: AbortSignal.timeout(2_000),
      })
      if (res.ok) return
    } catch {
      // not up yet
    }
    await new Promise((r) => setTimeout(r, 300))
  }
  throw new SidecarLaunchError('sidecar 健康检查超时（20s）', 'health-timeout')
}

export async function launchSidecar(opts: LauncherOptions): Promise<SidecarHandle> {
  const cli = opts.cliPath?.trim() || 'rivet'
  const port = opts.port && opts.port > 0 ? opts.port : await pickFreePort()
  const token = randomBytes(24).toString('hex')
  const baseUrl = `http://127.0.0.1:${port}`

  const child = spawn(cli, ['serve', '--port', String(port)], {
    cwd: opts.cwd,
    env: { ...process.env, RIVET_SERVER_TOKEN: token },
    stdio: ['ignore', 'pipe', 'pipe'],
    // Windows 上 npm 全局命令是 .cmd shim，需要 shell 解析（对齐 CLI 侧
    // /update 的 npm.cmd 经验教训）。
    shell: os.platform() === 'win32',
  })
  let spawnError: Error | undefined
  child.on('error', (err) => { spawnError = err })

  const log = (chunk: Buffer) => {
    for (const line of chunk.toString().split('\n')) {
      if (line.trim()) opts.onLog?.(line)
    }
  }
  child.stdout?.on('data', log)
  child.stderr?.on('data', log)

  const exitCbs: Array<(code: number | null) => void> = []
  let disposed = false
  child.on('exit', (code) => {
    if (!disposed) for (const cb of exitCbs) cb(code)
  })

  try {
    await waitHealthy(baseUrl, token, child, () => spawnError)
  } catch (err) {
    child.kill()
    throw err
  }

  return {
    port,
    token,
    baseUrl,
    dispose: () => {
      disposed = true
      child.kill()
    },
    onExit: (cb) => exitCbs.push(cb),
  }
}
