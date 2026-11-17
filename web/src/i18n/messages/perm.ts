/** 权限自检（账号详情、添加账号、账号卡片共用）。权限名、资源类型与动词保持官方原文。 */
const zh = {
  title: '功能权限',
  note: '按功能列出该 API 用户能否使用；依据为策略解析与只读探测',
  draftNote: '保存前检查该 API 用户能使用哪些功能',
  run: '检查权限',
  rerun: '重新检查',
  running: '检查中…',
  notRun: '尚未检查。点击「检查权限」读取策略并执行只读探测。',
  draftWait: '连接测试通过后自动检查权限。',
  failed: '权限检查失败',
  checkedAt: (when: string) => `检查于 ${when}`,
  summary: (ok: number, missing: number, unknown: number) =>
    `${ok} 项可用 · ${missing} 项缺失 · ${unknown} 项无法判断`,
  status: { ok: '可用', missing: '缺失', unknown: '无法判断' } as Record<string, string>,
  category: { compute: '计算', network: '网络', storage: '存储', account: '账号与计费' } as Record<string, string>,
  policy: {
    ok: '已读取策略与用户所在的组，结论来自策略解析，并以只读探测校正。',
    partial: '已读取策略，但部分信息缺失，相关项标为「无法判断」。',
    unavailable: '无法读取策略，仅依据只读探测判断；探测覆盖不到的项标为「无法判断」。'
  } as Record<string, string>,
  groups: (names: string) => `所在的组：${names}`,
  details: '详情',
  hide: '收起',
  colPerm: '权限',
  colRequire: '所需动词与资源类型',
  colSource: '依据',
  sourceProbe: (api: string) => `只读探测 ${api}`,
  sourcePolicy: '策略语句',
  sourceNone: '—',
  fix: '补齐缺失权限所需的策略语句（添加到根区间的策略中）：',
  fixLocation: '策略位置：OCI 控制台导航菜单 → Identity & Security → Identity → Policies，区间选择根区间（root compartment）。',
  disclaimer: 'OCI 不提供查询用户实际权限的接口，以上结论由本工具推算。只有确定缺失的项才标为「缺失」。',
  cardMissing: (n: number) => `缺 ${n} 项权限`,
  cardOk: '权限齐全',
  cardUnknown: (n: number) => `${n} 项权限无法判断`,
  cardNone: '未检查权限',
  cardTitle: (ok: number, missing: number, unknown: number, when: string) =>
    `权限自检：${ok} 项可用，${missing} 项缺失，${unknown} 项无法判断（检查于 ${when}）`,
  setup: {
    title: '如何创建 API 密钥并配置权限',
    note: '首次添加账号时参考',
    show: '展开',
    hide: '收起',
    intro: 'API 密钥本身不带权限。权限通过策略授予 IAM 用户所在的组：先建组、把用户加入组，再为该组编写策略。',
    stepsTitle: '步骤',
    steps: [
      'OCI 控制台导航菜单 → Identity & Security → Domains，选择 Default 域。在 Groups 中创建组（例如 OCI Core）；在 Users 中创建专用用户（或选择现有用户），将其加入该组。',
      '进入该用户详情 → API keys → Add API key → Generate API key pair，下载私钥。添加完成后页面显示的配置预览（user、fingerprint、tenancy、region）可直接粘贴到下方「粘贴 OCI config」。',
      'Identity & Security → Identity → Policies，区间选择根区间（root compartment）→ Create Policy → 打开 Show manual editor，粘贴下方语句。'
    ],
    groupLabel: '组名',
    groupHint: '用户不在 Default 域时，写成 域名/组名，例如 MyDomain/OCI Core。',
    policyTitle: '策略语句（点击复制）',
    purposeTitle: '各语句的用途',
    colStatement: '语句',
    colPurpose: '用途',
    purposes: [
      ['manage instance-family', '创建、终止、启停、修改实例；控制台连接'],
      ['manage virtual-network-family', '修改安全规则、更换临时公网 IP、IPv6、辅助网卡'],
      ['manage volume-family', '引导卷扩容、VPU 调整、挂载与卸载卷、删除卷'],
      ['manage compute-capacity-reports', '容量查询、容量监控与守候预检'],
      ['read metrics', '监控指标'],
      ['inspect limits / read resource-availability', '配额与用量'],
      ['read usage-report', '账单与出站流量'],
      ['inspect policies / users / groups', '仅供权限自检读取策略与用户所在的组，可省略']
    ] as [string, string][],
    notes: [
      '必须使用 manage 动词。use 不包含创建与终止实例（INSTANCE_CREATE / INSTANCE_DELETE）、更新安全列表（SECURITY_LIST_UPDATE）、创建 IPv6 地址（IPV6_CREATE）等权限；compute-capacity-reports 仅由 manage 授予权限。',
      '省略最后三条 inspect 语句不影响使用，但权限自检无法读取策略，只能依据只读探测判断，多数项会显示「无法判断」。',
      '如需限定区间，可将 in tenancy 改为 in compartment <区间名>；但 compute-capacity-reports、limits、resource-availability 与 usage-report 须保留 in tenancy。',
      '用户若属于 Administrators 组，已拥有全部权限，无需另写策略；建议改用专用用户，不要使用管理员的 API 密钥。'
    ]
  }
}

const en: typeof zh = {
  title: 'Feature permissions',
  note: 'Which features this API user can use, based on policy analysis and read-only probes',
  draftNote: 'Check which features this API user can use before saving',
  run: 'Check permissions',
  rerun: 'Check again',
  running: 'Checking…',
  notRun: 'Not checked yet. Click "Check permissions" to read policies and run read-only probes.',
  draftWait: 'Permissions are checked automatically after the connection test passes.',
  failed: 'Permission check failed',
  checkedAt: (when: string) => `Checked ${when}`,
  summary: (ok: number, missing: number, unknown: number) =>
    `${ok} available · ${missing} missing · ${unknown} undetermined`,
  status: { ok: 'Available', missing: 'Missing', unknown: 'Undetermined' } as Record<string, string>,
  category: { compute: 'Compute', network: 'Networking', storage: 'Storage', account: 'Account & billing' } as Record<string, string>,
  policy: {
    ok: 'Policies and the user\'s groups were read; results come from policy analysis, corrected by read-only probes.',
    partial: 'Policies were read but some information is missing; affected items are marked Undetermined.',
    unavailable: 'Policies could not be read, so results rely on read-only probes only; items the probes cannot cover are marked Undetermined.'
  } as Record<string, string>,
  groups: (names: string) => `Groups: ${names}`,
  details: 'Details',
  hide: 'Hide',
  colPerm: 'Permission',
  colRequire: 'Required verb and resource type',
  colSource: 'Basis',
  sourceProbe: (api: string) => `Read-only probe ${api}`,
  sourcePolicy: 'Policy statement',
  sourceNone: '—',
  fix: 'Policy statements that grant the missing permissions (add them to a policy in the root compartment):',
  fixLocation: 'Location: OCI Console navigation menu → Identity & Security → Identity → Policies, with the root compartment selected.',
  disclaimer: 'OCI has no API that reports a user\'s effective permissions; these results are inferred by this tool. Only items that are definitely missing are marked Missing.',
  cardMissing: (n: number) => `${n} permission${n === 1 ? '' : 's'} missing`,
  cardOk: 'All permissions present',
  cardUnknown: (n: number) => `${n} undetermined`,
  cardNone: 'Permissions not checked',
  cardTitle: (ok: number, missing: number, unknown: number, when: string) =>
    `Permission check: ${ok} available, ${missing} missing, ${unknown} undetermined (checked ${when})`,
  setup: {
    title: 'How to create an API key and set up permissions',
    note: 'For first-time setup',
    show: 'Show',
    hide: 'Hide',
    intro: 'An API key carries no permissions by itself. Permissions are granted by policies to the groups the IAM user belongs to: create a group, add the user to it, then write policies for that group.',
    stepsTitle: 'Steps',
    steps: [
      'In the OCI Console navigation menu, go to Identity & Security → Domains and select the Default domain. Under Groups, create a group (for example OCI Core). Under Users, create a dedicated user (or pick an existing one) and add it to the group.',
      "Open the user's details → API keys → Add API key → Generate API key pair, and download the private key. The configuration file preview shown afterwards (user, fingerprint, tenancy, region) can be pasted into “Paste OCI config” below.",
      'Go to Identity & Security → Identity → Policies, select the root compartment → Create Policy → turn on Show manual editor, and paste the statements below.'
    ],
    groupLabel: 'Group name',
    groupHint: 'If the user is not in the Default domain, write DomainName/GroupName, for example MyDomain/OCI Core.',
    policyTitle: 'Policy statements (click to copy)',
    purposeTitle: 'What each statement is for',
    colStatement: 'Statement',
    colPurpose: 'Purpose',
    purposes: [
      ['manage instance-family', 'Launch, terminate, start/stop and update instances; console connections'],
      ['manage virtual-network-family', 'Update security rules, replace ephemeral public IPs, IPv6, secondary VNICs'],
      ['manage volume-family', 'Resize boot volumes, change VPUs, attach/detach and delete volumes'],
      ['manage compute-capacity-reports', 'Capacity checks, capacity watch and hunt pre-checks'],
      ['read metrics', 'Monitoring metrics'],
      ['inspect limits / read resource-availability', 'Service limits and usage'],
      ['read usage-report', 'Billing and outbound traffic'],
      ['inspect policies / users / groups', 'Only used by the permission self-check to read policies and group memberships; optional']
    ] as [string, string][],
    notes: [
      'Use the manage verb. use does not include permissions such as launching and terminating instances (INSTANCE_CREATE / INSTANCE_DELETE), updating security lists (SECURITY_LIST_UPDATE) or creating IPv6 addresses (IPV6_CREATE); compute-capacity-reports permissions are granted only by manage.',
      'Omitting the last three inspect statements does not affect normal use, but the permission self-check cannot read policies and relies on read-only probes only, so most items show Undetermined.',
      'To restrict access to one compartment, replace in tenancy with in compartment <name>; compute-capacity-reports, limits, resource-availability and usage-report must stay in tenancy.',
      "Users in the Administrators group already have all permissions and need no policy; a dedicated user is recommended instead of an administrator's API key."
    ]
  }
}

export default { zh, en }
