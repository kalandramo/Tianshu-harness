/**
 * selfKill 判定的差分 oracle。
 *
 * 生成：npx tsx go/testdata/selfkill/gen-oracle.ts
 *
 * 导出 TS `src/agent/self-preservation.ts` 的 `isSelfDestructiveKill` 在
 * 受控输入上的真实输出，供 Go 侧逐值对账。
 *
 * # 为什么语料分两组（关键）
 *
 * TS 的 `isSelfDestructiveKill` 挡**两类**命令：
 *   A. 镜像名类（`pkill node` / `taskkill /IM node.exe`）——硬编码杀 node 进程
 *   B. PID 类（`kill <自身/祖先 pid>` / `taskkill /PID <n>`）——语言无关
 *
 * Go 侧**只移植 B**（A 的对应物是 `pkill tianshu`，但用户无该习惯性动机——
 * 收益低）。故语料分两组：
 *   - `pidCases`：Go 侧逐值对账
 *   - `imageCases`：Go 侧**有意返回 false**，测试显式断言「Go=false 而 TS=true」
 *     —— 把有意差异钉住，防将来有人"顺手补齐"时不知这是决策
 *
 * 若不分组，含镜像名的语料会让对账永远红，且掩盖真正要验的 PID 语义。
 *
 * # 固定进程树
 *
 * 传**确定值**的 tree（而非 `selfProcessTree()` 的运行时值），否则 oracle
 * 不可重现（PID 每次都变）。
 */
import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createHash } from 'node:crypto'
import { isSelfDestructiveKill } from '../../../src/agent/self-preservation.js'

const here = dirname(fileURLToPath(import.meta.url))

// 固定进程树：self=1000，祖先=[999, 998]。确定值 → 可重现。
const TREE = { selfPid: 1000, ancestorPids: [999, 998] }

// ── A 组：PID 类（Go 侧逐值对账）──
const pidCases: Array<{ label: string; cmd: string }> = [
  // Unix `kill <pid>`
  { label: 'kill-self', cmd: 'kill 1000' },
  { label: 'kill-ancestor1', cmd: 'kill 999' },
  { label: 'kill-ancestor2', cmd: 'kill 998' },
  { label: 'kill-with-sig', cmd: 'kill -9 1000' },
  { label: 'kill-with-sigterm', cmd: 'kill -TERM 1000' },
  { label: 'kill-unrelated', cmd: 'kill 12345' },
  { label: 'kill-unrelated-sig', cmd: 'kill -9 12345' },
  { label: 'kill-multi-first-match', cmd: 'kill 12345 1000' },
  { label: 'kill-multi-last-match', cmd: 'kill 1000 12345' },
  { label: 'kill-multi-none', cmd: 'kill 111 222 333' },
  { label: 'kill-sig-then-self', cmd: 'kill -15 -9 1000' },
  { label: 'kill-noninteger', cmd: 'kill 1000.5' },
  { label: 'kill-nonnumeric', cmd: 'kill abc' },
  { label: 'kill-mixed-token', cmd: 'kill 1000abc' },
  { label: 'kill-zero', cmd: 'kill 0' },
  { label: 'kill-negative-group', cmd: 'kill -9 -1000' },
  { label: 'kill-no-args', cmd: 'kill' },
  { label: 'kill-leading-space', cmd: '   kill 1000' },
  // 关键反例：tokens[0] 必须**严格等于** kill
  { label: 'not-kill-prefixed', cmd: 'killall 1000' },
  { label: 'not-kill-suffixed', cmd: 'killer 1000' },
  { label: 'not-kill-in-arg', cmd: 'echo kill 1000' },
  { label: 'not-kill-in-arg2', cmd: 'grep kill 1000' },
  // Windows `taskkill /PID <n>`
  { label: 'taskkill-pid-self', cmd: 'taskkill /PID 1000' },
  { label: 'taskkill-pid-ancestor', cmd: 'taskkill /PID 999' },
  { label: 'taskkill-double-slash', cmd: 'taskkill //PID 1000' },
  { label: 'taskkill-f-flag', cmd: 'taskkill /F /PID 1000' },
  { label: 'taskkill-quoted-pid', cmd: 'taskkill /PID "1000"' },
  { label: 'taskkill-unrelated', cmd: 'taskkill /PID 12345' },
  { label: 'taskkill-im-only', cmd: 'taskkill /IM notepad.exe' },
  // 复合：藏在执行符后
  { label: 'semi-hidden', cmd: 'echo hi; kill 1000' },
  { label: 'and-hidden', cmd: 'ls && kill 1000' },
  { label: 'or-hidden', cmd: 'ls || kill 1000' },
  { label: 'pipe-hidden', cmd: 'cat f | kill 1000' },
  { label: 'newline-hidden', cmd: 'ls\nkill 1000' },
  { label: 'cmdsubst-hidden', cmd: 'echo $(kill 1000)' },
  { label: 'backtick-hidden', cmd: 'echo `kill 1000`' },
  { label: 'chain-second-unrelated', cmd: 'ls && kill 12345' },
  // 无害（反假阳性基线）
  { label: 'benign-ls', cmd: 'ls -la' },
  { label: 'benign-echo-num', cmd: 'echo 1000' },
  { label: 'benign-npx-killport', cmd: 'npx kill-port 3000' },
  { label: 'benign-git', cmd: 'git status' },
  { label: 'empty', cmd: '' },
  { label: 'only-spaces', cmd: '   ' },
]

// ── B 组：镜像名类（Go 侧**有意不移植**）──
//
// TS 全部为 true。Go 侧应全部 false——测试显式断言这个差异。
const imageCases: Array<{ label: string; cmd: string }> = [
  { label: 'taskkill-im-node', cmd: 'taskkill /IM node.exe' },
  { label: 'taskkill-im-node-slash', cmd: 'taskkill //IM node.exe' },
  { label: 'taskkill-im-node-quoted', cmd: 'taskkill /IM "node.exe"' },
  { label: 'pkill-node', cmd: 'pkill node' },
  { label: 'pkill-f-node', cmd: 'pkill -f node' },
  { label: 'killall-node', cmd: 'killall node' },
  { label: 'wmic-node-delete', cmd: 'wmic process where name="node.exe" delete' },
  // 复合
  { label: 'semi-hidden-pkill', cmd: 'echo hi; pkill node' },
]

// ── C 组：守卫判别性语料（**树含 0/负值**——现实中不可能，但能判别守卫）──
//
// **为什么需要**：`n > 0` 守卫在真实树（PID 恒 > 0，内核保证）下**永不可判别**
// ——`kill 0` 的 `0` 不在正常树里，去掉守卫仍返回 false（变异反证 M1 红 0
// 暴露了这点）。但守卫对账 TS 的 `Number()` 语义（`1000.5`→NaN、`0`→非正），
// 是**语义保真**的一部分，不该因「真实场景不触发」就留着不测。
//
// 故造一组「树里含 0 与负值」的输入：TS 侧靠守卫挡住，Go 侧必须同样挡住。
const GUARD_TREE = { selfPid: 0, ancestorPids: [-5, 0] }

const guardCases: Array<{ label: string; cmd: string }> = [
  { label: 'guard-kill-zero', cmd: 'kill 0' },
  { label: 'guard-kill-negative', cmd: 'kill -5' },
  { label: 'guard-kill-zero-and-negative', cmd: 'kill 0 -5' },
  { label: 'guard-kill-mixed', cmd: 'kill 12345 0' },
]

const out: Record<string, unknown> = {
  // 固定树一并导出——Go 侧用同值对账，避免「两边各用运行时 PID」导致不可比
  tree: TREE,
  pidCases: pidCases.map(({ label, cmd }) => ({
    label,
    cmd,
    result: isSelfDestructiveKill(cmd, TREE),
  })),
  imageCases: imageCases.map(({ label, cmd }) => ({
    label,
    cmd,
    result: isSelfDestructiveKill(cmd, TREE),
  })),
  // C 组：守卫判别性（树含 0/负值）
  guardTree: GUARD_TREE,
  guardCases: guardCases.map(({ label, cmd }) => ({
    label,
    cmd,
    result: isSelfDestructiveKill(cmd, GUARD_TREE),
  })),
}

mkdirSync(here, { recursive: true })
writeFileSync(join(here, 'oracle.json'), JSON.stringify(out, null, 2) + '\n')
const sha = createHash('sha256').update(JSON.stringify(out)).digest('hex')
console.error(
  `selfkill oracle：PID 类 ${pidCases.length} 条、镜像名类 ${imageCases.length} 条、` +
    `守卫类 ${guardCases.length} 条 — sha256 ${sha.slice(0, 16)}`,
)
