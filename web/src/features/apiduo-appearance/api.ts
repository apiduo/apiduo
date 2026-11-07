import { api } from '@/lib/api'

import type { ApiduoAppearanceResponse } from './types'

/**
 * The decoration apiduo paints on its own surfaces.
 *
 * Public endpoint: the shell renders apiduo surfaces before a session exists,
 * and the reply is a colour plus three numbers.
 */
export async function getApiduoAppearance(): Promise<ApiduoAppearanceResponse> {
  const res = await api.get<ApiduoAppearanceResponse>('/api/appearance')
  return res.data
}
