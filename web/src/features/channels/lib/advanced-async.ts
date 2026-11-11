/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import type {
  AdvancedAsyncConfig,
  AdvancedAsyncFields,
  AdvancedAsyncQuery,
  AdvancedAsyncSubmit,
  AdvancedCustomRouteAuth,
} from '../types'

export const CHANNEL_TYPE_ADVANCED_ASYNC = 8001

export const ADVANCED_ASYNC_TASK_STATUSES = [
  'NOT_START',
  'SUBMITTED',
  'QUEUED',
  'IN_PROGRESS',
  'SUCCESS',
  'FAILURE',
  'UNKNOWN',
] as const

// APIDUO:image-async:BEGIN
export const ADVANCED_ASYNC_DEFAULT_INCOMING_PATH = '/v1/video/generations'

/** 同步的 OpenAI Images 入口。异步协议不得复用它。 */
export const ADVANCED_ASYNC_SYNC_IMAGES_PATH = '/v1/images/generations'
// 8001 只做异步，这些同步入口不能被任何协议声明为入站路径。
export const ADVANCED_ASYNC_SYNC_ENTRY_PATHS = [
  ADVANCED_ASYNC_SYNC_IMAGES_PATH,
  '/v1/images/edits',
]

/** 内置图像异步协议的默认入口（下游端点，可改）。 */
export const ADVANCED_ASYNC_IMAGE_ENTRY_PATH =
  '/v1/images/generations/async'

/**
 * 方舟协议的三条内置入口，客户端 base URL 怎么写都能对上：
 *
 *   base = https://apiduo.cn          → /contents/generations/tasks
 *   base = https://apiduo.cn/v1       → /v1/contents/generations/tasks
 *   base = https://apiduo.cn/api/v3   → /api/v3/contents/generations/tasks
 *
 * 前两条是网关的静态路由，第三条是 doubao 插件原生入口（模型不被插件
 * 认领时转交任务中继）。三条都不需要运营手填，这里只是把它们显示出来。
 */
export const ADVANCED_ASYNC_ARK_ENTRY_PATHS = [
  '/v1/contents/generations/tasks',
  '/contents/generations/tasks',
  '/api/v3/contents/generations/tasks',
]
// APIDUO:image-async:END

export function createAdvancedAsyncConfig(): AdvancedAsyncConfig {
  return {
    protocol: 'custom',
    incoming_paths: [ADVANCED_ASYNC_DEFAULT_INCOMING_PATH],
    submit: {
      upstream_path: '',
      method: 'POST',
      content_type: 'json',
      auth: { type: 'header', name: 'Authorization', value: 'Bearer {api_key}' },
    },
    query: {
      upstream_path: '',
      method: 'GET',
      task_id_param: 'task_id',
      task_id_location: 'query',
      auth: { type: 'header', name: 'Authorization', value: 'Bearer {api_key}' },
    },
    fields: {
      task_id_path: ['task_id'],
      status_path: ['status'],
      result_url_path: ['video_url'],
    },
    billing: { mode: 'per_call' },
  }
}

export function parseAdvancedAsyncConfig(
  value: string | undefined
): AdvancedAsyncConfig | null {
  if (!value?.trim()) return null
  try {
    const parsed = JSON.parse(value)
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
      return null
    }
    return normalizeAdvancedAsyncConfig(parsed as AdvancedAsyncConfig)
  } catch {
    return null
  }
}

export function stringifyAdvancedAsyncConfig(config: AdvancedAsyncConfig): string {
  return JSON.stringify(normalizeAdvancedAsyncConfig(config), null, 2)
}

export function normalizeAdvancedAsyncConfig(
  config: AdvancedAsyncConfig
): AdvancedAsyncConfig {
  const submit: AdvancedAsyncSubmit = {
    upstream_path: (config.submit?.upstream_path || '').trim(),
    method: (config.submit?.method || 'POST').toUpperCase(),
    content_type: config.submit?.content_type || 'json',
    auth: normalizeAuth(config.submit?.auth),
  }
  const query: AdvancedAsyncQuery = {
    upstream_path: (config.query?.upstream_path || '').trim(),
    method: (config.query?.method || 'GET').toUpperCase(),
    task_id_param: (config.query?.task_id_param || 'task_id').trim(),
    task_id_location: config.query?.task_id_location || 'query',
    auth: normalizeAuth(config.query?.auth),
  }
  const fields = normalizeFields(config.fields)
  return {
    protocol: (config.protocol || 'custom').trim().toLowerCase(),
    incoming_paths: (config.incoming_paths || [])
      .map(normalizeIncomingPath)
      .filter(Boolean),
    submit,
    query,
    fields,
    billing: {
      mode: config.billing?.mode || 'per_call',
      models:
        config.billing?.models && Object.keys(config.billing.models).length > 0
          ? config.billing.models
          : undefined,
      duration_path: (config.billing?.duration_path || [])
        .map((s) => s.trim())
        .filter(Boolean),
      default_seconds: config.billing?.default_seconds || undefined,
      token_fallback_price: config.billing?.token_fallback_price || undefined,
    },
  }
}

/**
 * 入站路径除非带 {model} 否则是逐字相等：尾斜杠会让同一入口变成两条
 * 不同路径，保存时不报错、客户端却永远拿不到任务号。与后端 Validate 对齐。
 */
function normalizeIncomingPath(path: string): string {
  const trimmed = path.trim()
  return trimmed === '/' ? trimmed : trimmed.replace(/\/+$/, '')
}

function normalizeAuth(
  auth: AdvancedCustomRouteAuth | undefined
): AdvancedCustomRouteAuth | undefined {
  if (!auth) return undefined
  return {
    type: auth.type || 'header',
    name: (auth.name || '').trim(),
    value: (auth.value || '').trim(),
  }
}

function normalizeFields(fields: AdvancedAsyncFields | undefined): AdvancedAsyncFields {
  const statusMap: Record<string, string> = {}
  for (const [vendorStatus, gatewayStatus] of Object.entries(
    fields?.status_map || {}
  )) {
    const key = vendorStatus.trim()
    const value = gatewayStatus.trim().toUpperCase()
    if (key && value) statusMap[key] = value
  }
  return {
    task_id_path: (fields?.task_id_path || []).map((s) => s.trim()).filter(Boolean),
    status_path: (fields?.status_path || []).map((s) => s.trim()).filter(Boolean),
    result_url_path: (fields?.result_url_path || [])
      .map((s) => s.trim())
      .filter(Boolean),
    progress_path: (fields?.progress_path || [])
      .map((s) => s.trim())
      .filter(Boolean),
    error_path: (fields?.error_path || []).map((s) => s.trim()).filter(Boolean),
    completion_tokens_path: (fields?.completion_tokens_path || [])
      .map((s) => s.trim())
      .filter(Boolean),
    total_tokens_path: (fields?.total_tokens_path || [])
      .map((s) => s.trim())
      .filter(Boolean),
    status_map: Object.keys(statusMap).length > 0 ? statusMap : undefined,
  }
}

export type AdvancedAsyncValidationError = { message: string }

export function validateAdvancedAsyncConfig(
  config: AdvancedAsyncConfig | null
): AdvancedAsyncValidationError | null {
  if (!config) {
    return { message: 'Async configuration is required' }
  }
  for (const path of config.incoming_paths || []) {
    if (!path.startsWith('/')) {
      return { message: 'Incoming path must start with /' }
    }
  }
  if (!config.submit?.upstream_path?.trim()) {
    return { message: 'Submit upstream path is required' }
  }
  if (!isFullHttpURLOrAbsolutePath(config.submit.upstream_path)) {
    return {
      message: 'Submit upstream path must be a full URL or a path starting with /',
    }
  }
  if (!config.query?.upstream_path?.trim()) {
    return { message: 'Query upstream path is required' }
  }
  if (!isFullHttpURLOrAbsolutePath(config.query.upstream_path)) {
    return {
      message: 'Query upstream path must be a full URL or a path starting with /',
    }
  }
  if (
    config.query.task_id_location === 'path' &&
    !config.query.upstream_path.includes('{task_id}')
  ) {
    return {
      message: 'Query path must contain {task_id} when the id travels in the path',
    }
  }
  // 8001 只做异步，任何协议都不能接管同步入口：同步请求一旦落到异步渠道，
  // 客户端拿到的会是任务号而不是图片。
  if (
    (config.incoming_paths || []).some((path) =>
      ADVANCED_ASYNC_SYNC_ENTRY_PATHS.includes(path.trim())
    )
  ) {
    return {
      message:
        'Async channels never serve synchronous traffic; /v1/images/generations cannot be an incoming path',
    }
  }
  if (!config.fields?.task_id_path?.length) {
    return { message: 'Task id path is required' }
  }
  if (!config.fields?.status_path?.length) {
    return { message: 'Status path is required' }
  }
  for (const [vendorStatus, gatewayStatus] of Object.entries(
    config.fields.status_map || {}
  )) {
    if (!ADVANCED_ASYNC_TASK_STATUSES.includes(gatewayStatus as never)) {
      return {
        message: `Status mapping for "${vendorStatus}" is not a task status`,
      }
    }
  }
  const submitAuthError = validateAuth(config.submit.auth)
  if (submitAuthError) return { message: submitAuthError }
  const queryAuthError = validateAuth(config.query.auth)
  if (queryAuthError) return { message: queryAuthError }
  return null
}

function validateAuth(auth: AdvancedCustomRouteAuth | undefined): string | null {
  if (!auth || auth.type === 'none') return null
  if (auth.type !== 'header' && auth.type !== 'query') {
    return 'Auth type is invalid'
  }
  if (!auth.name?.trim()) return 'Auth name is required'
  if (!auth.value?.trim()) return 'Auth value is required'
  return null
}

function isFullHttpURLOrAbsolutePath(value: string): boolean {
  if (value.startsWith('/')) return !value.startsWith('//')
  try {
    const parsed = new URL(value)
    return (
      Boolean(parsed.host) &&
      (parsed.protocol === 'http:' || parsed.protocol === 'https:')
    )
  } catch {
    return false
  }
}

export function parsePathInput(value: string): string[] {
  return value
    .split(/[.,]/)
    .map((segment) => segment.trim())
    .filter(Boolean)
}

export function formatPathInput(path: string[] | undefined): string {
  return (path || []).join('.')
}

export function parseStatusMapInput(value: string): Record<string, string> {
  const map: Record<string, string> = {}
  for (const line of value.split('\n')) {
    const trimmed = line.trim()
    if (!trimmed) continue
    const separator = trimmed.includes('=') ? '=' : ':'
    const index = trimmed.indexOf(separator)
    if (index <= 0) continue
    const key = trimmed.slice(0, index).trim()
    const status = trimmed.slice(index + 1).trim().toUpperCase()
    if (key && status) map[key] = status
  }
  return map
}

export function formatStatusMapInput(
  map: Record<string, string> | undefined
): string {
  return Object.entries(map || {})
    .map(([key, value]) => `${key} = ${value}`)
    .join('\n')
}

// APIDUO:advanced-async-preset:BEGIN
//
// 协议预设：面板让人回答的是「这条渠道说哪一套内置协议」，而不是「对方
// query 端点的状态字段在第几层 dot-path」。协议固定的是**形状**与默认值，
// 不固定地址 —— 同一套形状在不同供应商那里路径并不一致，所以上下游端点
// 始终可改，预设只是把常见值填好。
export type AdvancedAsyncProtocol = 'images' | 'ark' | 'custom'

export type AdvancedAsyncProtocolPreset = {
  value: AdvancedAsyncProtocol
  label: string
  description: string
  /** Custom keeps whatever the operator already typed. */
  config?: AdvancedAsyncConfig
}

// 内置 images 形状：提交拿任务号，GET <入站>/{id} 拿标准 Images 响应。
// 入站默认 /v1/images/generations/async，与同步入口分开。
const IMAGES_CONFIG: AdvancedAsyncConfig = {
  protocol: 'images',
  incoming_paths: [ADVANCED_ASYNC_IMAGE_ENTRY_PATH],
  submit: {
    upstream_path: '/v1/images/generations',
    method: 'POST',
    content_type: 'json',
    auth: { type: 'header', name: 'Authorization', value: 'Bearer {api_key}' },
  },
  query: {
    upstream_path: '/v1/images/generations/{task_id}',
    method: 'GET',
    task_id_param: 'task_id',
    task_id_location: 'path',
    auth: { type: 'header', name: 'Authorization', value: 'Bearer {api_key}' },
  },
  fields: {
    task_id_path: ['id'],
    status_path: ['status'],
    result_url_path: ['data', '0', 'url'],
    error_path: ['error', 'message'],
    status_map: {
      queued: 'QUEUED',
      pending: 'QUEUED',
      in_progress: 'IN_PROGRESS',
      processing: 'IN_PROGRESS',
      completed: 'SUCCESS',
      succeeded: 'SUCCESS',
      failed: 'FAILURE',
    },
  },
  billing: { mode: 'per_call' },
}

// 内置方舟任务形状：提交与查询都在 /api/v3/contents/generations/tasks，
// 入站沿用网关自有的 /v1/video/generations；方舟原生路径由别名转发过来。
const ARK_CONFIG: AdvancedAsyncConfig = {
  protocol: 'ark',
  incoming_paths: [...ADVANCED_ASYNC_ARK_ENTRY_PATHS],
  submit: {
    upstream_path: '/api/v3/contents/generations/tasks',
    method: 'POST',
    content_type: 'json',
    auth: { type: 'header', name: 'Authorization', value: 'Bearer {api_key}' },
  },
  query: {
    upstream_path: '/api/v3/contents/generations/tasks/{task_id}',
    method: 'GET',
    task_id_param: 'task_id',
    task_id_location: 'path',
    auth: { type: 'header', name: 'Authorization', value: 'Bearer {api_key}' },
  },
  fields: {
    task_id_path: ['id'],
    status_path: ['status'],
    result_url_path: ['content', 'video_url'],
    error_path: ['error', 'message'],
    status_map: {
      queued: 'QUEUED',
      running: 'IN_PROGRESS',
      succeeded: 'SUCCESS',
      failed: 'FAILURE',
      cancelled: 'FAILURE',
      expired: 'FAILURE',
    },
  },
  billing: {
    mode: 'per_second',
    duration_path: ['duration'],
    default_seconds: 5,
  },
}

export const ADVANCED_ASYNC_PROTOCOLS: AdvancedAsyncProtocolPreset[] = [
  {
    value: 'images',
    label: 'Images',
    description:
      'Submit answers a task id; GET <incoming>/{id} answers a standard Images response.',
    config: IMAGES_CONFIG,
  },
  {
    value: 'ark',
    label: 'Ark',
    description:
      'Volcengine Ark task API: POST /api/v3/contents/generations/tasks, polled by task id.',
    config: ARK_CONFIG,
  },
]

// 本期只提供两种客户端形状：Images 与 Ark。两者都只要求「上游本身返回任务号」，
// 差别只在轮询回来的响应长什么样。custom 不再作为可选项 —— 历史配置（字段缺失
// 或写成 custom）仍要能打开，所以单独留一条只读项。
export const ADVANCED_ASYNC_LEGACY_PROTOCOL: AdvancedAsyncProtocolPreset = {
  value: 'custom',
  label: 'Custom (legacy)',
  description:
    'Saved before the protocol was narrowed to Images and Ark. Pick one of them to rewrite the shape.',
}

// detectAdvancedAsyncProtocol reads the persisted protocol first and only
// falls back to endpoint heuristics for configs written before it existed.
export function detectAdvancedAsyncProtocol(
  config: AdvancedAsyncConfig
): AdvancedAsyncProtocol {
  if (!config) return 'custom'
  const declared = (config.protocol || '').trim().toLowerCase()
  if (declared === 'images' || declared === 'ark') return declared
  const upstream = (config.submit?.upstream_path || '').trim()
  if (upstream === '/api/v3/contents/generations/tasks') return 'ark'
  if (upstream.startsWith(ADVANCED_ASYNC_SYNC_IMAGES_PATH)) return 'images'
  return 'custom'
}

// applyAdvancedAsyncProtocol swaps in a preset while keeping the credentials
// the operator already typed. Auth is the one field that is never safe to
// reset: it usually carries a hand-written template, not the {api_key} default.
export function applyAdvancedAsyncProtocol(
  current: AdvancedAsyncConfig,
  protocol: AdvancedAsyncProtocol
): AdvancedAsyncConfig {
  const preset = ADVANCED_ASYNC_PROTOCOLS.find(
    (candidate) => candidate.value === protocol
  )
  if (!preset?.config) return current
  return normalizeAdvancedAsyncConfig({
    ...preset.config,
    protocol,
    submit: { ...preset.config.submit, auth: current.submit?.auth },
    query: { ...preset.config.query, auth: current.query?.auth },
  })
}
// APIDUO:advanced-async-preset:END

export function getAdvancedAsyncStats(value: string | undefined): {
  configured: boolean
  valid: boolean
  incomingPaths: string[]
} {
  const config = parseAdvancedAsyncConfig(value)
  if (!config) return { configured: false, valid: false, incomingPaths: [] }
  return {
    configured: true,
    valid: validateAdvancedAsyncConfig(config) === null,
    incomingPaths: config.incoming_paths || [],
  }
}
