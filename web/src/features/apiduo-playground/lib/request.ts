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
import { getFreshAuthHeaders } from '@/lib/api'

import { readRequestId } from '../billing/usage'

export interface JsonResult<T> {
  data: T | null
  requestId: string
  error?: string
  status: number
}

async function parseError(response: Response): Promise<string> {
  try {
    const text = await response.text()
    if (!text) return `HTTP ${response.status}`
    try {
      const parsed = JSON.parse(text) as Record<string, unknown>
      const error = parsed.error
      if (error && typeof error === 'object') {
        const detail = error as Record<string, unknown>
        if (typeof detail.message === 'string') return detail.message
      }
      if (typeof parsed.message === 'string') return parsed.message
    } catch {
      return text.slice(0, 400)
    }
    return text.slice(0, 400)
  } catch {
    return `HTTP ${response.status}`
  }
}

async function request<T>(
  method: 'GET' | 'POST',
  path: string,
  body?: unknown
): Promise<JsonResult<T>> {
  let headers: Record<string, string>
  try {
    headers = await getFreshAuthHeaders()
  } catch (error) {
    return {
      data: null,
      requestId: '',
      status: 0,
      error: error instanceof Error ? error.message : 'Request failed',
    }
  }

  const init: RequestInit = {
    method,
    headers: body
      ? { ...headers, 'Content-Type': 'application/json' }
      : headers,
    credentials: 'include',
  }
  // GET 不能带 body，拆开写而不是传 undefined —— 后者仍会被 lint 判为非法选项
  if (method === 'GET') {
    delete init.body
  } else if (body !== undefined) {
    init.body = JSON.stringify(body)
  }

  const response = await fetch(path, init)

  const requestId = readRequestId(response.headers)

  if (!response.ok) {
    return {
      data: null,
      requestId,
      status: response.status,
      error: await parseError(response),
    }
  }

  try {
    const data = (await response.json()) as T
    return { data, requestId, status: response.status }
  } catch {
    return {
      data: null,
      requestId,
      status: response.status,
      error: 'Error parsing response data',
    }
  }
}

export function postJson<T>(path: string, body: unknown): Promise<JsonResult<T>> {
  return request<T>('POST', path, body)
}

/**
 * Multipart 的错误面与 JSON 完全一致，差别只有两处：body 是 FormData，
 * 且 Content-Type 必须留给浏览器写——手填 `multipart/form-data` 会丢
 * boundary，上游 400 还看不出原因。
 */
export async function postForm<T>(
  path: string,
  form: FormData
): Promise<JsonResult<T>> {
  let headers: Record<string, string>
  try {
    headers = await getFreshAuthHeaders()
  } catch (error) {
    return {
      data: null,
      requestId: '',
      status: 0,
      error: error instanceof Error ? error.message : 'Request failed',
    }
  }
  delete headers['Content-Type']

  const response = await fetch(path, {
    method: 'POST',
    headers,
    credentials: 'include',
    body: form,
  })
  const requestId = readRequestId(response.headers)
  if (!response.ok) {
    return {
      data: null,
      requestId,
      status: response.status,
      error: await parseError(response),
    }
  }
  try {
    const data = (await response.json()) as T
    return { data, requestId, status: response.status }
  } catch {
    return { data: null, requestId, status: response.status, error: 'Error parsing response data' }
  }
}

export function getJson<T>(path: string): Promise<JsonResult<T>> {
  return request<T>('GET', path)
}
