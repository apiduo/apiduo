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
import {
  isStreamDoneMessage,
  parseStreamMessageUpdates,
} from '@/features/playground/lib'

import type { ChatCompletionResponse } from '@/features/playground/types'

import { extractUsageFromChunk, readRequestId } from '../billing/usage'
import type { ChatUsageTokens } from '../billing/types'

export interface StreamCallbacks {
  onUpdate: (type: 'reasoning' | 'content', chunk: string) => void
  onUsage: (usage: ChatUsageTokens) => void
  onComplete: (meta: { requestId: string; sawDone: boolean }) => void
  onError: (message: string, errorCode?: string) => void
}

const STREAM_ENDPOINT = '/pg/chat/completions'

/** 帧之间可能被任意切块，必须留尾 */
interface FrameBuffer {
  rest: string
}

function takeFrames(buffer: FrameBuffer): string[] {
  const frames: string[] = []
  let text = buffer.rest

  for (;;) {
    const boundary = text.indexOf('\n\n')
    if (boundary === -1) break
    frames.push(text.slice(0, boundary))
    text = text.slice(boundary + 2)
  }

  buffer.rest = text
  return frames
}

function readFramePayload(frame: string): string | null {
  if (!frame.trim()) return null

  const dataLines: string[] = []
  for (const line of frame.split('\n')) {
    const trimmed = line.trimStart()
    if (!trimmed.startsWith('data:')) continue
    dataLines.push(trimmed.slice(5).trimStart())
  }
  if (dataLines.length === 0) return null
  return dataLines.join('\n')
}

async function readErrorMessage(response: Response): Promise<[string, string?]> {
  try {
    const text = await response.text()
    if (!text) return [`HTTP ${response.status}`]
    try {
      const parsed = JSON.parse(text) as Record<string, unknown>
      const error = parsed.error
      if (error && typeof error === 'object') {
        const detail = error as Record<string, unknown>
        const message =
          typeof detail.message === 'string' ? detail.message : text
        const code = typeof detail.code === 'string' ? detail.code : undefined
        return [message, code]
      }
      if (typeof parsed.message === 'string') return [parsed.message]
    } catch {
      return [text.slice(0, 500)]
    }
    return [text.slice(0, 500)]
  } catch {
    return [`HTTP ${response.status}`]
  }
}

/**
 * 用 fetch 而不是 sse.js，唯一原因是**要读响应头**：
 * `X-Oneapi-Request-Id` 是反查消费日志、把「估算」变成「已对账」的唯一凭据，
 * EventSource 封装拿不到它。
 */
export async function streamChatCompletion(
  payload: unknown,
  signal: AbortSignal,
  callbacks: StreamCallbacks
): Promise<void> {
  let headers: Record<string, string>
  try {
    headers = await getFreshAuthHeaders()
  } catch (error) {
    callbacks.onError(
      error instanceof Error ? error.message : 'Error establishing connection'
    )
    return
  }

  let response: Response
  try {
    response = await fetch(STREAM_ENDPOINT, {
      method: 'POST',
      headers: { ...headers, 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
      signal,
      credentials: 'include',
    })
  } catch (error) {
    if (signal.aborted) return
    callbacks.onError(
      error instanceof Error
        ? error.message
        : 'Network connection failed or server not responding'
    )
    return
  }

  const requestId = readRequestId(response.headers)

  if (!response.ok) {
    const [message, code] = await readErrorMessage(response)
    callbacks.onError(message, code)
    return
  }

  if (!response.body) {
    callbacks.onError('Connection closed')
    return
  }

  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  const buffer: FrameBuffer = { rest: '' }
  let usage: ChatUsageTokens | null = null
  let sawDone = false

  try {
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break

      buffer.rest += decoder.decode(value, { stream: true })

      for (const frame of takeFrames(buffer)) {
        const data = readFramePayload(frame)
        if (data === null) continue

        if (isStreamDoneMessage(data)) {
          sawDone = true
          continue
        }

        const frameUsage = extractUsageFromChunk(data)
        if (frameUsage) usage = frameUsage

        try {
          for (const update of parseStreamMessageUpdates(data)) {
            callbacks.onUpdate(update.type, update.chunk)
          }
        } catch (error) {
          // 单块解析失败不该中断整条流，内容已经在前面推给界面了
          // eslint-disable-next-line no-console
          console.error('Failed to parse SSE message:', error)
        }
      }
    }

    // 收尾块可能没有空行结尾，补一次
    const tail = buffer.rest + decoder.decode()
    if (tail.trim()) {
      const data = readFramePayload(tail)
      if (data && !isStreamDoneMessage(data)) {
        const frameUsage = extractUsageFromChunk(data)
        if (frameUsage) usage = frameUsage
        try {
          for (const update of parseStreamMessageUpdates(data)) {
            callbacks.onUpdate(update.type, update.chunk)
          }
        } catch {
          // 同上，静默丢弃
        }
      }
    }

    if (signal.aborted) return
    if (usage) callbacks.onUsage(usage)
    callbacks.onComplete({ requestId, sawDone })
  } catch (error) {
    if (signal.aborted) return
    callbacks.onError(
      error instanceof Error ? error.message : 'Generation was interrupted'
    )
  } finally {
    try {
      reader.releaseLock()
    } catch {
      // reader 已释放
    }
  }
}

/** 非流式请求：一次拿完，usage 与 request id 都在响应里 */
export async function fetchChatCompletion(
  payload: unknown,
  signal: AbortSignal
): Promise<{
  body: ChatCompletionResponse | null
  requestId: string
  error?: [string, string?]
}> {
  let headers: Record<string, string>
  try {
    headers = await getFreshAuthHeaders()
  } catch (error) {
    return {
      body: null,
      requestId: '',
      error: [error instanceof Error ? error.message : 'Request failed'],
    }
  }

  const response = await fetch(STREAM_ENDPOINT, {
    method: 'POST',
    headers: { ...headers, 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
    signal,
    credentials: 'include',
  })

  const requestId = readRequestId(response.headers)
  if (!response.ok) {
    const detail = await readErrorMessage(response)
    return { body: null, requestId, error: detail }
  }

  try {
    const body = (await response.json()) as ChatCompletionResponse
    return { body, requestId }
  } catch {
    return { body: null, requestId, error: ['Error parsing response data'] }
  }
}
