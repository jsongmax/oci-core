/** 页面名（侧栏、面包屑、标签页标题共用）与外壳上的通用操作。 */
const zh = {
  pages: {
    login: '登录', overview: '总览', accounts: '账号', instances: '实例', network: '网络',
    jobs: '任务', hunt: '容量守候', capacity: '容量监控', storage: '存储', resources: '资源',
    billing: '账单', proxies: '代理', notifications: '通知', settings: '设置'
  } as Record<string, string>,
  /** 侧栏里的短名，比页面名更短 */
  side: {
    overview: '总览', accounts: '账号', instances: '实例', network: '网络', storage: '存储',
    resources: '资源', jobs: '任务', capacity: '容量', billing: '账单', hunt: '守候',
    proxies: '代理', notifications: '通知', settings: '设置'
  },
  mainNav: '主导航',
  toLight: '切到 Light',
  toDark: '切到 Dark',
  collapse: '折叠侧栏',
  expand: '展开侧栏',
  /** 语言按钮上显示「切换到的那种语言」的名字 */
  switchLanguage: 'English'
}

const en: typeof zh = {
  pages: {
    login: 'Sign in', overview: 'Overview', accounts: 'Accounts', instances: 'Instances', network: 'Network',
    jobs: 'Jobs', hunt: 'Capacity hunt', capacity: 'Capacity watch', storage: 'Storage', resources: 'Resources',
    billing: 'Billing', proxies: 'Proxies', notifications: 'Notifications', settings: 'Settings'
  },
  side: {
    overview: 'Overview', accounts: 'Accounts', instances: 'Instances', network: 'Network', storage: 'Storage',
    resources: 'Resources', jobs: 'Jobs', capacity: 'Capacity', billing: 'Billing', hunt: 'Hunt',
    proxies: 'Proxies', notifications: 'Notifications', settings: 'Settings'
  },
  mainNav: 'Main navigation',
  toLight: 'Light mode',
  toDark: 'Dark mode',
  collapse: 'Collapse sidebar',
  expand: 'Expand sidebar',
  switchLanguage: '简体中文'
}

export default { zh, en }
