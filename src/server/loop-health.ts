/**
 * Event-loop liveness signal (Phase 2 of the desktop reliability plan).
 *
 * The sidecar serves HTTP/SSE and runs the agent loop in ONE Node process, so
 * a long synchronous stretch (sync IO, big JSON.parse, inline diff fallback)
 * starves the SSE keepalive and /health at the same time — the client then
 * sees a "connection interrupted" it can't tell apart from a real network
 * drop. Publishing the measured loop delay on /health lets the UI (and the
 * Rust supervisor later) label that state honestly: "service busy", not
 * "disconnected".
 *
 * Samples once per second. Readers share a non-destructive 30 second window.
 */
import { monitorEventLoopDelay } from 'node:perf_hooks'

export interface LoopLagSnapshot {
  /** p99 event-loop delay in ms over the window since the last snapshot. */
  sampledAt?: number
  p99Ms: number
  /** Worst single delay in ms over the same window. */
  maxMs: number
}

const NS_PER_MS = 1e6

export class LoopHealthMonitor {
  // 20ms resolution keeps sampling overhead negligible (<0.1% CPU) while still
  // resolving the multi-hundred-ms stalls we care about.
  private hist = monitorEventLoopDelay({ resolution: 20 })
  private started = false
  private timer?: ReturnType<typeof setInterval>
  private samples: Array<LoopLagSnapshot & { sampledAt: number }> = []

  start(): void {
    if (this.started) return
    this.hist.enable()
    this.started = true
    this.timer = setInterval(() => this.sample(), 1000)
    this.timer.unref()
  }

  stop(): void {
    if (!this.started) return
    clearInterval(this.timer)
    this.hist.disable()
    this.started = false
  }

  private sample(): void {
    const sampledAt = Date.now()
    const ms = (value: number) => Number.isFinite(value) ? Math.round(value / NS_PER_MS * 10) / 10 : 0
    this.samples.push({ sampledAt, p99Ms: ms(this.hist.percentile(99)), maxMs: ms(this.hist.max) })
    this.hist.reset()
    this.samples = this.samples.filter(sample => sample.sampledAt >= sampledAt - 30_000)
  }

  /** Conservative maximum of one-second p99 samples, not a pooled percentile. */
  snapshot(): LoopLagSnapshot {
    const samples = this.samples.filter(sample => sample.sampledAt >= Date.now() - 30_000)
    return {
      sampledAt: samples.at(-1)?.sampledAt ?? 0,
      p99Ms: Math.max(0, ...samples.map(sample => sample.p99Ms)),
      maxMs: Math.max(0, ...samples.map(sample => sample.maxMs)),
    }
  }
}
