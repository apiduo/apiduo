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
import { api } from '@/lib/api'

import type { RequestLogRecord } from './types'

const LOG_LOOKUP_PATH = '/api/log/self'
const POLL_INTERVAL_MS = 200
const POLL_ATTEMPTS = 10

function toNumber(value: unknown): number {
  const num = typeof value === 'number' ? value : Number(value)
  return Number.isFinite(num) ? num : 0
}

function parseOther(raw: unknown): Record<string, unknown> | null {
  if (!raw) return null
  if (typeof raw === 'object') return raw as Record<string, unknown>
  if (typeof raw !== 'string') return null
  try {
    const parsed: unknown = JSON.parse(raw)
    return parsed && typeof parsed === 'object'
      ? (parsed as Record<string, unknown>)
      : null
  } catch {
    return null
  }
}

function parseRecord(item: Record<string, unknown>): RequestLogRecord | null {
  const requestId = typeof item.request_id === 'string' ? item.request_id : ''
  if (!requestId) return null

  return {
    quota: toNumber(item.quota),
    promptTokens: toNumber(item.prompt_tokens),
    completionTokens: toNumber(item.completion_tokens),
    useTime: toNumber(item.use_time),
    channelId: toNumber(item.channel),
    channelName: typeof item.channel_name === 'string' ? item.channel_name : '',
    requestId,
    other: parseOther(item.other),
  }
}

/**
 * 消费日志是异步落库的，请求刚结束时通常查不到。
 * 这里只做一次点查，轮询策略在 `waitForRequestLog` 里。
 */
export async function fetchRequestLog(
  requestId: string
): Promise<RequestLogRecord | null> {
  if (!requestId) return null

  const res = await api.get(LOG_LOOKUP_PATH, {
    params: { request_id: requestId, page: 1, page_size: 1, type: 2 },
    skipErrorHandler: true,
  })

  const payload = (res.data ?? {}) as Record<string, unknown>
  if (payload.success !== true) return null

  const page = (payload.data ?? {}) as Record<string, unknown>
  const items = Array.isArray(page.items) ? page.items : []
  const first = items[0]
  if (!first || typeof first !== 'object') return null

  const record = parseRecord(first as Record<string, unknown>)
  // 同一个 request_id 可能先命中别的类型日志，只认消费日志
  return record && record.quota >= 0 ? record : null
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => {
    window.setTimeout(resolve, ms)
  })
}

/**
 * 轮询直到拿到消费日志。超时返回 null，调用方据此降级为「估算」——
 * 宁可显示带标注的估算值，也不能把估算冒充成实际扣费。
 */
export async function waitForRequestLog(
  requestId: string,
  isCancelled: () => boolean = () => false
): Promise<RequestLogRecord | null> {
  if (!requestId) return null

  for (let attempt = 0; attempt < POLL_ATTEMPTS; attempt += 1) {
    if (isCancelled()) return null
    try {
      const record = await fetchRequestLog(requestId)
      if (record) return record
    } catch {
      // 日志库偶发不可用不应打断对话，继续重试
    }
    if (attempt < POLL_ATTEMPTS - 1) await sleep(POLL_INTERVAL_MS)
  }
  return null
}
