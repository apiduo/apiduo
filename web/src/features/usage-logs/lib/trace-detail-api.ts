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
along with this program. If not <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
/**
 * Trace detail read, kept out of the shared api.ts.
 *
 * Reasons this is its own module:
 *   1. api.ts is an upstream file; every line added there is merge debt. The
 *      trace feature can own a new file instead.
 *   2. The shape below is a whitelist mirrored from pkg/apiduo/trace/fetch.go,
 *      not the Langfuse payload. Keeping both sides in one small file makes a
 *      mismatch obvious during review.
 */
import { api } from '@/lib/api'

export interface TraceObservation {
  /** Langfuse "t-…" span id; drives the peek deep link. */
  observation_id: string
  trace_id: string
  name: string
  type: string
  level: string
  status_message: string
  start_time: string
  end_time: string
  latency_s: number
  ttft_s: number
  model: string
  input: string
  output: string
  prompt_tokens: number
  completion_tokens: number
  environment: string
  release: string
  content_sampled: boolean
  request_id: string
  channel_id: string
  group: string
  token_name: string
  format: string
}

/**
 * `reason` separates the two normal empty states from a real failure:
 * "disabled" (tracing is off) and "not_found" (sampled out, or aged out of
 * retention). Both render as an explanation, never as an error toast.
 */
export interface TraceDetailResult {
  observation: TraceObservation | null
  reason?: 'disabled' | 'not_found'
}

export async function getTraceDetail(
  traceId: string
): Promise<TraceDetailResult> {
  const res = await api.get<{
    success: boolean
    data?: TraceObservation | null
    reason?: 'disabled' | 'not_found'
  }>(`/api/apiduo/trace/detail/${encodeURIComponent(traceId)}`)
  return { observation: res.data?.data ?? null, reason: res.data?.reason }
}
