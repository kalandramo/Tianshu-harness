/**
 * /login 与 /logout 的注册与分流。
 *
 * ## 为什么需要它
 * 账号登录对用户根本不存在：`src/auth/login-flow.ts` 的 `runOAuthLogin`
 * 只被它自己的测试引用，`src/auth/device-flow.ts` 的解析函数零调用方。
 *
 * ## 一个被修正的错误前提（留痕）
 * 计划里写的是「/login 从未注册（幽灵指引）」——**这是错的**。它早就在
 * slash-commands.ts 注册了，只是用 `register("/login", …)` 这套 API，而不是
 * TUI_SLASH_COMMANDS 数组的 `name: '/login'` 形态。按后者 grep 并 `head -80`
 * 截断，就得出过「不存在」的错误结论。真实状态：/login 存在，但只做 provider
 * OAuth（无参默认 codex），没有天枢账号通道。
 *
 * ## 断言口径
 * 分两层，各自打在真实内容上：
 *   · slash-commands.ts —— 注册壳接线（handleAccountLogin / handleAccountLogout）
 *   · account-login.ts —— 真实 device flow 调用点
 * 而不是 `'account'` 这类字面（本文件里到处能命中，改坏了也照样绿）。
 *
 * 运行：npm exec -- tsx --test src/tui/__tests__/slash-login.test.ts
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const read = (p: string) => readFileSync(resolve(process.cwd(), p), 'utf8')
const src = read('src/tui/slash-commands.ts')
const accountSrc = read('src/tui/account-login.ts')

test('/login 与 /logout 已注册（走 register API，不是 TUI_SLASH_COMMANDS 数组）', () => {
  assert.match(src, /register\("\/login"/, '/login 未注册——用户无法登录任何身份')
  assert.match(src, /register\("\/logout"/, '/logout 未注册——登出无路可走')
})

test('slash-commands 里 /login account 与 /logout 接到了账号实现', () => {
  // 实现在 src/tui/account-login.ts（slash-commands 是行数棘轮点名的巨石，
  // 只留注册壳），所以这里断言的是接线，不是 device flow 细节。
  assert.match(src, /handleAccountLogin\(/, '/login account 未接到账号登录实现')
  assert.match(src, /handleAccountLogout\(/, '/logout 未接到账号登出实现')
})

test('账号登录实现走完整 device flow（真实调用点，非字面）', () => {
  assert.match(
    accountSrc,
    /requestDeviceCode\(/,
    '未调用 requestDeviceCode——账号登录没有发起授权请求',
  )
  assert.match(
    accountSrc,
    /pollForAccountToken\(/,
    '未调用 pollForAccountToken——发起后不会等用户授权',
  )
  assert.match(
    accountSrc,
    /saveAccountToken\(/,
    '未调用 saveAccountToken——拿到 token 也不落盘，等于没登录',
  )
})

test('/login <provider> 保留模型 provider 的 OAuth 通道', () => {
  assert.match(
    src,
    /runOAuthLogin\(/,
    '/login 丢掉了 provider OAuth（codex 等 oauth 型 provider 无路可走）',
  )
})

test('登出只清账号凭据，不碰 provider 的 OAuth', () => {
  assert.match(accountSrc, /accountStore\(/, '/logout 未接 accountStore——登出不会清除凭据')
  assert.ok(
    !/runOAuthLogin/.test(accountSrc),
    '登出实现里出现了 runOAuthLogin——不该动 provider 凭据',
  )
})

test('/status 接了星籍段（实现在 account-status.ts，slash 侧只留一行）', () => {
  assert.match(src, /accountIdentityLines\(\)/, '/status 未接星籍段——用户看不到自己的星籍')
  assert.match(src, /import\('\.\/account-status\.js'\)/, '星籍段未外置（行数棘轮要求沿接缝拆分）')
  // 登录成功时顺带取一次星籍（与 sidecar 的 poll 同法），否则首次 /status 是空的
  assert.match(accountSrc, /primeStellarIdentity\(/, '登录后未预热星籍缓存')
})
