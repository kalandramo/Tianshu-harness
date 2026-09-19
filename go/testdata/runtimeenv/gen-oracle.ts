/**
 * detectRuntimeEnvBlock oracle 生成器。
 *
 * 生成：
 *   npx tsx go/testdata/runtimeenv/gen-oracle.ts
 *
 * 覆盖 src/prompt/runtime-env.ts 的 detectRuntimeEnvBlock（184 行）。
 *
 * ## 为什么能把 IO 探测对账
 *
 * 该函数有**两个可注入依赖**（TS 侧的测试正是这么用的）：
 *   - `probe(command, args)` —— 版本命令的执行者（生产是真 spawn，测试注入 fake）
 *   - 文件读取 —— 读 fixture 目录
 *
 * 故 oracle 把两者都**参数化**为输入：
 *   - `files`：fixture 目录的文件内容（path → content）
 *   - `probe`：command → 输出（null 表示命令失败/不存在）
 * 生成器在临时目录里真实写入这些文件，调用真实 detectRuntimeEnvBlock，
 * 记录输出。Go 侧用同样的 files/probe 注入自己的实现，对账字节。
 *
 * 这样避免了对账依赖宿主真实环境（python3 版本、node 版本会变）。
 */
import { writeFileSync, mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import { createHash } from 'node:crypto'
import { detectRuntimeEnvBlock, __resetRuntimeEnvCache, type VersionProbe } from '../../../src/prompt/runtime-env.js'

const here = dirname(fileURLToPath(import.meta.url))

type Case = {
  name: string
  note?: string
  files: Record<string, string>
  /** command → 输出；缺失表示该命令返回 null（不存在/失败）。 */
  probe?: Record<string, string>
}

const cases: Case[] = [
  {
    name: 'emptyProject',
    note: '空目录——两个探测都不成立，返回 null',
    files: {},
  },
  {
    name: 'pythonVersionPinned',
    note: '.python-version 钉版本 + probed 解释器版本（取 probed）',
    files: { '.python-version': '3.6.9\n', 'setup.py': '' },
    probe: { python3: 'Python 3.6.9' },
  },
  {
    name: 'pythonDeclaredOnlySetupPy',
    note: '无 .python-version，从 setup.py 的 python_requires 取 declared',
    files: { 'setup.py': "setup(python_requires='>=3.5')" },
  },
  {
    name: 'pythonDeclaredOnlyPyproject',
    note: '从 pyproject.toml 的 requires-python 取 declared',
    files: { 'pyproject.toml': 'requires-python = ">=3.11"\n' },
  },
  {
    name: 'pythonDeclaredOnlySetupCfg',
    note: '从 setup.cfg 取 declared（第三种来源）',
    files: { 'setup.cfg': 'python_requires = >=3.8\n' },
  },
  {
    name: 'pythonMarkerOnlyNoDeclared',
    note: '有 marker（requirements.txt）但无版本声明、无 probe → null',
    files: { 'requirements.txt': 'requests\n' },
  },
  {
    name: 'pythonFallbackToPlainPython',
    note: 'python3 探测失败，回退 python（第二条 probe 路径）',
    files: { '.python-version': '3.10.0\n' },
    probe: { python: 'Python 3.10.2' },
  },
  {
    name: 'pythonDated',
    note: 'python 3.6 < 3.9 → 触发 isDated 的 caution 文案',
    files: { '.python-version': '3.6.9\n' },
  },
  {
    name: 'nodeEngines',
    note: 'package.json engines.node + probed node 版本',
    files: { 'package.json': JSON.stringify({ engines: { node: '>=18' } }) },
    probe: { node: 'v20.11.0' },
  },
  {
    name: 'nodeNvmrc',
    note: '无 engines，从 .nvmrc 取 declared',
    files: { '.nvmrc': '18.20.0\n' },
  },
  {
    name: 'nodeMalformedPackageJson',
    note: 'package.json 非法 JSON → 忽略 engines；无 .nvmrc → 无 declared',
    files: { 'package.json': '{ this is not json' },
  },
  {
    name: 'nodeDated',
    note: 'node 16 < 18 → 触发 caution',
    files: { 'package.json': JSON.stringify({ engines: { node: '16' } }) },
    probe: { node: 'v16.20.0' },
  },
  {
    name: 'rustToolchainToml',
    note: 'rust-toolchain.toml 的 channel',
    files: { 'rust-toolchain.toml': '[toolchain]\nchannel = "1.75.0"\n' },
  },
  {
    name: 'rustToolchainPlain',
    note: '无 channel 键 → 取首行作 declared（rust-toolchain 裸文件）',
    files: { 'rust-toolchain': 'nightly-2024-01-01\n' },
  },
  {
    name: 'goMod',
    note: 'go.mod 的 go 指令',
    files: { 'go.mod': 'module x\n\ngo 1.27\n' },
  },
  {
    name: 'goModNoVersion',
    note: 'go.mod 无 go 指令 → 该行不产生',
    files: { 'go.mod': 'module x\n' },
  },
  {
    name: 'allFour',
    note: '四种运行时齐上——验证顺序 python→node→rust→go 与拼接',
    files: {
      '.python-version': '3.12.0\n',
      'package.json': JSON.stringify({ engines: { node: '>=20' } }),
      'rust-toolchain.toml': '[toolchain]\nchannel = "1.78.0"\n',
      'go.mod': 'module x\n\ngo 1.22.0\n',
    },
    probe: { python3: 'Python 3.12.1', node: 'v20.11.0' },
  },
  {
    name: 'twoDated',
    note: 'python 与 node 都 dated → caution 列出两个名字（用 / 连接）',
    files: {
      '.python-version': '3.8.0\n',
      'package.json': JSON.stringify({ engines: { node: '16' } }),
    },
  },
  {
    name: 'python3AndPythonBoth',
    note: '**M4 区分点**：python3 与 python 都有响应 → python3 优先（顺序敏感）',
    files: { '.python-version': '3.10.0\n' },
    probe: { python3: 'Python 3.12.1', python: 'Python 3.10.5' },
  },
  {
    name: 'markerWithDeclaredNoPinned',
    note: '**M7 区分点**：有 marker（setup.py）+ 有版本声明，但无 .python-version。'
      + '若忽略 marker 判定会漏掉整条 python 行',
    files: { 'setup.py': "setup(python_requires='>=3.9')" },
  },
  {
    name: 'declaredWithActualMixed',
    note: '实际版本 + 声明版本并存——renderLine 的两种形态',
    files: {
      '.python-version': '>=3.10\n',
      'package.json': JSON.stringify({ engines: { node: '>=18' } }),
    },
    probe: { python3: 'Python 3.11.5', node: 'v18.19.0' },
  },
]

const dirs: string[] = []
const results: Record<string, unknown> = {}

try {
  for (const c of cases) {
    __resetRuntimeEnvCache()
    const dir = mkdtempSync(join(tmpdir(), 'runtime-env-oracle-'))
    dirs.push(dir)
    for (const [name, content] of Object.entries(c.files)) {
      writeFileSync(join(dir, name), content)
    }
    const probe: VersionProbe = (cmd) => c.probe?.[cmd] ?? null
    const out = detectRuntimeEnvBlock(dir, probe)
    results[c.name] = { note: c.note, files: c.files, probe: c.probe ?? {}, out }
  }
} finally {
  for (const d of dirs) rmSync(d, { recursive: true, force: true })
}

const out = {
  _note: 'files/probe 是输入；out 是 TS 真实 detectRuntimeEnvBlock 的产出',
  cases: results,
}

mkdirSync(here, { recursive: true })
writeFileSync(join(here, 'oracle.json'), JSON.stringify(out, null, 2) + '\n')

const sha = createHash('sha256').update(JSON.stringify(out)).digest('hex')
const nonNull = Object.values(results).filter((r) => (r as { out: unknown }).out != null).length
console.error(
  `runtimeenv oracle：${cases.length} 用例（${nonNull} 个非 null 输出）— sha256 ${sha.slice(0, 16)}`,
)
