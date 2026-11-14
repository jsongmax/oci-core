/** 存储页。 */
const zh = {
  tabs: { boot: '引导卷', block: '块存储' } as Record<string, string>,
  lowCost: '低成本档',
  ultraHigh: '超高性能',
  iops25k: '约 25k IOPS',
  iops3k: '约 3k IOPS',
  title: '存储',
  subtitle: (boots: number, blocks: number, gb: number) =>
    `跨账号聚合 · 引导卷 ${boots} · 块存储 ${blocks} · 引导卷合计 ${gb} GB`,
  groupByAccount: '按账号分组',
  loadingShort: '加载中',
  bootTitle: '引导卷',
  bootNote: '容量只能增不能减；扩容后需在实例内扩展分区与文件系统',
  noInstances: '所选账号下暂无实例',
  noInstancesBody: '引导卷随实例一同创建。',
  colInstance: '实例', colAccount: '账号', colRegion: '区域', colSize: '容量', colVpu: 'VPU', colPerf: '预估性能',
  colName: '名称', colState: '状态',
  manage: '管理',
  blockTitle: '块存储',
  blockNote: '独立于实例的数据盘',
  noBlocks: '所选账号下暂无块存储卷',
  noBlocksBody: '块存储需要在 Oracle 控制台创建后再挂载到实例。'
}

const en: typeof zh = {
  tabs: { boot: 'Boot volumes', block: 'Block volumes' },
  lowCost: 'Lower cost',
  ultraHigh: 'Ultra high',
  iops25k: '~25k IOPS',
  iops3k: '~3k IOPS',
  title: 'Storage',
  subtitle: (boots: number, blocks: number, gb: number) =>
    `All accounts · ${boots} boot · ${blocks} block · ${gb} GB of boot volumes`,
  groupByAccount: 'Group by account',
  loadingShort: 'Loading',
  bootTitle: 'Boot volumes',
  bootNote: 'Size can only grow; after resizing, extend the partition and file system inside the instance',
  noInstances: 'No instances in the selected accounts',
  noInstancesBody: 'Boot volumes are created together with instances.',
  colInstance: 'Instance', colAccount: 'Account', colRegion: 'Region', colSize: 'Size', colVpu: 'VPU', colPerf: 'Est. performance',
  colName: 'Name', colState: 'State',
  manage: 'Manage',
  blockTitle: 'Block volumes',
  blockNote: 'Data disks independent of instances',
  noBlocks: 'No block volumes in the selected accounts',
  noBlocksBody: 'Create block volumes in the Oracle Console, then attach them to instances.'
}

export default { zh, en }
