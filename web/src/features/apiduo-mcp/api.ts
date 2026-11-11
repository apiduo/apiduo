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

/**
 * A token as the list endpoint returns it. `key` is masked server-side
 * (`sk-R**********S5aG`); the plaintext only comes from `getTokenKey`, one
 * token at a time, and that read is written to the token audit log.
 */
export interface McpToken {
  id: number
  name: string
  key: string
  status: number
  expired_time: number
  remain_quota: number
  unlimited_quota: boolean
  allow_ips?: string | null
  accessed_time: number
}

/** Tokens the page may preselect: enabled, unexpired, funded, no IP allowlist. */
export function isUsable(token: McpToken): boolean {
  if (token.status !== 1) return false
  if (token.expired_time !== -1 && token.expired_time < Date.now() / 1000) {
    return false
  }
  if (!token.unlimited_quota && token.remain_quota <= 0) return false
  return !token.allow_ips
}

/** Why a token cannot be used as-is, or an empty string when it is fine. */
export function describeBlocker(
  token: McpToken,
  labels: {
    disabled: string
    expired: string
    exhausted: string
    ipAllowlist: string
  }
): string {
  if (token.status !== 1) return labels.disabled
  if (token.expired_time !== -1 && token.expired_time < Date.now() / 1000) {
    return labels.expired
  }
  if (!token.unlimited_quota && token.remain_quota <= 0) return labels.exhausted
  if (token.allow_ips) return labels.ipAllowlist
  return ''
}

export async function listTokens(): Promise<McpToken[]> {
  const res = await api.get('/api/token/?p=1&size=100')
  return res.data?.data?.items ?? []
}

export async function getTokenKey(id: number): Promise<string> {
  const res = await api.post(`/api/token/${id}/key`)
  const key: string = res.data?.data?.key ?? ''
  if (!key) return ''
  // The endpoint hands back the bare key; every client, and the gateway's own
  // auth middleware, expect the `sk-` prefix.
  return key.startsWith('sk-') ? key : `sk-${key}`
}

/**
 * Create a token that works for MCP out of the box: no expiry, no quota cap,
 * no IP allowlist. Returns the new id so the caller can select and read it.
 */
export async function createMcpToken(name: string): Promise<number> {
  const res = await api.post('/api/token/', {
    name,
    unlimited_quota: true,
    expired_time: -1,
    remain_quota: 0,
    allow_ips: '',
    model_limits_enabled: false,
    model_limits: '',
  })
  return res.data?.data?.id ?? 0
}
