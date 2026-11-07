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
import { MULTI_KEY_STATUS } from '../constants'

/**
 * How many keys of a multi-key channel can serve right now.
 *
 * Mirrors the backend's eligible set, which is the whole key field trimmed and
 * then split by newline, filtered by "not blank, not disabled". The index used
 * for the status lookup therefore has to be the position in *that* array, not in
 * the raw textarea text — a leading blank line shifts one relative to the other
 * and every status would be attributed to the wrong key.
 */
export function countAvailableKeys(
  keyText: string | undefined,
  statusList?: Record<string, number>
): number {
  const trimmed = (keyText ?? '').trim()
  if (!trimmed) return 0
  return trimmed.split('\n').filter((key, index) => {
    if (!key.trim()) return false
    const status = statusList?.[String(index)]
    return status === undefined || status === MULTI_KEY_STATUS.ENABLED
  }).length
}

/**
 * How many of the available keys are in rotation under a cap.
 *
 * The backend serves the first `limit` available keys, so this is a plain
 * clamp — and it is derived, never stored, because a spare key has no state of
 * its own.
 */
export function countServingKeys(available: number, limit: number): number {
  return limit > 0 ? Math.min(limit, available) : available
}
