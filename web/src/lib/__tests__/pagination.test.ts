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
import { describe, expect, it } from 'vitest'

import { clampPage, pageCount, slicePage } from '@/lib/pagination'

describe('pagination arithmetic', () => {
  it('rounds the page count up and never reports zero pages', () => {
    expect(pageCount(0, 20)).toBe(1)
    expect(pageCount(1, 20)).toBe(1)
    expect(pageCount(20, 20)).toBe(1)
    expect(pageCount(21, 20)).toBe(2)
    expect(pageCount(100, 50)).toBe(2)
  })

  it('clamps a page that points past the end after the list shrank', () => {
    // Filtering 100 rows down to 5 leaves the old page pointing at nothing;
    // an unclamped slice would render an empty table that reads as "no data".
    expect(clampPage(5, 5, 20)).toBe(1)
    expect(clampPage(3, 41, 20)).toBe(3)
    expect(clampPage(9, 41, 20)).toBe(3)
    expect(clampPage(0, 41, 20)).toBe(1)
    expect(clampPage(-2, 41, 20)).toBe(1)
    expect(clampPage(Number.NaN, 41, 20)).toBe(1)
  })

  it('slices the requested page and degrades safely on bad input', () => {
    const rows = Array.from({ length: 45 }, (_, index) => index)
    expect(slicePage(rows, 1, 20)).toEqual(rows.slice(0, 20))
    expect(slicePage(rows, 3, 20)).toEqual(rows.slice(40))
    expect(slicePage(rows, 4, 20)).toEqual(rows.slice(40))
    expect(slicePage(rows, 1, 0)).toEqual([])
  })
})
