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

export type BatchOptionResponse = {
  success: boolean
  message?: string
  data?: { saved?: string[]; count?: number }
}

/**
 * Saves several option rows as one transaction.
 *
 * Lives in its own file rather than next to `updateSystemOption`: a settings
 * form that writes six fields through the per-key endpoint pays six round
 * trips and, worse, commits each key on its own — a rejected value in the
 * middle leaves the form half applied (new rates, old rules). The batch
 * endpoint validates the whole set first and writes it in one transaction.
 *
 * Keeping it out of `api.ts` also keeps that upstream file free of apiduo
 * lines, so a rebase never has to resolve a conflict over a helper.
 */
export async function updateSystemOptionsBatch(
  options: Record<string, string>
) {
  const res = await api.put<BatchOptionResponse>('/api/option/batch', {
    options,
  })
  return res.data
}
