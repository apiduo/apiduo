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
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback, useEffect, useRef, useState } from 'react'

import {
  useSecureVerification,
  type RequestVerificationOptions,
} from '@/features/auth/secure-verification'
import { handleServerError } from '@/lib/handle-server-error'
import { AuthOperationError } from '@/lib/secure-verification'
import { useAuthStore } from '@/stores/auth-store'

import {
  createAgentKey,
  listAgentKeys,
  revokeAgentKey,
  type AgentKeyInput,
} from '../agent-keys-api'

// APIDUO:agent-key:BEGIN
/**
 * Manages the caller's agent keys.
 *
 * Registering and revoking both mint or destroy a credential, so each goes
 * through the same step-up verification as personal access tokens. The key
 * material itself is public, so listing needs no proof.
 */
export function useAgentKeys() {
  const client = useQueryClient()
  const userId = useAuthStore((state) => state.auth.user?.id)
  const sessionId = useAuthStore((state) => state.auth.session?.sid)
  const [pending, setPending] = useState(false)
  const currentOperation = useRef<AbortController | null>(null)
  const verification = useSecureVerification()
  const requestVerification = verification.requestVerification
  const cancelVerification = verification.cancel

  const list = useQuery({
    queryKey: ['security', 'agent-keys', userId],
    queryFn: ({ signal }) => listAgentKeys(signal),
    retry: false,
  })

  const invalidateList = useCallback(
    () => client.invalidateQueries({ queryKey: ['security', 'agent-keys', userId] }),
    [client, userId]
  )

  useEffect(
    () => () => {
      const current = currentOperation.current
      currentOperation.current = null
      current?.abort()
      cancelVerification()
      setPending(false)
    },
    [cancelVerification, userId, sessionId]
  )

  const runVerified = useCallback(
    async <T,>(
      operation: RequestVerificationOptions,
      action: (proofToken: string, signal: AbortSignal) => Promise<T>
    ): Promise<T | undefined> => {
      if (currentOperation.current) return undefined
      const controller = new AbortController()
      currentOperation.current = controller
      setPending(true)
      try {
        const proof = await requestVerification(operation)
        if (currentOperation.current !== controller || !proof) return undefined
        const result = await action(proof.proof_token, controller.signal)
        if (currentOperation.current !== controller) return undefined
        void invalidateList()
        return result
      } catch (error) {
        if (currentOperation.current !== controller) return undefined
        const failure = AuthOperationError.from(error)
        if (failure.code !== 'AUTH_CANCELLED') handleServerError(failure)
        void invalidateList()
        return undefined
      } finally {
        if (currentOperation.current === controller) {
          currentOperation.current = null
          setPending(false)
        }
      }
    },
    [requestVerification, invalidateList]
  )

  const create = useCallback(
    async (input: AgentKeyInput) => {
      const created = await runVerified(
        {
          scope: 'agent_key.register',
          context: {
            scopes: input.scopes,
            expires_at: input.expires_at,
            // Must match the server's fingerprint of the submitted key,
            // otherwise the proof will not bind to this request.
            public_key_id: await fingerprintPublicKey(input.public_key),
          },
        },
        (proofToken, signal) => createAgentKey(input, proofToken, signal)
      )
      return Boolean(created)
    },
    [runVerified]
  )

  const revoke = useCallback(
    async (id: number) => {
      const done = await runVerified(
        { scope: 'agent_key.revoke', context: { key_id: id } },
        (proofToken, signal) => revokeAgentKey(id, proofToken, signal)
      )
      return done !== undefined
    },
    [runVerified]
  )

  const verificationPhase = verification.dialogProps.state.phase
  return {
    list,
    pending,
    create,
    revoke,
    maxKeys: 20,
    showVerification:
      verificationPhase !== 'idle' && verificationPhase !== 'loading',
    verificationDialogProps: verification.dialogProps,
  }
}

/**
 * Mirrors the server's SHA-256 based fingerprint (first 8 bytes, hex).
 *
 * WebCrypto is not available in insecure contexts, but this path only runs on
 * the console, which is served over TLS or localhost.
 */
async function fingerprintPublicKey(raw: string): Promise<string> {
  const digest = await crypto.subtle.digest(
    'SHA-256',
    new TextEncoder().encode(raw.trim())
  )
  const bytes = new Uint8Array(digest)
  let out = ''
  for (let index = 0; index < 8; index += 1) {
    out += bytes[index].toString(16).padStart(2, '0')
  }
  return out
}
// APIDUO:agent-key:END
