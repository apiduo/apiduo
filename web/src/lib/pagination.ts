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

/**
 * Page arithmetic for lists that are rendered page by page.
 *
 * Kept as plain functions instead of a hook so the clamping rules can be
 * tested directly: an off-by-one page shows an empty table, which reads as
 * "no data" rather than "wrong page".
 */

export const DEFAULT_PAGE_SIZE = 20

export function pageCount(total: number, pageSize: number): number {
  if (!Number.isFinite(total) || total <= 0) return 1
  if (!Number.isFinite(pageSize) || pageSize <= 0) return 1
  return Math.max(1, Math.ceil(total / pageSize))
}

/**
 * Keep `page` inside `1..pageCount(total, pageSize)`.
 *
 * Callers pass the page they asked for; after a filter shrinks the list that
 * number may point past the end, and the table would render zero rows while
 * the pager still says "3 / 3".
 */
export function clampPage(page: number, total: number, pageSize: number): number {
  const max = pageCount(total, pageSize)
  if (!Number.isFinite(page)) return 1
  return Math.min(Math.max(1, Math.floor(page)), max)
}

/** Slice one page out of a fully loaded list. */
export function slicePage<T>(
  rows: readonly T[],
  page: number,
  pageSize: number
): T[] {
  if (!Number.isFinite(pageSize) || pageSize <= 0) return []
  const current = clampPage(page, rows.length, pageSize)
  const start = (current - 1) * pageSize
  return rows.slice(start, start + pageSize)
}
