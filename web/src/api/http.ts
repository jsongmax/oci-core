/**
 * HTTP 客户端。所有后端调用都走这里。
 *
 * 三件事集中在这一层处理，业务代码不必重复：
 *  1. 写操作自动带 CSRF 头（后端要求，见 docs/API.md）
 *  2. 错误统一成 ApiError，保留原始 OCI 错误码与处理建议
 *  3. 401 广播一次全局事件，由路由层决定跳登录还是弹两步验证
 */

/** 后端约定的 CSRF 请求头。浏览器不允许跨源请求携带自定义头。 */
const CSRF_HEADER = 'X-OCI-Tools'
const CSRF_VALUE = '1'

/** 401 时广播的事件名。 */
export const AUTH_REQUIRED_EVENT = 'oci:auth-required'

export interface ApiErrorBody {
  code?: string
  message?: string
  ociCode?: string
  advice?: string
}

/**
 * ApiError 保留后端给的全部错误信息。
 *
 * ociCode 是 Oracle 的原始错误码，UI 上必须原样展示且可复制——
 * 用户排障时要拿它去搜索。advice 是可直接显示的中文建议。
 */
export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly ociCode?: string
  readonly advice?: string

  constructor(status: number, body: ApiErrorBody) {
    super(body.message || `请求失败（HTTP ${status}）`)
    this.name = 'ApiError'
    this.status = status
    this.code = body.code || 'unknown'
    this.ociCode = body.ociCode
    this.advice = body.advice
  }

  /** 供 Toast 直接显示的一行说明。 */
  get display(): string {
    return this.ociCode ? `${this.ociCode} · ${this.message}` : this.message
  }

  /** 需要用户完成两步验证。 */
  get needsTotp(): boolean {
    return this.code === 'totp_required'
  }

  /** 该操作缺少确认参数，前端应弹确认框后重试。 */
  get needsConfirm(): boolean {
    return this.code === 'confirm_required'
  }
}

type Method = 'GET' | 'POST' | 'PATCH' | 'PUT' | 'DELETE'

interface RequestOptions {
  /** 查询参数。undefined / null / '' 的项会被丢弃。 */
  query?: Record<string, string | number | boolean | undefined | null>
  body?: unknown
  signal?: AbortSignal
}

function buildUrl(path: string, query?: RequestOptions['query']): string {
  if (!query) return path
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined || value === null || value === '') continue
    params.set(key, String(value))
  }
  const qs = params.toString()
  return qs ? `${path}?${qs}` : path
}

async function request<T>(method: Method, path: string, opts: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' }

  // 读操作不需要 CSRF 头，带上也无害，但保持与后端约定一致更清楚。
  if (method !== 'GET') {
    headers[CSRF_HEADER] = CSRF_VALUE
    if (opts.body !== undefined) headers['Content-Type'] = 'application/json'
  }

  let response: Response
  try {
    response = await fetch(buildUrl(path, opts.query), {
      method,
      headers,
      credentials: 'same-origin',
      body: opts.body === undefined ? undefined : JSON.stringify(opts.body),
      signal: opts.signal
    })
  } catch (err) {
    // 网络层失败没有状态码。用 0 表示，让上层能与业务错误区分开。
    if ((err as Error)?.name === 'AbortError') throw err
    throw new ApiError(0, { code: 'network', message: '无法连接到服务，请检查网络或后端是否在运行' })
  }

  if (response.status === 204) return undefined as T

  const text = await response.text()
  let payload: unknown = undefined
  if (text) {
    try {
      payload = JSON.parse(text)
    } catch {
      payload = undefined
    }
  }

  if (!response.ok) {
    const body = (payload ?? {}) as ApiErrorBody
    if (response.status === 401) {
      // 广播而不是直接跳转：路由不该被 HTTP 层硬编码。
      window.dispatchEvent(new CustomEvent(AUTH_REQUIRED_EVENT, {
        detail: { code: body.code ?? 'unauthenticated' }
      }))
    }
    throw new ApiError(response.status, body)
  }

  return payload as T
}

export const http = {
  get: <T>(path: string, opts?: RequestOptions) => request<T>('GET', path, opts),
  post: <T>(path: string, body?: unknown, opts?: RequestOptions) =>
    request<T>('POST', path, { ...opts, body }),
  patch: <T>(path: string, body?: unknown, opts?: RequestOptions) =>
    request<T>('PATCH', path, { ...opts, body }),
  put: <T>(path: string, body?: unknown, opts?: RequestOptions) =>
    request<T>('PUT', path, { ...opts, body }),
  del: <T>(path: string, opts?: RequestOptions) => request<T>('DELETE', path, opts)
}

/** 把任意异常转成可显示的一行文本。 */
export function errorText(err: unknown): string {
  if (err instanceof ApiError) return err.display
  if (err instanceof Error) return err.message
  return String(err)
}

/** 取出错误里的处理建议，没有则返回空串。 */
export function errorAdvice(err: unknown): string {
  return err instanceof ApiError && err.advice ? err.advice : ''
}
