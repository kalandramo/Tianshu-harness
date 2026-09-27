/**
 * dispose-community-pr.sh 的 credit 查重/自检：**先把 subject 收进变量，再 grep**，
 * 不得写成 `git log --format='%s' | grep -qF …`。
 *
 * ## 事故（2026-09-27，处置公开仓 PR #281）
 *
 * 脚本开了 `set -o pipefail`，而 `grep -q` 命中即退出——上游 `git log` 还在往管道里写就被
 * SIGPIPE 打死，管道返回 141（128+13）。于是同一形态在两处各咬一次：
 *
 *   ① **自检假阴性**：credit 提交**已经**落账，`if ! … | grep -q` 仍判「未落账」，
 *      脚本在刚创建完提交之后 exit 1（本次实跑就是这个形态，账是好的、报错是假的）；
 *   ② **查重反向漏判**：「已存在则跳过」的分支永远进不去 → 重复处置同一 PR 会重复落
 *      一笔 credit。公开仓现有 143 笔 credit 无重复，说明还没人连跑两次踩到，但闸门是坏的。
 *
 * 实测对照（401 笔提交、`git log --format='%s'` 输出 125,121 字节的合成仓，远超 64 KiB
 * 管道缓冲）：
 *
 * ```
 * set -o pipefail; git log --format='%s' | grep -qF "…"   → exit=141（假阴性）
 * subjects="$(git log --format='%s')"; grep -qF … <<<"$subjects" → exit=0
 * ```
 *
 * 命中的那笔是**最新**提交，也就是最坏形态：grep 一读到就退出，git 还有 125 KB 没写完。
 * 输出小于管道缓冲时（如 gh label list）上游能一次写完、不必然触发——所以纪律按形态统一锁，
 * 不靠「这个命令的输出一般不大」这种推断。
 */

import { test, describe } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync, mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import { execFileSync, spawnSync } from 'node:child_process'

const repoRoot = join(dirname(fileURLToPath(import.meta.url)), '..', '..')
const SCRIPT = join(repoRoot, 'scripts', 'dispose-community-pr.sh')

/** 行尾反斜杠续行先接起来，免得把跨行的管道形态漏掉。 */
function shellLines(text: string): string[] {
  return text
    .replace(/\\\n\s*/g, ' ')
    .split('\n')
    .filter(line => !/^\s*#/.test(line)) // 注释里要引用这个反面形态当例子
}

/** 把脚本里真实的 has_credit_commit 定义抽出来跑——测的是出货代码，不是它的复制品。 */
function loadCreditHelper(): string {
  const m = /has_credit_commit\(\) \{[\s\S]*?\n\}/.exec(readFileSync(SCRIPT, 'utf8'))
  assert.ok(m, 'dispose-community-pr.sh 里应有 has_credit_commit 定义')
  return m[0]
}

/**
 * 造一个「最新提交就是 credit，历史长到 log 输出远超管道缓冲」的仓——
 * 假阴性要在这个量级上才稳定复现。
 */
function makeLongHistoryRepo(): string {
  const dir = mkdtempSync(join(tmpdir(), 'dispose-credit-'))
  execFileSync('git', ['init', '-q', '.'], { cwd: dir })
  // HEAD 必须显式指向 fast-import 的目标分支：`git init` 的默认分支名由 runner 的
  // init.defaultBranch 决定（CI 上实测是 master），HEAD 悬在未出生的分支上时下面那句
  // `git log` 输出 0 字节 → 断言必红，且报错形如「合成仓的 log 输出应超出管道缓冲，
  // 实际 0 字节」，与真正要锁的管道假阴性混在一起分不清。2026-09-27 公开仓 CI
  // （ci 与 windows-smoke 两个 job 各 2 条）红的就是这个形态——本机绿、runner 红。
  execFileSync('git', ['symbolic-ref', 'HEAD', 'refs/heads/main'], { cwd: dir })
  const stream: string[] = []
  for (let i = 0; i < 400; i++) {
    stream.push(
      'commit refs/heads/main',
      'committer t <t@t> 1700000000 +0000',
      'data <<EOM',
      `${'x'.repeat(300)} subject-${i}`,
      'EOM',
    )
  }
  stream.push(
    'commit refs/heads/main',
    'committer t <t@t> 1700000000 +0000',
    'data <<EOM',
    'credit: PR #999 计入贡献',
    'EOM',
  )
  execFileSync('git', ['fast-import', '--quiet'], { cwd: dir, input: `${stream.join('\n')}\n` })
  const bytes = execFileSync('bash', ['-c', "git log --format='%s' | wc -c"], { cwd: dir })
    .toString()
    .trim()
  assert.ok(Number(bytes) > 64 * 1024, `合成仓的 log 输出应超出管道缓冲，实际 ${bytes} 字节`)
  return dir
}

describe('dispose-community-pr.sh 的 credit 查重形态', () => {
  test('脚本里不得出现「管道给 grep -q」——pipefail 下会变成假阴性', () => {
    const offenders = shellLines(readFileSync(SCRIPT, 'utf8')).filter(l => /\|\s*grep\s+-q/.test(l))
    assert.deepEqual(
      offenders,
      [],
      `改用「先收进变量再 grep」：${offenders.map(l => l.trim()).join(' ;; ')}`,
    )
  })

  test('真实历史量级下：管道形态假阴性，变量形态命中', t => {
    const dir = makeLongHistoryRepo()
    try {
      const probe = 'credit: PR #999 计入贡献'
      const piped = spawnSync(
        'bash',
        ['-c', `set -o pipefail; git log --format='%s' | grep -qF "${probe}"`],
        { cwd: dir },
      )
      const viaVar = spawnSync(
        'bash',
        ['-c', `subjects="$(git log --format='%s')"; grep -qF "${probe}" <<<"$subjects"`],
        { cwd: dir },
      )
      assert.equal(viaVar.status, 0, '变量形态应找到那笔 credit')
      // 「管道形态必假阴性」是**平台相关**的复现，不是产品不变量：Windows 的 Git-Bash
      // 管道语义不同，同量级下 piped.status 实测为 0（2026-09-27 windows-smoke 实跑）。
      // 真正与平台无关的守卫是上一条静态用例（脚本里不得出现管道给 grep -q）；这里在
      // Windows 上只保留「变量形态命中」这一半，并显式打出诊断——不是静默跳过。
      if (process.platform === 'win32') {
        t.diagnostic(
          `win32：跳过管道假阴性复现（piped.status=${piped.status}）——该形态在本平台不复现，` +
            '不变量由静态用例守住',
        )
        return
      }
      assert.notEqual(piped.status, 0, '管道形态在本量级下应复现假阴性（这就是要锁它的原因）')
    } finally {
      rmSync(dir, { recursive: true, force: true })
    }
  })

  test('抽出来的 has_credit_commit：命中返回 0，未命中返回非 0', () => {
    const dir = makeLongHistoryRepo()
    try {
      const helper = loadCreditHelper()
      const hit = spawnSync('bash', ['-c', `${helper}\nhas_credit_commit`], {
        cwd: dir,
        env: { ...process.env, PR: '999' },
      })
      const miss = spawnSync('bash', ['-c', `${helper}\nhas_credit_commit`], {
        cwd: dir,
        env: { ...process.env, PR: '998' },
      })
      assert.equal(hit.status, 0, '早命中的 credit 必须判为已落账')
      assert.notEqual(miss.status, 0, '不存在的 PR 必须判为未落账')
    } finally {
      rmSync(dir, { recursive: true, force: true })
    }
  })
})
