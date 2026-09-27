/**
 * image-paste.ts tests：多行图片路径粘贴的解析/加载/告警文案。
 *
 * 反证表（每条对着一个偷懒实现）：
 *   #1「多行就放弃」      → 多行路径必须全部解析出来（旧实现见 \n 就退回文本）
 *   #2「挑出路径就算」    → 混了别的话整段按文本处理，绝不吞掉其余行
 *   #3「引号/转义不认」   → 终端拖拽与 Copy as Pathname 的两种包裹都要认出
 *   #4「超上限静默丢」    → 槽位不足要报「已跳过 N 张」
 *   #5「一张失败全盘作废」→ 部分成功照样挂上，只有全员失败才回退文本
 */

import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  formatImagePasteNotices,
  loadPastedImages,
  parseImagePathPaste,
} from '../image-paste.js'

// ── parseImagePathPaste ──────────────────────────────────────────────────────

test('#1 单行路径解析为一条；多行路径全部解析（这是本模块存在的理由）', () => {
  assert.deepEqual(parseImagePathPaste('/tmp/shot.png'), ['/tmp/shot.png'])
  assert.deepEqual(
    parseImagePathPaste('/tmp/a.png\n/tmp/b.jpg\r\n/tmp/c.webp'),
    ['/tmp/a.png', '/tmp/b.jpg', '/tmp/c.webp'],
  )
})

test('#1b 空行/尾随换行不产生空路径，也不影响判定', () => {
  assert.deepEqual(parseImagePathPaste('\n\n/tmp/a.png\n\n'), ['/tmp/a.png'])
})

test('#2 混入普通文本 → null（整段按文本粘贴，绝不只挑路径）', () => {
  assert.equal(parseImagePathPaste('看这张\n/tmp/a.png'), null)
  assert.equal(parseImagePathPaste('/tmp/a.png\n还有这张'), null)
  assert.equal(parseImagePathPaste('随便一段话'), null)
  assert.equal(parseImagePathPaste(''), null)
  assert.equal(parseImagePathPaste('\n  \n'), null, '只有空白也按文本处理')
})

test('#2b 非图片扩展名 → null（.txt/.pdf 不该被当图去加载）', () => {
  assert.equal(parseImagePathPaste('/tmp/a.txt'), null)
  assert.equal(parseImagePathPaste('/tmp/a.png\n/tmp/b.txt'), null)
  assert.equal(parseImagePathPaste('/tmp/a'), null)
})

test('#3 成对引号与反斜杠转义空格都还原（终端拖拽/复制的两种形态）', () => {
  assert.deepEqual(parseImagePathPaste('"/tmp/My Shot.png"'), ['/tmp/My Shot.png'])
  assert.deepEqual(parseImagePathPaste("'/tmp/My Shot.png'"), ['/tmp/My Shot.png'])
  assert.deepEqual(parseImagePathPaste('/tmp/My\\ Shot.png'), ['/tmp/My Shot.png'])
  // 行首行尾空白（复制一列路径时常见）
  assert.deepEqual(parseImagePathPaste('  /tmp/a.png  '), ['/tmp/a.png'])
})

// ── loadPastedImages ─────────────────────────────────────────────────────────

const okLoader = async (path: string) => `data:image/png;base64,${path}`

test('按粘贴顺序加载，槽位足够时全挂上', async () => {
  const out = await loadPastedImages(['/a.png', '/b.png'], { slots: 4, load: okLoader })
  assert.deepEqual(out.dataUrls, ['data:image/png;base64,/a.png', 'data:image/png;base64,/b.png'])
  assert.equal(out.skipped, 0)
  assert.deepEqual(out.failures, [])
})

test('#4 槽位不足：加载到满，其余计入 skipped（不得静默丢弃）', async () => {
  const out = await loadPastedImages(['/a.png', '/b.png', '/c.png'], { slots: 2, load: okLoader })
  assert.equal(out.dataUrls.length, 2)
  assert.equal(out.skipped, 1)
})

test('槽位为 0（已满 4 张）：一条都不加载，全部计入 skipped', async () => {
  const out = await loadPastedImages(['/a.png', '/b.png'], { slots: 0, load: okLoader })
  assert.deepEqual(out.dataUrls, [])
  assert.equal(out.skipped, 2)
})

test('#5 单张失败不影响其余（顺序保持，失败项带原因）', async () => {
  const loader = async (path: string) => {
    if (path === '/bad.png') throw new Error('ENOENT: no such file')
    return `data:image/png;base64,${path}`
  }
  const out = await loadPastedImages(['/a.png', '/bad.png', '/c.png'], { slots: 4, load: loader })
  assert.deepEqual(out.dataUrls, ['data:image/png;base64,/a.png', 'data:image/png;base64,/c.png'])
  assert.equal(out.failures.length, 1)
  assert.equal(out.failures[0]!.path, '/bad.png')
  assert.match(out.failures[0]!.message, /ENOENT/)
})

test('非 Error 抛出也要有可读原因（不能变成 [object Object] 之外的空白）', async () => {
  const out = await loadPastedImages(['/a.png'], { slots: 1, load: async () => { throw 'boom' } })
  assert.equal(out.failures[0]!.message, 'boom')
})

// ── formatImagePasteNotices ──────────────────────────────────────────────────

test('无异常时不产生告警（正常粘贴不该刷状态区）', () => {
  assert.deepEqual(formatImagePasteNotices({ dataUrls: ['x'], skipped: 0, failures: [] }, 4), [])
})

test('单张失败沿用旧文案；多张失败点名文件（只列前 3 个）', () => {
  assert.deepEqual(
    formatImagePasteNotices({ dataUrls: [], skipped: 0, failures: [{ path: '/a.png', message: 'boom' }] }, 4),
    ['⚠ 图片加载失败: boom'],
  )
  const many = ['/a.png', '/b.png', '/c.png', '/d.png'].map((path) => ({ path, message: 'boom' }))
  const [line] = formatImagePasteNotices({ dataUrls: [], skipped: 0, failures: many }, 4)
  assert.ok(line?.includes('4 张图片加载失败'))
  assert.ok(line?.includes('a.png、b.png、c.png'))
  assert.ok(line?.includes('等 4 个'))
})

test('槽位不足时给出「已跳过」告警（与失败告警可同时出现）', () => {
  const notices = formatImagePasteNotices(
    { dataUrls: ['x'], skipped: 2, failures: [{ path: '/a.png', message: 'boom' }] },
    4,
  )
  assert.equal(notices.length, 2)
  assert.ok(notices[0]?.includes('最多附加 4 张图片'))
  assert.ok(notices[0]?.includes('已跳过 2 张'))
})
