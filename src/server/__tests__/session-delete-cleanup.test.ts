/**
 * 会话硬删的落盘清理回归（2026-09-16 审查修复）。
 *
 * 事实（修复前，探针实测）：deleteSession → hardDelete 只清 events 子目录与
 * 内存 record——`<id>.jsonl` / `<id>.meta.json` 等残留；SessionPersist
 * .listSessionsWithMetadata 即使在缓存全量重建后仍列出已删会话——不是 60s
 * 窗口，是持久残留，只能等 LRU evict 兜底（MAX_SESSIONS=50）。
 *
 * 本档要求：硬删清空会话全部落盘文件（与 evictOldSessionsInternal 同清理面）
 * 并移除列表缓存条目；不误伤其他会话。
 */
import { describe, it, before, after } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, rmSync, writeFileSync, existsSync, mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { RuntimeSessionManager } from '../session-manager.js'
import { FileSessionPersistence } from '../session-persistence.js'
import { SessionPersist, getSessionDir } from '../../agent/session-persist.js'

/** 模拟真实 agent 写过的落盘产物（6 类：5 个附属文件 + 同名子目录）。 */
function seedSessionFiles(sdir: string, id: string): void {
  writeFileSync(join(sdir, `${id}.jsonl`), '{"role":"user"}\n')
  writeFileSync(join(sdir, `${id}.meta.json`), JSON.stringify({ sessionId: id, updatedAt: Date.now() }))
  writeFileSync(join(sdir, `${id}.memory.json`), '{}')
  writeFileSync(join(sdir, `${id}.claims.jsonl`), '')
  writeFileSync(join(sdir, `${id}.frozen.json`), '{}')
  mkdirSync(join(sdir, id), { recursive: true })
  writeFileSync(join(sdir, id, 'events.jsonl'), '')
}

/** 返回仍存在于磁盘的落盘类别（空数组 = 清空）。 */
function leftoverKinds(sdir: string, id: string): string[] {
  return ['jsonl', 'meta.json', 'memory.json', 'claims.jsonl', 'frozen.json', 'dir']
    .filter((kind) => existsSync(kind === 'dir' ? join(sdir, id) : join(sdir, `${id}.${kind}`)))
}

describe('session delete disk cleanup', () => {
  let dataRoot: string
  let cwd: string
  let sdir: string
  let manager: RuntimeSessionManager

  before(() => {
    dataRoot = mkdtempSync(join(tmpdir(), 'delclean-'))
    cwd = mkdtempSync(join(tmpdir(), 'delclean-cwd-'))
    process.env.RIVET_SESSION_DIR = dataRoot
    sdir = getSessionDir(cwd)
    mkdirSync(sdir, { recursive: true })
    manager = new RuntimeSessionManager({
      defaultCwd: cwd,
      // 本套用例不构建 agent（归档/硬删走 agent=null 轻量路径）
      createAgent: () => { throw new Error('this test must not build a real agent') },
      persistence: new FileSessionPersistence(sdir),
    })
  })

  after(async () => {
    // 等会话清理链落定再删目录——否则后台写在 rmSync 期间仍可能落盘，命中
    // ENOTEMPTY（并行负载下必现；Windows 上 EBUSY/ENOTEMPTY 更常见）。
    await manager.shutdownAll()
    delete process.env.RIVET_SESSION_DIR
    SessionPersist.invalidateListCache()
    const rmOpts = { recursive: true, force: true, maxRetries: 10, retryDelay: 20 } as const
    rmSync(dataRoot, rmOpts)
    rmSync(cwd, rmOpts)
  })

  it('hardDelete 清空会话全部落盘文件且列表不再列出', () => {
    const rec = manager.createSession({ cwd, title: 'del-clean' })
    seedSessionFiles(sdir, rec.id)
    assert.equal(leftoverKinds(sdir, rec.id).length, 6, '播种失效：应存在 6 类落盘')

    SessionPersist.invalidateListCache()
    assert.ok(SessionPersist.listSessionsWithMetadata(cwd).some((s) => s.id === rec.id), '删除前应可见')

    assert.ok(manager.archiveSession(rec.id))
    const del = manager.deleteSession(rec.id)
    assert.ok(del.ok, '归档会话应可硬删')

    assert.deepEqual(leftoverKinds(sdir, rec.id), [], '硬删后不得有落盘残留')
    // 缓存条目已移除——全量重建同样不该列出（修复前此处红）
    SessionPersist.invalidateListCache()
    assert.ok(
      !SessionPersist.listSessionsWithMetadata(cwd).some((s) => s.id === rec.id),
      '已删会话不得复活',
    )
  })

  it('不误伤其他会话的文件', () => {
    const keep = manager.createSession({ cwd, title: 'del-keep' })
    const gone = manager.createSession({ cwd, title: 'del-gone' })
    seedSessionFiles(sdir, keep.id)
    seedSessionFiles(sdir, gone.id)

    assert.ok(manager.archiveSession(gone.id))
    assert.ok(manager.deleteSession(gone.id).ok)

    assert.equal(leftoverKinds(sdir, gone.id).length, 0, '目标会话应清空')
    assert.equal(leftoverKinds(sdir, keep.id).length, 6, '他会话文件不得被误伤')
  })
})
