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
 * Trace jump-link construction.
 *
 * Pure functions only, so the URL shape is unit-testable and backend e2e
 * tests can assert on the same values. Keeping this in lib (not inside the
 * component) is the extension point: pointing the link at Phoenix or Tempo
 * later means changing the template here, nothing else.
 */

export interface TraceConfig {
  enabled: boolean
  host: string
  project_id: string
  body_mode: 'off' | 'headtail' | 'full'
  sample_rate: number
}

/**
 * Build the Langfuse trace URL. Returns null when any piece is missing —
 * callers render nothing rather than a dead link.
 *
 * Trace ids are 32 hex chars; the trace_id from the log payload is used
 * verbatim (the backend derives it from the request id, the frontend never
 * re-derives — one source of truth, no drift).
 */
export function buildTraceUrl(
  config: Pick<TraceConfig, 'enabled' | 'host' | 'project_id'>,
  traceId?: string
): string | null {
  if (!config.enabled || !config.host || !config.project_id) return null
  const id = traceId?.trim()
  if (!id || !/^[0-9a-f]{32}$/i.test(id)) return null
  const host = config.host.replace(/\/+$/, '')
  return `${host}/project/${encodeURIComponent(config.project_id)}/traces/${id}`
}

/**
 * Deep link that lands on the exact span, not on the trace list.
 *
 * The peek parameters are what Langfuse's own table sets when you click a row:
 * `peek`/`observation` open the span detail, `traceId` scopes the list,
 * `traceTab=attributes` skips the default overview. Without `peek` the URL
 * only filters the list, and the operator has to find the row again — on a
 * busy trace list that is a second navigation worth saving.
 */
export function buildDeepTraceUrl(
  config: Pick<TraceConfig, 'enabled' | 'host' | 'project_id'>,
  traceId: string | undefined,
  observationId: string | undefined,
  startTime: string | undefined
): string | null {
  if (!config.enabled || !config.host || !config.project_id) return null
  const trace = traceId?.trim()
  const span = observationId?.trim()
  if (!trace || !/^[0-9a-f]{32}$/i.test(trace)) return null
  if (!span) return null

  const host = config.host.replace(/\/+$/, '')
  const q = new URLSearchParams({
    peek: span,
    observation: span,
    traceId: trace,
    traceTab: 'attributes',
  })
  if (startTime) q.set('timestamp', startTime)
  return `${host}/project/${encodeURIComponent(config.project_id)}/traces?${q.toString()}`
}
