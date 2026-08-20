/**
 * SSE 实时事件流。
 *
 * 生命周期转换要 30–90 秒，轮询既慢又浪费。后端用 SSE 单向推送，
 * 浏览器的 EventSource 自带断线重连——但重连后必须重新拉一次全量列表，
 * 因为后端不做历史事件补发（见 docs/API.md）。
 */

export type ServerEventType =
  | 'instance.updated'
  | 'instance.removed'
  | 'instance.error'
  | 'account.status'
  | 'sync.started'
  | 'sync.finished'

export interface ServerEvent {
  type: ServerEventType
  at: string
  instanceId?: string
  accountId?: string
  state?: string
  message?: string
  data?: unknown
}

export interface EventHandlers {
  onEvent?: (event: ServerEvent) => void
  /** 连接建立时触发。断线重连也会触发，此时应重新拉全量数据。 */
  onOpen?: (reconnected: boolean) => void
  onError?: () => void
}

const ALL_TYPES: ServerEventType[] = [
  'instance.updated', 'instance.removed', 'instance.error',
  'account.status', 'sync.started', 'sync.finished'
]

/**
 * 建立事件流连接，返回断开函数。
 *
 * 只在整个应用生命周期内连一条：SSE 在 HTTP/1.1 下占用一个连接，
 * 每个页面各连一条很容易撞上浏览器的并发连接上限。
 */
export function connectEvents(handlers: EventHandlers): () => void {
  let source: EventSource | null = null
  let opened = false
  let closed = false

  const open = () => {
    if (closed) return
    source = new EventSource('/api/events', { withCredentials: true })

    source.onopen = () => {
      const reconnected = opened
      opened = true
      handlers.onOpen?.(reconnected)
    }

    source.onerror = () => {
      // EventSource 会自己重连，这里只用于通知 UI 显示"连接中断"。
      handlers.onError?.()
    }

    for (const type of ALL_TYPES) {
      source.addEventListener(type, (raw) => {
        try {
          const event = JSON.parse((raw as MessageEvent).data) as ServerEvent
          handlers.onEvent?.(event)
        } catch {
          // 事件体解析失败没有补救办法，丢掉即可——
          // 前端本来就有定期兜底刷新。
        }
      })
    }
  }

  open()

  return () => {
    closed = true
    source?.close()
    source = null
  }
}
