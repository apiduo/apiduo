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

export const ADVANCED_ASYNC_DEFAULT_INCOMING_PATH = '/v1/video/generations'

export function createAdvancedAsyncConfig(): AdvancedAsyncConfig {
  return {
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
    incoming_paths: (config.incoming_paths || [])
      .map((path) => path.trim())
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
