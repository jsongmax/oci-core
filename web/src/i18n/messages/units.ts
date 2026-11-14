/** 计数单位：「3 个卷」/「3 volumes」。英文要分单复数，所以都是函数。 */
const pl = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`

const zh = {
  vcn: (n: number) => `${n} 个 VCN`,
  publicIp: (n: number) => `${n} 个公网 IP`,
  prefix: (n: number) => `${n} 个前缀`,
  volume: (n: number) => `${n} 个卷`,
  subnet: (n: number) => `${n} 个子网`,
  bootVolume: (n: number) => `${n} 个引导卷`,
  instance: (n: number) => `${n} 台`
}

const en: typeof zh = {
  vcn: (n: number) => pl(n, 'VCN', 'VCNs'),
  publicIp: (n: number) => pl(n, 'public IP', 'public IPs'),
  prefix: (n: number) => pl(n, 'prefix', 'prefixes'),
  volume: (n: number) => pl(n, 'volume', 'volumes'),
  subnet: (n: number) => pl(n, 'subnet', 'subnets'),
  bootVolume: (n: number) => pl(n, 'boot volume', 'boot volumes'),
  instance: (n: number) => pl(n, 'instance', 'instances')
}

export default { zh, en }
