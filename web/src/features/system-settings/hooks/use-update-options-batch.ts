import { useMutation, useQueryClient } from '@tanstack/react-query'
import i18next from 'i18next'
import { toast } from 'sonner'

import { handleServerError } from '@/lib/handle-server-error'
import { requireServerSuccess } from '@/lib/server-error-message'

import {
  updateSystemOptionsBatch,
  type BatchOptionUpdateRequest,
} from '../api-apiduo-batch'

/**
 * Saves a settings form's dirty fields in one request.
 *
 * The per-key hook writes one audit event per field, so a ten-field form left
 * ten audit rows, ten success toasts and ten full /api/option/ refetches behind
 * a single click. One save is one action: this keeps it to one request, one
 * toast and one refresh.
 */
export function useUpdateOptionsBatch() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async (request: BatchOptionUpdateRequest) =>
      requireServerSuccess(await updateSystemOptionsBatch(request)),
    onSuccess: (data) => {
      if (!data.success) return
      queryClient.invalidateQueries({ queryKey: ['system-options'] })
      // Appearance and storage keys both feed the public status payload. The
      // other settings pages do the same for their display keys, and refreshing
      // one extra cached query is cheaper than a stale header.
      queryClient.invalidateQueries({ queryKey: ['status'] })
      queryClient.invalidateQueries({ queryKey: ['apiduo-appearance'] })
      try {
        window.localStorage.removeItem('status')
      } catch {
        /* Storage may be disabled. */
      }
      toast.success(i18next.t('Setting updated successfully'))
    },
    onError: (error: Error) =>
      handleServerError(error, i18next.t('Failed to update setting')),
  })
}
