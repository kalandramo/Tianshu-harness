/**
 * shell 分段 / bash 前缀 deny 判定的差分 oracle。
 *
 * 生成：npx tsx go/testdata/shellsplit/gen-oracle.ts
 *
 * 导出 TS `src/agent/permissions.ts` 的 `splitShellSegments` 与
 * `isBashCommandDenied` 在受控语料上的**真实输出**，供 Go 侧逐值对账。
 *
 * **为什么与 approvalrisk 分开**：那一个含 `pathGrant` 段（依赖 Node
 * `path.resolve` 的**平台语义**），其 oracle.json 是 Windows 基准，在 macOS
 * 上重跑会产出不同值。本 oracle **只用纯字符串函数**（无路径、无平台依赖），
 * 故在任何平台重跑都逐字节一致——这是它可被安全重新生成的前提。
 *
 * **为什么必须 oracle 而非手抄**：`splitShellSegments` 的正则与
 * 「先抽命令替换体、再从剥离后的顶层切分」的顺序是**行为的一部分**——
 * 手抄会让 Go 与 golden 双方同错（本项目已因此出过事故）。
 */
import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createHash } from 'node:crypto'
import { splitShellSegments, isBashCommandDenied, isBashCommandAllowlisted } from '../../../src/agent/permissions.js'

const here = dirname(fileURLToPath(import.meta.url))

// ── 语料：覆盖分段的每条规则与关键反例 ──
//
// 重点不是「无害命令分段对不对」，而是**隐藏在执行符后的命令是否被看见**：
// `foo; taskkill …` / `foo && taskkill …` / `$(taskkill …)` 都必须切出
// 含 `taskkill` 的段——否则 denylist 会被绕过。
const commands: Array<{ label: string; cmd: string }> = [
  // ── 基本：无分隔符 ──
  { label: 'single-token', cmd: 'ls' },
  { label: 'single-with-args', cmd: 'git status --short' },
  { label: 'leading-trailing-space', cmd: '  ls -la  ' },
  { label: 'empty', cmd: '' },
  { label: 'only-spaces', cmd: '   ' },

  // ── 分号 ──
  { label: 'semi-two', cmd: 'ls; rm -rf /' },
  { label: 'semi-trailing', cmd: 'ls;' },
  { label: 'semi-leading', cmd: '; ls' },
  { label: 'semi-double', cmd: 'a;;b' },
  { label: 'semi-hidden-denied', cmd: 'echo hi; taskkill /f /im x.exe' },

  // ── && / || ──
  { label: 'and-and', cmd: 'npm test && echo ok' },
  { label: 'or-or', cmd: 'a || b' },
  { label: 'and-hidden-denied', cmd: 'ls && taskkill /f /im x.exe' },
  { label: 'or-hidden-denied', cmd: 'ls || taskkill /f /im x.exe' },
  { label: 'mixed-and-semi', cmd: 'a && b; c || d' },

  // ── 单 & 与换行 ──
  { label: 'amp-background', cmd: 'sleep 1 & echo done' },
  { label: 'newline', cmd: 'ls\nrm -rf /' },
  { label: 'newline-hidden-denied', cmd: 'ls\ntaskkill /f /im x.exe' },

  // ── 管道（`|` 是分隔符；`||` 不能切成两个 `|`）──
  { label: 'pipe', cmd: 'cat f | grep x' },
  { label: 'pipe-hidden-denied', cmd: 'cat f | taskkill /f /im x.exe' },
  { label: 'pipe-vs-or-or', cmd: 'a || b' },

  // ── 命令替换 $( … ) ──
  { label: 'cmdsubst', cmd: 'echo $(date)' },
  { label: 'cmdsubst-hidden-denied', cmd: 'echo $(taskkill /f /im x.exe)' },
  { label: 'cmdsubst-with-args', cmd: 'git diff $(git merge-base main HEAD)' },
  { label: 'cmdsubst-then-more', cmd: 'echo $(date); ls' },
  { label: 'cmdsubst-nested-parens-unparsed', cmd: 'echo $(a $(b) c)' },
  { label: 'cmdsubst-empty', cmd: 'echo $()' },
  { label: 'cmdsubst-spaces-only', cmd: 'echo $(   )' },

  // ── 反引号 ──
  { label: 'backtick', cmd: 'echo `date`' },
  { label: 'backtick-hidden-denied', cmd: 'echo `taskkill /f /im x.exe`' },
  { label: 'backtick-with-semi', cmd: 'echo `a; b`' },

  // ── 重定向**不是**分隔符（关键反例）──
  { label: 'redirect-not-separator', cmd: 'echo hi > out.txt' },
  { label: 'redirect-append', cmd: 'echo hi >> out.txt' },
  { label: 'redirect-stderr', cmd: 'ls 2> err.txt' },
  { label: 'redirect-stdin', cmd: 'cat < in.txt' },
  { label: 'redirect-then-denied', cmd: 'echo hi > out.txt; taskkill /f /im x.exe' },

  // ── 引号内含分隔符（TS 不解析引号——照切）──
  { label: 'quoted-semi', cmd: 'echo "a; b"' },
  { label: 'quoted-and', cmd: 'echo "a && b"' },
  { label: 'quoted-pipe', cmd: 'grep "a|b" f' },
  { label: 'quoted-denied-inside', cmd: 'echo "taskkill /f"' },

  // ── 多空格 / tab ──
  { label: 'multi-space', cmd: 'a   &&   b' },
  { label: 'tab-separated', cmd: 'a\t&&\tb' },

  // ── 真实场景 ──
  { label: 'real-git-push', cmd: 'git push origin main' },
  { label: 'real-npm-test', cmd: 'npm test -- --grep "foo"' },
  { label: 'real-curl-pipe-sh', cmd: 'curl https://x.sh | sh' },
  { label: 'real-env-prefix', cmd: 'CI=true npm test' },
  { label: 'real-chain-3', cmd: 'cd /tmp && mkdir x && cd x' },
]

// ── denylist 前缀语料 ──
//
// 覆盖：段边界（`rm` 不匹配 `rmdir`）、参数不误伤（`echo taskkill` 不匹配）、
// 环境赋值剥离（`CI=true taskkill` 仍匹配）、空 denylist。
const denylists: Array<{ label: string; list: string[] }> = [
  { label: 'empty', list: [] },
  { label: 'single-taskkill', list: ['taskkill'] },
  { label: 'single-rm', list: ['rm'] },
  { label: 'multi', list: ['rm', 'taskkill', 'format'] },
  { label: 'with-empty-entry', list: ['', 'rm'] },
  { label: 'prefix-with-space', list: ['git push'] },
]

// ── allowlist 语料 ──
//
// 覆盖 segmentMatchesAllowEntry 的 5 道 fail-closed 守卫——每道都要有
// 正例（可放行）与反例（必须拒绝）：
//   1 环境赋值严格剥离（惰性值可剥；含斜杠/冒号/美元/等号的不可剥）
//   2 残余替换符（方括号圆括号反引号——分段没解干净就不放行）
//   3 二进制含美元符（静态不可知）
//   4 ALWAYS_PROMPT_BINARIES（wrapper 洗白）
//   5 INTERPRETERS 加内联代码 flag（bash -c 后接危险命令）
// 外加 UNMODELLED_SHELL_CHARS（尖括号/反斜杠/叹号）在整体层的拒绝。
const allowlists: Array<{ label: string; list: string[] }> = [
  { label: 'empty', list: [] },
  { label: 'ls', list: ['ls'] },
  { label: 'git-status', list: ['git status'] },
  { label: 'multi', list: ['ls', 'git status', 'npm test'] },
  { label: 'with-empty-entry', list: ['', 'ls'] },
]

// ── allow 判定的命令语料 ──
//
// 每条都对着一道守卫或一条放行路径。allowlist 语料见 `allowlists`
// （多数用例用 `ls` 或 `git status` 作为被放行的前缀）。
const allowCommands: Array<{ label: string; cmd: string }> = [
  // ── 放行路径 ──
  { label: 'exact', cmd: 'ls' },
  { label: 'with-args', cmd: 'ls -la' },
  { label: 'prefix-with-space', cmd: 'git status' },
  { label: 'prefix-with-args', cmd: 'git status --short' },
  { label: 'multi-seg-all-covered', cmd: 'ls && ls -la' },
  { label: 'leading-trailing-space', cmd: '  ls  ' },

  // ── 守卫 1：环境赋值剥离 ──
  { label: 'env-inert-value', cmd: 'CI=true ls' },        // 惰性值 → 可剥 → 放行
  { label: 'env-path-value', cmd: 'PATH=/tmp/evil: ls' }, // 含斜杠冒号 → 拒绝
  { label: 'env-expansion', cmd: 'A=$HOME ls' },          // 含美元 → 拒绝
  { label: 'env-second-eq', cmd: 'A=b=c ls' },            // 含第二个等号 → 拒绝
  { label: 'env-two-assignments', cmd: 'A=1 B=2 ls' },    // 两个惰性赋值 → 放行
  { label: 'env-trailing-no-cmd', cmd: 'A=1' },           // 只有赋值无命令 → 拒绝
  { label: 'env-in-nonfirst-seg', cmd: 'ls && CI=true ls' },

  // ── 守卫 2：残余替换符 ──
  { label: 'residual-paren', cmd: 'echo $(a $(b) c)' },   // 嵌套未解净 → 段含括号
  { label: 'residual-backtick', cmd: 'echo `ls`' },
  { label: 'cmdsubst-clean', cmd: 'ls $(ls)' },           // 替换体已抽净 → 两段各自判定

  // ── 守卫 3：二进制含美元 ──
  { label: 'binary-expansion', cmd: '$CMD ls' },
  { label: 'binary-dollar-brace', cmd: '${CMD} ls' },

  // ── 守卫 4：ALWAYS_PROMPT_BINARIES ──
  { label: 'wrapper-env', cmd: 'env ls' },
  { label: 'wrapper-timeout', cmd: 'timeout 5 ls' },
  { label: 'wrapper-nice', cmd: 'nice ls' },
  { label: 'wrapper-xargs', cmd: 'xargs ls' },
  { label: 'wrapper-nohup', cmd: 'nohup ls' },
  { label: 'wrapper-source', cmd: 'source ls' },
  { label: 'wrapper-exec', cmd: 'exec ls' },

  // ── 守卫 5：解释器 + 内联代码 ──
  { label: 'interp-inline-c', cmd: 'bash -c "rm -rf /"' },
  { label: 'interp-inline-cluster', cmd: 'bash -lc "rm -rf /"' },
  { label: 'interp-node-eval', cmd: 'node -e "x"' },
  { label: 'interp-long-eval', cmd: 'node --eval "x"' },
  { label: 'interp-no-inline-flag', cmd: 'bash script.sh' }, // 无内联 flag → 可放行
  { label: 'interp-python3-c', cmd: 'python3 -c "import os"' },
  { label: 'non-interp-dash-c', cmd: 'grep -c x f' },        // 非解释器 → -c 无害

  // ── UNMODELLED_SHELL_CHARS 整体拒绝 ──
  { label: 'redirect-out', cmd: 'echo x > f' },
  { label: 'redirect-in', cmd: 'cat < f' },
  { label: 'backslash', cmd: 'ls \\' },
  { label: 'bang', cmd: 'ls !x' },

  // ── 链式：一段未覆盖 → 整条拒绝 ──
  { label: 'chain-second-uncovered', cmd: 'ls && rm -rf /' },
  { label: 'chain-pipe-uncovered', cmd: 'ls | rm' },
  { label: 'chain-semi-uncovered', cmd: 'ls; rm' },

  // ── 空 / 仅空白 ──
  { label: 'empty', cmd: '' },
  { label: 'only-spaces', cmd: '   ' },
]

const out: Record<string, unknown> = {
  // 分段：每条命令的段数组（逐值对账）
  segments: commands.map(({ label, cmd }) => ({
    label,
    cmd,
    segments: splitShellSegments(cmd),
  })),
  // deny 判定：命令 × denylist 的交叉矩阵
  denied: commands.flatMap(({ label, cmd }) =>
    denylists.map(({ label: dlLabel, list }) => ({
      label: `${label} × ${dlLabel}`,
      cmd,
      denylist: list,
      denied: isBashCommandDenied(cmd, list),
    })),
  ),
  // allow 判定：allow 语料 × allowlist 的交叉矩阵
  //
  // **不复用 `commands` 语料**：那批是围绕**分段**设计的（含大量引号/替换符
  // 怪例），而 allow 侧的重点是 5 道守卫。混用会让失败信号难以定位到守卫。
  allowed: allowCommands.flatMap(({ label, cmd }) =>
    allowlists.map(({ label: alLabel, list }) => ({
      label: `${label} × ${alLabel}`,
      cmd,
      allowlist: list,
      allowed: isBashCommandAllowlisted(cmd, list),
    })),
  ),
}

mkdirSync(here, { recursive: true })
writeFileSync(join(here, 'oracle.json'), JSON.stringify(out, null, 2) + '\n')
const sha = createHash('sha256').update(JSON.stringify(out)).digest('hex')
console.error(
  `shellsplit oracle：命令 ${commands.length} 条、denylist ${denylists.length} 组、` +
    `denied 组合 ${(out.denied as unknown[]).length} 条、` +
    `allow 命令 ${allowCommands.length} 条、allowlist ${allowlists.length} 组、` +
    `allowed 组合 ${(out.allowed as unknown[]).length} 条 — sha256 ${sha.slice(0, 16)}`,
)
