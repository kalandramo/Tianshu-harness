import { test } from 'node:test'
import assert from 'node:assert/strict'
import { persistStrippedImagesIfUnambiguous } from '../persisted-image-strip.js'
import { SessionContext } from '../context.js'
import { STRIPPED_IMAGE_PLACEHOLDER, type OaiMessage } from '../../api/oai-types.js'

/**
 * 持久化剥图反证表（Grok Build image_strip.rs 同款纪律）：
 *   #1「多图 blame 不明也写历史」 → uniqueUrlCount!==1 必须 no-op（wire-only）
 *   #2「唯一 blame 不写」        → uniqueUrlCount===1 必须替换 image part 并 replaceMessages
 *   #3「直接删 part」            → 必须留 STRIPPED_IMAGE_PLACEHOLDER，与 wire 剥图逐字节相同
 *   #4「无图也重写 transcript」  → 没图可剥不得触发 replace（白写盘 + 碎前缀）
 *   #5「只改内存不落盘」         → replaceMessages 必须发 replace mutation（listener 原子重写）
 */

function userImageMsg(url: string, text = '看这张图'): OaiMessage {
  return {
    role: 'user',
    content: [
      { type: 'text', text },
      { type: 'image_url', image_url: { url } },
    ],
  }
}

function fakeSession(messages: OaiMessage[]) {
  const replacements: OaiMessage[][] = []
  return {
    session: {
      getMessages: () => messages,
      replaceMessages: (next: OaiMessage[]) => { replacements.push(next) },
    },
    replacements,
  }
}

const IMG = 'data:image/png;base64,AAAA'

test('#1 blame 不唯一（2 个不同 URL）→ 不写历史，保持 wire-only', () => {
  const msgs: OaiMessage[] = [
    { role: 'system', content: 'sys' },
    userImageMsg('data:image/png;base64,AAAA'),
    userImageMsg('data:image/png;base64,BBBB'),
  ]
  const { session, replacements } = fakeSession(msgs)
  assert.equal(persistStrippedImagesIfUnambiguous(session, { removedCount: 2, uniqueUrlCount: 2 }), false)
  assert.equal(persistStrippedImagesIfUnambiguous(session, { removedCount: 2, uniqueUrlCount: 0 }), false)
  assert.equal(persistStrippedImagesIfUnambiguous(session, { removedCount: 2 }), false, '旧调用方缺字段按不唯一处理')
  assert.equal(replacements.length, 0, 'blame 不明时一次写盘都不许发生')
})

test('#2 唯一 blame → 替换 image part 并 replaceMessages（触发 transcript 重写）', () => {
  const msgs: OaiMessage[] = [
    { role: 'system', content: 'sys' },
    userImageMsg(IMG, '看这张图'),
    { role: 'assistant', content: '看到了' },
    userImageMsg(IMG, '再看一次'),
    { role: 'user', content: '纯文本' },
  ]
  const { session, replacements } = fakeSession(msgs)
  assert.equal(persistStrippedImagesIfUnambiguous(session, { removedCount: 2, uniqueUrlCount: 1 }), true)
  assert.equal(replacements.length, 1)
  const next = replacements[0]!
  assert.equal(next.length, msgs.length, '消息数与角色结构保持')
  for (const idx of [1, 3]) {
    assert.deepEqual(next[idx]!.content, [
      { type: 'text', text: idx === 1 ? '看这张图' : '再看一次' },
      { type: 'text', text: STRIPPED_IMAGE_PLACEHOLDER },
    ], '占位符必须与 wire 剥图同一常量（下一轮续上缓存前缀）')
  }
  assert.equal(next[4]!.content, '纯文本', '无关消息原样保留')
  assert.ok(!JSON.stringify(next).includes('image_url'), '历史里不得再留 image_url')
})

test('#3 无图可剥 → no-op，不触发 replace', () => {
  const msgs: OaiMessage[] = [{ role: 'user', content: '只是文本' }]
  const { session, replacements } = fakeSession(msgs)
  assert.equal(persistStrippedImagesIfUnambiguous(session, { removedCount: 1, uniqueUrlCount: 1 }), false)
  assert.equal(replacements.length, 0)
})

test('#4 SessionContext 真身：replaceMessages 发 replace mutation（落盘链路入口）', () => {
  const session = new SessionContext()
  session.addUserMessage('看这张图', [IMG])
  session.addAssistantBlocks([{ type: 'text', text: '看到了' }])
  const mutations: Array<{ type: string; hasImage: boolean }> = []
  session.setMutationListener(m => {
    mutations.push({
      type: m.type,
      hasImage: m.type === 'replace' && JSON.stringify(m.messages).includes('image_url'),
    })
  })
  assert.equal(persistStrippedImagesIfUnambiguous(session, { removedCount: 1, uniqueUrlCount: 1 }), true)
  assert.deepEqual(mutations, [{ type: 'replace', hasImage: false }], 'replace mutation 必须已去掉图片')
  assert.ok(!JSON.stringify(session.getMessages()).includes('image_url'), '内存历史已剥离')
  assert.match(JSON.stringify(session.getMessages()), /no longer visible/, '占位符留在历史里供模型理解')
})

test('#5 同一 URL 多副本（同一张图贴两次）→ 一次写回全部替换', () => {
  const msgs: OaiMessage[] = [userImageMsg(IMG, 'first'), userImageMsg(IMG, 'second')]
  const { session, replacements } = fakeSession(msgs)
  assert.equal(persistStrippedImagesIfUnambiguous(session, { removedCount: 2, uniqueUrlCount: 1 }), true)
  assert.equal(replacements.length, 1)
  assert.ok(!JSON.stringify(replacements[0]).includes('image_url'))
})
