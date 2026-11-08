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
import type { ChatUsageTokens } from './types'

/** 网关在响应头回传本次请求号，用它反查消费日志 */
export const REQUEST_ID_HEADER = 'X-Oneapi-Request-Id'

function toNumber(value: unknown): number {
  const num = typeof value === 'number' ? value : Number(value)
  return Number.isFinite(num) ? num : 0
}

function readPath(source: Record<string, unknown>, path: string[]): unknown {
  let node: unknown = source
  for (const key of path) {
    if (!node || typeof node !== 'object') return undefined
    node = (node as Record<string, unknown>)[key]
  }
  return node
}

/**
 * 上游不是所有渠道都按 OpenAI 语义报 usage：
 * Responses 风格给 input_tokens/output_tokens，Claude 走 cache_creation。
 * 这里统一折算成计费用的三个数，缺的按 0 处理。
 */
export function parseUsage(raw: unknown): ChatUsageTokens | null {
  if (!raw || typeof raw !== 'object') return null
  const usage = raw as Record<string, unknown>

  const promptTokens = Math.max(
    toNumber(usage.prompt_tokens),
    toNumber(usage.input_tokens)
  )
  const completionTokens = Math.max(
    toNumber(usage.completion_tokens),
    toNumber(usage.output_tokens)
  )
  const cacheTokens = Math.max(
    toNumber(readPath(usage, ['prompt_tokens_details', 'cached_tokens'])),
    toNumber(readPath(usage, ['input_tokens_details', 'cached_tokens']))
  )

  if (promptTokens === 0 && completionTokens === 0 && cacheTokens === 0) {
    return null
  }
  return { promptTokens, completionTokens, cacheTokens }
}

/**
 * 流式响应的 usage 只在最后一块 chunk 里，且必须请求时带
 * `stream_options.include_usage` 才会下发。之前的实现直接丢弃整块 payload，
 * 所以 Playground 一直拿不到 token 数。
 */
export function extractUsageFromChunk(data: string): ChatUsageTokens | null {
  if (!data || data === '[DONE]') return null

  let parsed: unknown
  try {
    parsed = JSON.parse(data)
  } catch {
    return null
  }
  if (!parsed || typeof parsed !== 'object') return null

  const payload = parsed as Record<string, unknown>
  const direct = parseUsage(payload.usage)
  if (direct) return direct

  // 少数渠道把 usage 塞进 choices[0].usage 或 x_groq.usage
  const choices = Array.isArray(payload.choices) ? payload.choices : []
  for (const choice of choices) {
    if (!choice || typeof choice !== 'object') continue
    const nested = parseUsage((choice as Record<string, unknown>).usage)
    if (nested) return nested
  }
  return parseUsage(readPath(payload, ['x_groq', 'usage']))
}

export function readRequestId(
  headers: Headers | Record<string, string> | null | undefined
): string {
  if (!headers) return ''
  if (typeof (headers as Headers).get === 'function') {
    return (headers as Headers).get(REQUEST_ID_HEADER) ?? ''
  }
  const record = headers as Record<string, string>
  for (const [key, value] of Object.entries(record)) {
    if (key.toLowerCase() === REQUEST_ID_HEADER.toLowerCase()) return value
  }
  return ''
}
