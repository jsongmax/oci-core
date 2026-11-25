/**
 * 演示站的权限自检数据。功能与所需权限和后端 internal/permsvc/catalog.go 一一对应，
 * 结论按三种典型场景编造：策略齐全、写成了 use、读不到策略只能靠探测。
 */
import type * as D from '@/api/types'

type L = (zh: string, en: string) => string
type Need = [perm: string, type: string, verb: string]
type Scenario = 'full' | 'use' | 'probe'

const CAPS: { key: string; category: D.PermItemDTO['category']; zh: string; en: string; needs: Need[]; probe?: string }[] = [
  { key: 'instance.view', category: 'compute', zh: '查看实例列表与详情', en: 'View instances and details', probe: 'ListInstances',
    needs: [['INSTANCE_READ', 'instances', 'read'], ['INSTANCE_INSPECT', 'instances', 'inspect'], ['VNIC_ATTACHMENT_READ', 'vnic-attachments', 'inspect'], ['VNIC_READ', 'vnics', 'inspect']] },
  { key: 'instance.power', category: 'compute', zh: '启动、停止、重启实例', en: 'Start, stop and reboot instances', needs: [['INSTANCE_POWER_ACTIONS', 'instances', 'use']] },
  { key: 'instance.update', category: 'compute', zh: '修改实例配置', en: 'Update instance configuration', needs: [['INSTANCE_UPDATE', 'instances', 'use']] },
  { key: 'instance.launch', category: 'compute', zh: '创建实例', en: 'Launch instances',
    needs: [['INSTANCE_CREATE', 'instances', 'manage'], ['INSTANCE_IMAGE_READ', 'instance-images', 'read'], ['INSTANCE_IMAGE_INSPECT', 'instance-images', 'inspect'], ['VNIC_CREATE', 'vnics', 'use'], ['VNIC_ATTACH', 'vnics', 'use'], ['SUBNET_ATTACH', 'subnets', 'use'], ['INSTANCE_INSPECT', 'instances', 'inspect']] },
  { key: 'instance.terminate', category: 'compute', zh: '终止实例', en: 'Terminate instances',
    needs: [['INSTANCE_DELETE', 'instances', 'manage'], ['VNIC_DELETE', 'vnics', 'use'], ['SUBNET_DETACH', 'subnets', 'use']] },
  { key: 'instance.console', category: 'compute', zh: '创建与删除控制台连接', en: 'Create and delete console connections',
    needs: [['INSTANCE_CONSOLE_CONNECTION_CREATE', 'instance-console-connection', 'manage'], ['INSTANCE_CONSOLE_CONNECTION_DELETE', 'instance-console-connection', 'manage'], ['INSTANCE_READ', 'instances', 'read']] },
  { key: 'capacity.report', category: 'compute', zh: '容量查询、容量监控与守候预检', en: 'Capacity checks, capacity watch and hunt pre-checks', probe: 'CreateComputeCapacityReport',
    needs: [['COMPUTE_CAPACITY_REPORT_CREATE', 'compute-capacity-reports', 'manage']] },
  { key: 'network.view', category: 'network', zh: '查看 VCN、子网与安全列表', en: 'View VCNs, subnets and security lists', probe: 'ListVcns',
    needs: [['VCN_READ', 'vcns', 'inspect'], ['SUBNET_READ', 'subnets', 'inspect'], ['SECURITY_LIST_READ', 'security-lists', 'inspect'], ['INTERNET_GATEWAY_READ', 'internet-gateways', 'inspect'], ['ROUTE_TABLE_READ', 'route-tables', 'inspect']] },
  { key: 'network.rules', category: 'network', zh: '修改安全规则', en: 'Update security rules', needs: [['SECURITY_LIST_UPDATE', 'security-lists', 'manage']] },
  { key: 'network.publicip', category: 'network', zh: '更换临时公网 IP', en: 'Replace ephemeral public IPs',
    needs: [['PRIVATE_IP_READ', 'private-ips', 'inspect'], ['PRIVATE_IP_ASSIGN_PUBLIC_IP', 'private-ips', 'use'], ['PRIVATE_IP_UNASSIGN_PUBLIC_IP', 'private-ips', 'use']] },
  { key: 'network.ipv6', category: 'network', zh: '添加与删除 IPv6 地址', en: 'Add and remove IPv6 addresses',
    needs: [['IPV6_READ', 'ipv6s', 'read'], ['IPV6_CREATE', 'ipv6s', 'manage'], ['IPV6_DELETE', 'ipv6s', 'manage'], ['SUBNET_ATTACH', 'subnets', 'use'], ['SUBNET_DETACH', 'subnets', 'use']] },
  { key: 'network.vnic', category: 'network', zh: '附加与分离辅助网卡', en: 'Attach and detach secondary VNICs',
    needs: [['INSTANCE_ATTACH_SECONDARY_VNIC', 'instances', 'manage'], ['INSTANCE_DETACH_SECONDARY_VNIC', 'instances', 'manage'], ['VNIC_CREATE', 'vnics', 'use'], ['VNIC_DELETE', 'vnics', 'use'], ['VNIC_ATTACH', 'vnics', 'use'], ['VNIC_DETACH', 'vnics', 'use'], ['SUBNET_ATTACH', 'subnets', 'use'], ['SUBNET_DETACH', 'subnets', 'use']] },
  { key: 'storage.view', category: 'storage', zh: '查看引导卷与块存储卷', en: 'View boot volumes and block volumes', probe: 'ListVolumes',
    needs: [['VOLUME_INSPECT', 'volumes', 'inspect']] },
  { key: 'storage.update', category: 'storage', zh: '引导卷扩容与 VPU 调整', en: 'Resize boot volumes and change VPUs', needs: [['VOLUME_UPDATE', 'volumes', 'use']] },
  { key: 'storage.attach', category: 'storage', zh: '挂载与卸载块存储卷', en: 'Attach and detach block volumes',
    needs: [['VOLUME_ATTACHMENT_CREATE', 'volume-attachments', 'manage'], ['VOLUME_ATTACHMENT_DELETE', 'volume-attachments', 'manage'], ['VOLUME_WRITE', 'volumes', 'use'], ['INSTANCE_ATTACH_VOLUME', 'instances', 'use'], ['INSTANCE_DETACH_VOLUME', 'instances', 'use']] },
  { key: 'storage.delete', category: 'storage', zh: '删除引导卷与块存储卷', en: 'Delete boot volumes and block volumes', needs: [['VOLUME_DELETE', 'volumes', 'manage']] },
  { key: 'account.quota', category: 'account', zh: '查看配额与用量', en: 'View service limits and usage', probe: 'ListLimitValues',
    needs: [['inspect limits', 'limits', 'inspect'], ['read resource-availability', 'resource-availability', 'read']] },
  { key: 'account.metrics', category: 'account', zh: '查看监控指标', en: 'View monitoring metrics', needs: [['METRIC_READ', 'metrics', 'read']] },
  { key: 'account.billing', category: 'account', zh: '查看账单与出站流量', en: 'View billing and outbound traffic', probe: 'RequestSummarizedUsages',
    needs: [['read usage-report', 'usage-report', 'read']] },
  { key: 'account.audit', category: 'account', zh: '查看 OCI 云端审计日志', en: 'View OCI cloud audit logs', probe: 'ListEvents',
    needs: [['AUDIT_EVENT_READ', 'audit-events', 'read']] }
]

const NET_TYPES = new Set(['vcns', 'subnets', 'security-lists', 'internet-gateways', 'route-tables', 'private-ips', 'ipv6s', 'vnics', 'vnic-attachments'])
const VERB = { inspect: 1, read: 2, use: 3, manage: 4 } as Record<string, number>

/** 各场景下策略里写了什么：资源类型 → 动词。 */
const GRANTS: Record<Exclude<Scenario, 'probe'>, Record<string, string>> = {
  full: {
    'instance-family': 'manage', 'virtual-network-family': 'manage', 'volume-family': 'manage',
    'compute-capacity-reports': 'manage', 'all-resources': 'read'
  },
  // issue #10 的写法：网络与卷都写成了 use，也没有容量权限。
  use: {
    'instance-family': 'manage', 'virtual-network-family': 'use', 'volume-family': 'use', 'all-resources': 'read'
  }
}

function family(type: string): string {
  if (NET_TYPES.has(type)) return 'virtual-network-family'
  if (['instances', 'instance-images', 'instance-console-connection'].includes(type)) return 'instance-family'
  if (['volumes', 'volume-attachments'].includes(type)) return 'volume-family'
  return type
}

function statementFor(grants: Record<string, string>, type: string, verb: string): string | undefined {
  for (const fam of [family(type), type, 'all-resources']) {
    const v = grants[fam]
    if (v && VERB[v] >= VERB[verb]) return `Allow group 'OCI Core' to ${v} ${fam} in tenancy`
  }
  return undefined
}

export function demoPermReport(scenario: Scenario, L: L, checkedAt: string): D.PermReportDTO {
  const items: D.PermItemDTO[] = CAPS.map(c => {
    const missing: Record<string, string> = {}
    const needs: D.PermNeedDTO[] = c.needs.map(([perm, type, verb], i) => {
      // 探测只证明每项功能的第一个权限（与后端 probeProves 一致）。
      const probed = c.probe && i === 0
      if (scenario === 'probe') {
        if (probed) {
          const denied = c.key === 'account.billing'
          return { perm, type, verb, status: denied ? 'missing' : 'ok', source: 'probe', via: c.probe,
            reason: denied ? L(`只读探测 ${c.probe} 返回 NotAuthorizedOrNotFound。`, `The read-only probe ${c.probe} returned NotAuthorizedOrNotFound.`) : undefined }
        }
        return { perm, type, verb, status: 'unknown',
          reason: L('无法读取策略，且没有可用的只读探测，无法判断。', 'Policies could not be read and no read-only probe covers this permission.') }
      }
      if (probed && scenario === 'use' && c.key === 'capacity.report') {
        missing[family(type)] = verb
        return { perm, type, verb, status: 'missing', source: 'probe', via: c.probe,
          reason: L(`只读探测 ${c.probe} 返回 NotAuthorizedOrNotFound。`, `The read-only probe ${c.probe} returned NotAuthorizedOrNotFound.`) }
      }
      if (probed) return { perm, type, verb, status: 'ok', source: 'probe', via: c.probe }
      const st = statementFor(GRANTS[scenario], type, verb)
      if (st) return { perm, type, verb, status: 'ok', source: 'policy', via: st }
      const fam = family(type)
      if (!missing[fam] || VERB[missing[fam]] < VERB[verb]) missing[fam] = verb
      return { perm, type, verb, status: 'missing', source: 'policy',
        reason: L('没有任何适用于该用户的策略语句授予此权限。', 'No policy statement that applies to this user grants this permission.') }
    })
    const status: D.PermStatusDTO = needs.some(n => n.status === 'missing') ? 'missing'
      : needs.every(n => n.status === 'ok') ? 'ok' : 'unknown'
    return {
      key: c.key, category: c.category, label: L(c.zh, c.en), status, needs,
      reason: status !== 'unknown' ? undefined
        : needs.some(n => n.source === 'probe' && n.status === 'ok')
          ? L(`只读探测 ${c.probe} 证实了部分所需权限，其余权限无法判断。`, `The read-only probe ${c.probe} confirmed some of the required permissions; the rest cannot be determined.`)
          : needs.find(n => n.status === 'unknown')?.reason,
      fix: status === 'missing'
        ? Object.keys(missing).sort().map(f => `Allow group 'OCI Core' to ${missing[f]} ${f} in tenancy`)
        : undefined
    }
  })
  const count = (s: D.PermStatusDTO) => items.filter(i => i.status === s).length
  return {
    checkedAt,
    policy: scenario === 'probe' ? 'unavailable' : 'ok',
    policyNote: scenario === 'probe'
      ? [L('无法读取根区间的策略。策略解析需要 inspect policies in tenancy；未授予时仅依据只读探测判断。',
        'Policies in the root compartment could not be read. Policy analysis requires inspect policies in tenancy; without it, results rely on read-only probes only.')]
      : undefined,
    groups: scenario === 'probe' ? [] : ['OCI Core'],
    summary: { ok: count('ok'), missing: count('missing'), unknown: count('unknown'), checkedAt },
    items
  }
}
