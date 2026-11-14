/** 任务页与存储页。 */
const zh = {
  status: {
    queued: '排队中', running: '进行中', succeeded: '已完成',
    failed: '失败', canceled: '已取消', interrupted: '已中断'
  },
  durS: (s: number) => `${s} 秒`,
  durMS: (m: number, s: number) => `${m} 分 ${s} 秒`,
  clearTitle: '清除已结束的任务',
  clearBody: (n: number) => `将从列表中移除 ${n} 条已结束的任务。操作本身已记在审计日志里，不受影响。`,
  clear: '清除',
  title: '任务',
  subtitle: (active: number, all: number) => `长耗时操作在后台执行，关掉页面也会跑完 · ${active} 个进行中，共 ${all} 条`,
  clearFinished: '清除已结束',
  filterAll: '全部',
  filterActive: '进行中',
  filterFailed: '失败',
  filterDone: '已完成',
  listTitle: '任务列表',
  listNote: '最多保留最近 300 条已结束的任务',
  emptyNone: '还没有后台任务',
  emptyFiltered: '这个分类下没有任务',
  emptySub: '创建实例、更换公网 IP、修改配置等操作提交后会出现在这里。',
  submitted: (when: string) => `${when}提交`,
  took: (d: string) => `用时 ${d}`,
  waitingQueue: '等待前面的任务完成（同一台实例上的操作按顺序执行）',
  noSteps: '还没有进度记录',
  interruptedHint: '请求可能已经发到 Oracle，请到对应页面确认实际状态，再决定是否重做。',
  remove: '移除'
}

const en: typeof zh = {
  status: {
    queued: 'Queued', running: 'Running', succeeded: 'Done',
    failed: 'Failed', canceled: 'Canceled', interrupted: 'Interrupted'
  },
  durS: (s: number) => `${s}s`,
  durMS: (m: number, s: number) => `${m}m ${s}s`,
  clearTitle: 'Clear finished jobs',
  clearBody: (n: number) =>
    `Removes ${n} finished ${n === 1 ? 'job' : 'jobs'} from the list. The actions themselves stay in the audit log.`,
  clear: 'Clear',
  title: 'Jobs',
  subtitle: (active: number, all: number) =>
    `Long-running actions run in the background and finish even if you close the page · ${active} running, ${all} total`,
  clearFinished: 'Clear finished',
  filterAll: 'All',
  filterActive: 'Running',
  filterFailed: 'Failed',
  filterDone: 'Done',
  listTitle: 'Job list',
  listNote: 'Keeps the latest 300 finished jobs',
  emptyNone: 'No background jobs yet',
  emptyFiltered: 'No jobs in this category',
  emptySub: 'Creating instances, changing public IPs, changing shapes and similar actions show up here once submitted.',
  submitted: (when: string) => `Submitted ${when}`,
  took: (d: string) => `took ${d}`,
  waitingQueue: 'Waiting for earlier jobs (actions on the same instance run in order)',
  noSteps: 'No progress yet',
  interruptedHint: 'The request may already have reached Oracle. Check the actual state on the relevant page before retrying.',
  remove: 'Remove'
}

export default { zh, en }
