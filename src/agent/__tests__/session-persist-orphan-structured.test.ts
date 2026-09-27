/**
 * PLAN §4 恢复设计第 3 步——孤儿工具的「结构化注入」（灰度，默认关）。
 *
 * 默认（flag 关）：保持既有行为，静默剔除孤儿 tool_call + 通用 reminder。
 * 开启（RIVET_RECOVERY_STRUCTURED_TOOLS=1）：保留 tool_call 并注入一条明确的
 * 「结果未知」tool 消息——写工具非破坏性措辞（先核实再补写），只读工具可安全重跑。
 */
import { test, beforeEach, afterEach } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { SessionPersist } from '../session-persist.js'
import { isAssistantWithTools, type OaiMessage } from '../../api/oai-types.js'

let dir: string
beforeEach(() => { dir = mkdtempSync(join(tmpdir(), 'rivet-orphan-')); process.env.RIVET_SESSION_DIR = dir })
afterEach(() => {
  delete process.env.RIVET_RECOVERY_STRUCTURED_TOOLS
  delete process.env.RIVET_SESSION_DIR
  rmSync(dir, { recursive: true, force: true })
})

const assistantWith = (id: string, name: string): OaiMessage => ({
  role: 'assistant',
  content: '',
  tool_calls: [{ id, type: 'function', function: { name, arguments: '{}' } }],
})

async function seedOracle(messages: OaiMessage[]): Promise<SessionPersist> {
  const p = new SessionPersist('s-orphan', dir)
  for (const m of messages) await p.appendOaiWithChecksum(m, { flush: true })
  return p
}

test('默认（flag 关）：孤儿 tool_call 被剔除，不注入合成结果', async () => {
  delete process.env.RIVET_RECOVERY_STRUCTURED_TOOLS
  const persist = await seedOracle([assistantWith('c1', 'write_file')])
  const msgs = persist.loadOai()
  assert.equal(msgs.some((m) => isAssistantWithTools(m)), false, '孤儿 tool_call 应被剔除')
  assert.equal(msgs.some((m) => m.role === 'tool'), false)
})

test('flag 开：写工具孤儿注入非破坏性的「结果未知」tool 消息', async () => {
  process.env.RIVET_RECOVERY_STRUCTURED_TOOLS = '1'
  const persist = await seedOracle([assistantWith('c1', 'write_file')])
  const msgs = persist.loadOai()

  const assistant = msgs.find(isAssistantWithTools)
  assert.ok(assistant, 'tool_call 必须保留')
  assert.equal(assistant.tool_calls.length, 1)
  assert.equal(assistant.tool_calls[0]!.id, 'c1')

  const tool = msgs.find((m) => m.role === 'tool')
  assert.ok(tool && 'tool_call_id' in tool && tool.tool_call_id === 'c1', '注入结果必须与该 tool_call 配对')
  const content = String(tool.content)
  assert.match(content, /结果未知/)
  assert.match(content, /不要自动重放/)
  assert.match(content, /read_file/, '写工具必须先核实')
  assert.equal(msgs.some((m) => m.role === 'system'), false, '结构化模式不再注入通用 reminder')
})

test('flag 开：只读工具孤儿用「可安全重跑」措辞', async () => {
  process.env.RIVET_RECOVERY_STRUCTURED_TOOLS = '1'
  const persist = await seedOracle([assistantWith('c2', 'grep')])
  const tool = persist.loadOai().find((m) => m.role === 'tool')
  assert.ok(tool)
  assert.match(String(tool.content), /安全重跑/)
})

test('config 选项（agent.recovery.structuredTools）无需 env 也生效', async () => {
  delete process.env.RIVET_RECOVERY_STRUCTURED_TOOLS
  const persist = new SessionPersist('s-cfg', dir, { recoveryStructuredTools: true })
  await persist.appendOaiWithChecksum(assistantWith('c9', 'write_file'), { flush: true })
  const msgs = persist.loadOai()
  assert.ok(msgs.some((m) => m.role === 'tool'), 'config 开启也应注入「结果未知」tool 消息')
  assert.equal(persist.getLastInjectedUncertain().length, 1)
})

test('flag 开：非文件编辑但有副作用的工具不得被说成「可安全重跑」', async () => {
  process.env.RIVET_RECOVERY_STRUCTURED_TOOLS = '1'
  // 反向枚举（「哪些工具会写文件」）的漏判：下面这些都不是文件编辑工具，却都有副作用。
  // 此前它们全部落到「这是只读 / 查询类工具，可用相同参数安全重跑」——恢复期会诱导
  // 模型重复执行外部操作（跑命令、推分支、发消息）。判据已改成只读白名单。
  const sideEffecting = ['bash', 'git', 'run_tests', 'computer_use', 'export_file', 'mcp__github__create_issue']
  for (const name of sideEffecting) {
    const p = new SessionPersist(`s-side-${name}`, dir)
    await p.appendOaiWithChecksum(assistantWith('c1', name), { flush: true })
    const tool = p.loadOai().find((m) => m.role === 'tool')
    assert.ok(tool, `${name} 应注入「结果未知」tool 消息`)
    const content = String(tool.content)
    assert.doesNotMatch(content, /安全重跑/, `${name} 有副作用，绝不能提示「可安全重跑」`)
    assert.match(content, /不要自动重放/, `${name} 必须要求先核实再决定`)
  }
})

test('flag 开：白名单内的只读工具仍保留「可安全重跑」', async () => {
  process.env.RIVET_RECOVERY_STRUCTURED_TOOLS = '1'
  for (const name of ['read_file', 'grep', 'glob', 'semantic_search', 'repo_graph', 'file_info']) {
    const p = new SessionPersist(`s-ro-${name}`, dir)
    await p.appendOaiWithChecksum(assistantWith('c1', name), { flush: true })
    const tool = p.loadOai().find((m) => m.role === 'tool')
    assert.ok(tool, `${name} 应注入「结果未知」tool 消息`)
    assert.match(String(tool.content), /安全重跑/, `${name} 是只读工具，可安全重跑`)
  }
})
