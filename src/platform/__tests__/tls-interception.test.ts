import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import {
  detectTlsInterception,
  findInterceptionCerts,
  formatTlsTrustLines,
  type TlsTrustProbe,
} from '../tls-interception.js'

const PEM = (subject: string): string =>
  `-----BEGIN CERTIFICATE-----\n${subject}\n-----END CERTIFICATE-----`

function probe(system: string[], bundled: string[], effective?: string[]): TlsTrustProbe {
  return {
    bundled: () => bundled,
    system: () => system,
    effective: () => effective ?? bundled,
  }
}

/** 多个拦截产品并存的系统证书存储：6 张同厂商（撑满展示上限）+ 1 张异厂商（排在上限之后）。 */
const MIXED_MITM_STORE = [
  ...Array.from({ length: 6 }, (_, i) => PEM(`O=Zscaler Inc, CN=Zscaler Root CA ${i}`)),
  PEM('C=RU, O=AO KASPERSKY LAB, CN=Kaspersky Anti-Virus Personal Root Certificate'),
]

describe('findInterceptionCerts', () => {
  it('命中的是中间人厂商根证书（大小写不敏感）', () => {
    const { suspects, vendors, count } = findInterceptionCerts([
      PEM('C=RU, O=AO KASPERSKY LAB, CN=Kaspersky Anti-Virus Personal Root Certificate'),
      PEM('C=US, O=DigiCert Inc, CN=DigiCert Global Root G2'),
    ])
    assert.equal(count, 1)
    assert.deepEqual(vendors, ['Kaspersky'])
    assert.match(suspects[0]!, /KASPERSKY/i)
  })

  it('公开 CA 不误报', () => {
    const { count } = findInterceptionCerts([
      PEM('C=US, O=Internet Security Research Group, CN=ISRG Root X1'),
      PEM('C=CN, O=TrustAsia, CN=TrustAsia TLS RSA CA'),
    ])
    assert.equal(count, 0)
  })

  it('命中数超过展示上限时截断 suspects 但保留真实计数', () => {
    const many = Array.from({ length: 8 }, (_, i) => PEM(`O=Zscaler Inc, CN=Zscaler Root CA ${i}`))
    const { suspects, vendors, count } = findInterceptionCerts(many)
    assert.equal(count, 8)
    assert.equal(suspects.length, 5)
    assert.deepEqual(vendors, ['Zscaler'])
  })

  // 上面那条用的是同一厂商的 8 张证书，所以「vendors 被展示上限一起卡掉」这个缺陷它测不出来：
  // 8 张全命中 Zscaler，去重后无论卡不卡都是 ['Zscaler']。真实系统证书存储里多个拦截产品
  // 并存是常态，而 getCACertificates('system') 的返回顺序由 OS 决定，不由我们决定。
  it('厂商清单不受展示上限影响——排在第 6 张之后的软件也要被点名', () => {
    const { suspects, vendors, count } = findInterceptionCerts(MIXED_MITM_STORE)
    assert.equal(count, 7, '计数应为全部命中')
    assert.equal(suspects.length, 5, 'subjects 仍按展示上限截断')
    // 用户去改哪个软件的设置，取决于这一条里有没有它的名字。
    assert.deepEqual(vendors, ['Zscaler', 'Kaspersky'], '第 6 张之后的卡巴斯基被展示上限吞掉——诊断会指错方向')
  })

  it('subject 取 PEM 的第一行内容（不是 BEGIN 头）', () => {
    const { suspects } = findInterceptionCerts([
      `-----BEGIN CERTIFICATE-----\nO=ESET, spol. s r.o., CN=ESET SSL Filter CA\nMIIB...\n-----END CERTIFICATE-----`,
    ])
    assert.equal(suspects[0], 'O=ESET, spol. s r.o., CN=ESET SSL Filter CA')
  })
})

describe('detectTlsInterception', () => {
  it('生效链长于内置链 ⇒ 判定为已信任系统 CA', () => {
    const report = detectTlsInterception(probe(['sys-a'], ['b1', 'b2'], ['b1', 'b2', 'sys-a']))
    assert.equal(report.trustsSystemCa, true)
    assert.equal(report.bundledCaCount, 2)
  })

  it('生效链等于内置链 ⇒ 判定为未信任系统 CA（默认形态）', () => {
    const report = detectTlsInterception(probe(['sys-a'], ['b1', 'b2']))
    assert.equal(report.trustsSystemCa, false)
  })

  it('系统存储不可读时退化为未检出，不抛错', () => {
    const report = detectTlsInterception({
      bundled: () => ['b1'],
      system: () => {
        throw new Error('system store unsupported')
      },
      effective: () => ['b1'],
    })
    assert.equal(report.systemStoreReadable, false)
    assert.deepEqual(report.suspects, [])
  })
})

describe('formatTlsTrustLines', () => {
  it('检出中间人且未信任系统 CA 时给出三条处置建议', () => {
    const report = detectTlsInterception(
      probe([PEM('O=AO KASPERSKY LAB, CN=Kaspersky Anti-Virus Personal Root Certificate')], ['b1']),
    )
    const lines = formatTlsTrustLines(report).join('\n')
    assert.match(lines, /Kaspersky/)
    assert.match(lines, /NODE_EXTRA_CA_CERTS/)
    assert.match(lines, /--use-system-ca/)
    assert.match(lines, /加密连接扫描/)
  })

  it('摘要行点名的厂商与计数口径一致——不指错要用户去改的那个软件', () => {
    const report = detectTlsInterception(probe(MIXED_MITM_STORE, ['bundled-a']))
    const [headline] = formatTlsTrustLines(report)
    assert.equal(report.suspectCount, 7)
    assert.match(headline!, /7 条/)
    assert.match(headline!, /Kaspersky/, '摘要行漏掉真正要用户去设置里排除的那个软件')
  })

  it('未检出时只报平安，不刷建议', () => {
    const lines = formatTlsTrustLines(detectTlsInterception(probe([PEM('O=DigiCert Inc, CN=DigiCert Global Root G2')], ['b1'])))
    assert.equal(lines.length, 1)
    assert.match(lines[0]!, /未检出/)
  })

  it('系统存储不可读时明确说明跳过（不假装探过）', () => {
    const lines = formatTlsTrustLines({
      systemStoreReadable: false,
      systemCaCount: 0,
      trustsSystemCa: false,
      bundledCaCount: 1,
      suspects: [],
      suspectCount: 0,
      vendors: [],
    })
    assert.equal(lines.length, 1)
    assert.match(lines[0]!, /跳过/)
  })
})
