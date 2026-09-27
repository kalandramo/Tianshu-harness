import { createHash, randomUUID } from 'node:crypto'
import { mkdir, open, readFile, rename } from 'node:fs/promises'
import { join } from 'node:path'

export interface RunReceipt {
  requestId: string
  runId: string
  attemptId: string
  fingerprint: string
  state: 'accepted' | 'running' | 'settled' | 'rejected' | 'needs_attention'
  ownerInstanceId?: string
  outcome?: string
}

/** Acknowledgement is durable before execution; duplicates never invoke a run. */
export class RunLedger {
  private locks = new Map<string, Promise<unknown>>()
  private instanceId = randomUUID()
  constructor(private root: string) {}

  async accept(sessionId: string, requestId: string, payload: unknown, start: (receipt: RunReceipt) => boolean): Promise<RunReceipt & { duplicate: boolean }> {
    if (!/^[a-zA-Z0-9_-]{1,128}$/.test(requestId)) throw new Error('Invalid requestId')
    const key = createHash('sha256').update(`${sessionId}\0${requestId}`).digest('hex')
    const previous = this.locks.get(key) ?? Promise.resolve()
    const operation = previous.catch(() => {}).then(async () => {
      const path = join(this.root, `${key}.json`)
      const fingerprint = createHash('sha256').update(JSON.stringify(payload)).digest('hex')
      let existing: RunReceipt | undefined
      try { existing = JSON.parse(await readFile(path, 'utf8')) as RunReceipt }
      catch (error) { if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw error }
      if (existing) {
        if (existing.fingerprint !== fingerprint) throw new Error('requestId already belongs to a different request')
        return { ...this.currentReceipt(existing), duplicate: true }
      }
      const receipt: RunReceipt = { requestId, runId: randomUUID(), attemptId: randomUUID(), fingerprint, state: 'accepted', ownerInstanceId: this.instanceId }
      await this.write(path, receipt)
      // If the process dies between this durable receipt and execution, a retry
      // reports accepted/unknown. It must never silently run the payload again.
      const started = start(receipt)
      receipt.state = started ? 'running' : 'rejected'
      await this.write(path, receipt)
      return { ...receipt, duplicate: false }
    })
    this.locks.set(key, operation)
    try { return await operation }
    finally { if (this.locks.get(key) === operation) this.locks.delete(key) }
  }

  private currentReceipt(receipt: RunReceipt): RunReceipt {
    return receipt.ownerInstanceId !== this.instanceId && ['accepted', 'running'].includes(receipt.state)
      ? { ...receipt, state: 'needs_attention' } : receipt
  }

  async get(sessionId: string, requestId: string): Promise<RunReceipt | undefined> {
    const key = createHash('sha256').update(`${sessionId}\0${requestId}`).digest('hex')
    await this.locks.get(key)?.catch(() => {})
    try { return this.currentReceipt(JSON.parse(await readFile(join(this.root, `${key}.json`), 'utf8')) as RunReceipt) }
    catch (error) { if ((error as NodeJS.ErrnoException).code === 'ENOENT') return undefined; throw error }
  }

  async settle(sessionId: string, requestId: string, outcome: string): Promise<void> {
    const key = createHash('sha256').update(`${sessionId}\0${requestId}`).digest('hex')
    const previous = this.locks.get(key) ?? Promise.resolve()
    const operation = previous.catch(() => {}).then(async () => {
      const path = join(this.root, `${key}.json`)
      const receipt = JSON.parse(await readFile(path, 'utf8')) as RunReceipt
      await this.write(path, { ...receipt, state: 'settled', outcome })
    })
    this.locks.set(key, operation)
    try { await operation } finally { if (this.locks.get(key) === operation) this.locks.delete(key) }
  }

  private async write(path: string, receipt: RunReceipt): Promise<void> {
    await mkdir(this.root, { recursive: true })
    const temporary = `${path}.${randomUUID()}.tmp`
    const file = await open(temporary, 'wx', 0o600)
    try { await file.writeFile(JSON.stringify(receipt)); await file.sync() }
    finally { await file.close() }
    await rename(temporary, path)
  }
}
