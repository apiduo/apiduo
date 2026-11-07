import { api } from '@/lib/api'

export type BatchOptionValues = Record<string, string | boolean | number>

export type BatchOptionUpdateRequest = {
  options: BatchOptionValues
}

export type BatchOptionUpdateResponse = {
  success: boolean
  message?: string
  data?: {
    saved?: string[]
    count?: number
    // Present when the server rejected the batch halfway through: values
    // written before the failure stay applied, so the caller learns which ones.
    applied?: string[]
    failed?: string
  }
}

export async function updateSystemOptionsBatch(
  request: BatchOptionUpdateRequest
) {
  const res = await api.put<BatchOptionUpdateResponse>(
    '/api/option/batch',
    request
  )
  return res.data
}
