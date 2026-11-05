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
import {
  AuthOperationError,
  authRequestOptions,
  authResult,
} from '@/lib/secure-verification'

// APIDUO:agent-key:BEGIN
export interface AgentKeyItem {
  id: number
  name: string
  public_key: string
  key_type: string
  /** Unix seconds; 0 means the key never expires. */
  expires_at: number
  /** Unix seconds; non-zero means the key can no longer be exchanged. */
  revoked_at: number
  last_used_at: number
  last_used_ip: string
  created_at: number
}

export interface AgentKeyInput {
  name: string
  public_key: string
  scopes: string[]
  expires_at: number
}

export async function listAgentKeys(signal?: AbortSignal) {
  const res = await api.get<{ success: boolean; data: AgentKeyItem[] }>(
    '/api/agent/keys',
    { signal }
  )
  return res.data.data ?? []
}

export async function createAgentKey(
  input: AgentKeyInput,
  proofToken: string,
  signal: AbortSignal
): Promise<AgentKeyItem> {
  const created = await authResult<AgentKeyItem>(
    api.post('/api/agent/keys', input, {
      ...authRequestOptions,
      headers: { 'X-Security-Proof': proofToken },
      singleUseAuthorization: true,
      signal,
    }),
    'Failed to register the key'
  )
  if (!created?.id) throw new AuthOperationError('Failed to register the key')
  return created
}

export async function revokeAgentKey(
  id: number,
  proofToken: string,
  signal: AbortSignal
): Promise<void> {
  await authResult(
    api.delete(`/api/agent/keys/${id}`, {
      ...authRequestOptions,
      headers: { 'X-Security-Proof': proofToken },
      singleUseAuthorization: true,
      signal,
    }),
    'Failed to revoke the key'
  )
}
// APIDUO:agent-key:END
