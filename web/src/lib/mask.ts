/**
 * 敏感数据打码。
 *
 * 场景是截图与共享屏幕——这个面板日常要发截图求助、录演示，而屏幕上到处是
 * 邮箱、公网 IP、租户 OCID。它们单独看都不算机密，凑齐了就够别人定位到
 * 你的机器并开始撞。
 *
 * 两条贯穿始终的原则：
 *
 *  1. **保留可辨识度。** 全打成星号等于让面板没法用——你得能一眼分清
 *     两台机器、认出是哪个账号。所以一律保留前缀与结构，只抹掉能直接
 *     拿去连接或登录的那部分。
 *  2. **复制永远复制原文。** 打码只影响显示。复制出来是星号的话，
 *     这功能就从"隐私保护"变成了"故意添堵"。
 *
 * 这一层不是安全边界：数据仍然完整地经 API 到达浏览器。真正的机密
 * （OCI 私钥、代理密码、通知渠道令牌）在后端就不回显，那才是边界。
 */

const DOT = '•'

/** 重复打码符，长度有上下限，免得极短或极长的值看起来很怪。 */
function dots(n: number): string {
  return DOT.repeat(Math.min(Math.max(n, 3), 8))
}

/**
 * 邮箱：保留首字母与完整域名。
 *
 *   jsongmax@gmail.com  ->  j••••••@gmail.com
 *
 * 保留域名是有意的——它几乎不敏感，却能让你一眼看出这是哪类账号
 * （自己的 Gmail 还是公司邮箱）。真正不该露的是本地部分。
 */
export function maskEmail(v: string): string {
  const at = v.lastIndexOf('@')
  if (at <= 0) return maskGeneric(v)
  const local = v.slice(0, at)
  const domain = v.slice(at)
  return local.slice(0, 1) + dots(local.length - 1) + domain
}

/**
 * IPv4：保留前两段。
 *
 *   168.138.53.100  ->  168.138.•.•
 *
 * 前两段给出的是网段——够你分辨"这台在东京那批里"，但不够任何人拿去
 * 连接。抹掉整个地址反而会让实例列表里几十台机器长得一模一样。
 */
export function maskIPv4(v: string): string {
  const parts = v.split('.')
  if (parts.length !== 4) return maskGeneric(v)
  return `${parts[0]}.${parts[1]}.${DOT}.${DOT}`
}

/** IPv6：保留前两组。 */
export function maskIPv6(v: string): string {
  const parts = v.split(':')
  if (parts.length < 3) return maskGeneric(v)
  return `${parts[0]}:${parts[1]}:${dots(4)}`
}

/** 按内容自动判断 v4 还是 v6。 */
export function maskIP(v: string): string {
  if (!v || v === '—') return v
  if (v.includes(':')) return maskIPv6(v)
  if (v.includes('.')) return maskIPv4(v)
  return maskGeneric(v)
}

/**
 * OCID：保留类型前缀与末尾四位。
 *
 *   ocid1.instance.oc1.ap-tokyo-1.aaaa…wxyz  ->  ocid1.instance…••••wxyz
 *
 * 前缀说明这是什么资源，末四位够你在两条 OCID 之间做区分——排障时
 * 常常就是要核对"是不是同一个"。中间那段才是真正能拿去调 API 的部分。
 */
export function maskOCID(v: string): string {
  if (!v) return v
  const parts = v.split('.')
  if (parts.length < 3 || parts[0] !== 'ocid1') return maskGeneric(v)
  const tail = v.slice(-4)
  return `${parts[0]}.${parts[1]}${DOT.repeat(3)}${tail}`
}

/**
 * 指纹：保留首尾两段。
 *
 *   ab:cd:ef:12:34:…:9f  ->  ab:cd:••••:9f
 */
export function maskFingerprint(v: string): string {
  const parts = v.split(':')
  if (parts.length < 4) return maskGeneric(v)
  return `${parts[0]}:${parts[1]}:${dots(4)}:${parts[parts.length - 1]}`
}

/**
 * 主机地址：打码主机但保留端口。
 *
 *   1.2.3.4:8080  ->  1.2.•.•:8080
 *
 * 端口不敏感，而且是排查代理配置时最要紧的信息——协议对不对、端口填没填错。
 */
export function maskHostPort(v: string): string {
  // 逐层剥掉 scheme:// 与 user:pass@ 再打码主机。
  //
  // 不能只按最后一个冒号切：http://5.6.7.8:3128 里 scheme 自带一个冒号，
  // 按内容判类型会一路跑进 IPv6 分支，结果是 ht••••••••.8:3128 ——
  // 既没打对码，也看不出原来是什么。
  let prefix = ''
  let rest = v

  const proto = rest.indexOf('://')
  if (proto >= 0) {
    prefix = rest.slice(0, proto + 3)
    rest = rest.slice(proto + 3)
  }
  // 用最后一个 @：密码里可能含 @。
  const at = rest.lastIndexOf('@')
  if (at >= 0) {
    prefix += rest.slice(0, at + 1)
    rest = rest.slice(at + 1)
  }

  const colon = rest.lastIndexOf(':')
  if (colon <= 0) return prefix + maskIP(rest)
  return prefix + maskIP(rest.slice(0, colon)) + rest.slice(colon)
}

/** 兜底：保留首尾各两位。太短的值直接整体打码。 */
export function maskGeneric(v: string): string {
  if (!v) return v
  if (v.length <= 6) return dots(v.length)
  return v.slice(0, 2) + dots(v.length - 4) + v.slice(-2)
}

/** 支持的打码类型。 */
export type MaskKind =
  | 'email' | 'ip' | 'ocid' | 'fingerprint' | 'hostport' | 'generic'

const MASKERS: Record<MaskKind, (v: string) => string> = {
  email: maskEmail,
  ip: maskIP,
  ocid: maskOCID,
  fingerprint: maskFingerprint,
  hostport: maskHostPort,
  generic: maskGeneric
}

/**
 * 按类型打码。
 *
 * 空值、占位符（—、未分配、未启用之类）原样返回：把"没有这个值"也
 * 打成星号，会让人以为存在一个被藏起来的值。
 */
export function mask(v: string, kind: MaskKind = 'generic'): string {
  if (!v) return v
  const t = v.trim()
  if (t === '' || t === '—' || t === '-') return v
  // 中文占位文案一律放行：它们不是数据。
  if (/^(未|无|尚未|不)/.test(t)) return v
  return MASKERS[kind](v)
}
