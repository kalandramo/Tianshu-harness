/**
 * Cron 表达式解析与「下次触发时刻」计算。
 *
 * 从 `cron-scheduler.ts` 沿接缝拆出（2026-09-24）：那个文件是表外文件、受 800 行
 * 统一红线约束，issue #266 的写盘健康改动把它推过了线。这一族是纯计算——不碰调度器
 * 实例状态、不读盘，只把 (表达式 | 任务, now) 映射成时间戳，与 tick 循环、持久化都无关。
 */
import type { ScheduledTask } from './cron-scheduler.js'

/**
 * Parse one cron field into the set of matching values.
 * Supports `*`, `n`, `a-b`, comma lists of any of those, and a `/step` suffix
 * on the wildcard or a range (e.g. `5/15`, `a-b/step`). Returns null on any
 * syntax or range error.
 */
function parseCronField(field: string, min: number, max: number): Set<number> | null {
  const values = new Set<number>()
  for (const part of field.split(',')) {
    if (!part) return null
    const [rangeExpr, stepExpr, extra] = part.split('/')
    if (extra !== undefined) return null
    let step = 1
    if (stepExpr !== undefined) {
      step = parseInt(stepExpr, 10)
      if (!/^\d+$/.test(stepExpr) || isNaN(step) || step < 1) return null
    }
    let lo: number
    let hi: number
    if (rangeExpr === '*') {
      lo = min
      hi = max
    } else if (/^\d+$/.test(rangeExpr!)) {
      lo = parseInt(rangeExpr!, 10)
      // A bare number with a step (`5/15`) means "from 5 to max, every 15".
      hi = stepExpr !== undefined ? max : lo
    } else {
      const m = /^(\d+)-(\d+)$/.exec(rangeExpr!)
      if (!m) return null
      lo = parseInt(m[1]!, 10)
      hi = parseInt(m[2]!, 10)
    }
    if (lo < min || hi > max || lo > hi) return null
    for (let v = lo; v <= hi; v += step) values.add(v)
  }
  return values.size > 0 ? values : null
}

interface ParsedCron {
  minutes: Set<number>
  hours: Set<number>
  daysOfMonth: Set<number>
  months: Set<number>
  daysOfWeek: Set<number>
  domRestricted: boolean
  dowRestricted: boolean
}

export function parseCronExpr(expr: string): ParsedCron | null {
  const parts = expr.trim().split(/\s+/)
  if (parts.length !== 5) return null
  const minutes = parseCronField(parts[0]!, 0, 59)
  const hours = parseCronField(parts[1]!, 0, 23)
  const daysOfMonth = parseCronField(parts[2]!, 1, 31)
  const months = parseCronField(parts[3]!, 1, 12)
  // Day-of-week accepts 0-7 with 7 ≡ Sunday ≡ 0 (both cron dialects in the wild).
  const rawDow = parseCronField(parts[4]!, 0, 7)
  if (!minutes || !hours || !daysOfMonth || !months || !rawDow) return null
  const daysOfWeek = new Set<number>()
  for (const d of rawDow) daysOfWeek.add(d === 7 ? 0 : d)
  return {
    minutes,
    hours,
    daysOfMonth,
    months,
    daysOfWeek,
    domRestricted: parts[2] !== '*',
    dowRestricted: parts[4] !== '*',
  }
}

/**
 * Next fire time (UTC) strictly after `from` for a standard 5-field cron
 * expression. Standard day-matching rule: when BOTH day-of-month and
 * day-of-week are restricted, a day fires if EITHER matches; otherwise the
 * restricted one (or neither) applies.
 */
export function nextCronTime(expr: string, from: number): number | null {
  const cron = parseCronExpr(expr)
  if (!cron) return null

  const dayMatches = (d: Date): boolean => {
    if (!cron.months.has(d.getUTCMonth() + 1)) return false
    const domOk = cron.daysOfMonth.has(d.getUTCDate())
    const dowOk = cron.daysOfWeek.has(d.getUTCDay())
    if (cron.domRestricted && cron.dowRestricted) return domOk || dowOk
    if (cron.domRestricted) return domOk
    if (cron.dowRestricted) return dowOk
    return true
  }

  // Scan day-by-day (bounded to 4 years to cover Feb-29 schedules), then pick
  // the earliest in-set hour/minute — cheap: at most ~1461 iterations.
  const start = new Date(from)
  start.setUTCSeconds(0, 0)
  const startDay = Date.UTC(start.getUTCFullYear(), start.getUTCMonth(), start.getUTCDate())
  const sortedHours = [...cron.hours].sort((a, b) => a - b)
  const sortedMinutes = [...cron.minutes].sort((a, b) => a - b)
  for (let dayOffset = 0; dayOffset <= 4 * 366; dayOffset++) {
    const day = new Date(startDay + dayOffset * 24 * 60 * 60 * 1000)
    if (!dayMatches(day)) continue
    for (const h of sortedHours) {
      for (const m of sortedMinutes) {
        const candidate = Date.UTC(day.getUTCFullYear(), day.getUTCMonth(), day.getUTCDate(), h, m, 0, 0)
        if (candidate > from) return candidate
      }
    }
  }
  return null
}

export function computeNextTrigger(task: ScheduledTask, now: number): number | null {
  switch (task.trigger.type) {
    case 'startup':
    case 'app-open':
    case 'file-change':
    case 'git-push':
    case 'focus-change':
      // 事件触发器——不参与 tick 轮询。由外部事件（OS 开机 / 应用打开 / 文件
      // 变更 / git push / 窗口聚焦）经 CronScheduler.fireByEvent() 显式触发。
      return null
    case 'interval': {
      const ms = parseInt(task.trigger.spec, 10)
      if (isNaN(ms) || ms <= 0) return null
      const base = task.lastTriggeredAt
        ? new Date(task.lastTriggeredAt).getTime()
        : new Date(task.createdAt).getTime()
      return base + ms
    }
    case 'cron': {
      // Compute from the last fire (or creation), NOT from `now`: next is
      // strictly in the future relative to its base, so basing it on `now`
      // meant `next <= now` never held and cron tasks never fired. With the
      // last-fire base, a tick landing any time after the scheduled minute
      // sees next <= now and fires exactly once.
      const base = task.lastTriggeredAt
        ? new Date(task.lastTriggeredAt).getTime()
        : new Date(task.createdAt).getTime()
      if (isNaN(base)) return null
      return nextCronTime(task.trigger.spec, base)
    }
    case 'oneshot': {
      if (task.triggerCount > 0) return null
      const ts = new Date(task.trigger.spec).getTime()
      if (isNaN(ts)) return null
      return ts <= now ? now : ts
    }
  }
}
