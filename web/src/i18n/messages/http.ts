/** HTTP 客户端自己生成的错误文案（后端没给 message 时的兜底）。 */
const zh = {
  failed: (status: number) => `请求失败（HTTP ${status}）`,
  network: '无法连接到服务，请检查网络或后端是否在运行'
}

const en: typeof zh = {
  failed: (status: number) => `Request failed (HTTP ${status})`,
  network: 'Can\'t reach the server. Check your network or whether the backend is running.'
}

export default { zh, en }
