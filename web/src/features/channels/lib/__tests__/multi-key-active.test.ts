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
import { describe, expect, test } from 'vitest'

import { MULTI_KEY_STATUS } from '../../constants'
import { countAvailableKeys, countServingKeys } from '../multi-key-active'

describe('countAvailableKeys', () => {
  test('counts the keys of the field', () => {
    expect(countAvailableKeys('k0\nk1\nk2')).toBe(3)
  })

  test('empty or missing field has no available keys', () => {
    expect(countAvailableKeys('')).toBe(0)
    expect(countAvailableKeys(undefined)).toBe(0)
    expect(countAvailableKeys('   \n  ')).toBe(0)
  })

  test('trims before splitting so blank edges do not shift status indexes', () => {
    // The backend trims the whole field first, so index 0 is k0. Without the
    // trim this line would read the status of k0 for what it thinks is the
    // blank first line, and report k1 as enabled.
    expect(
      countAvailableKeys('\n\nk0\nk1', {
        '0': MULTI_KEY_STATUS.AUTO_DISABLED,
      })
    ).toBe(1)
  })

  test('interior blank lines are not keys', () => {
    expect(countAvailableKeys('k0\n\nk1')).toBe(2)
    expect(countAvailableKeys('k0\n   \nk1')).toBe(2)
  })

  test('disabled keys are excluded while their neighbours keep their status', () => {
    // Interior blanks still occupy a slot in the array, so the status map has to
    // be read by raw position: k2 is index 3, not index 2.
    const available = countAvailableKeys('k0\n\nk1\nk2', {
      '0': MULTI_KEY_STATUS.MANUAL_DISABLED,
      '3': MULTI_KEY_STATUS.AUTO_DISABLED,
    })
    expect(available).toBe(1)
  })
})

describe('countServingKeys', () => {
  test('no cap serves everything available', () => {
    expect(countServingKeys(100, 0)).toBe(100)
  })

  test('a cap below the available count clamps', () => {
    expect(countServingKeys(100, 10)).toBe(10)
  })

  test('a cap above the available count degrades to everything', () => {
    expect(countServingKeys(3, 10)).toBe(3)
  })
})
