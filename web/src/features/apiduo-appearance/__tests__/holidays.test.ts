import { expect, test } from 'vitest'

import { isHolidayActive } from '../holidays'

/** Dates are built in local time because that is what the function compares. */
function at(month: number, day: number): Date {
  return new Date(2026, month - 1, day, 12, 0, 0)
}

test('no holiday means nothing is painted', () => {
  expect(isHolidayActive('', '', '', at(2, 10))).toBe(false)
})

test('a named holiday without a window is always on', () => {
  expect(isHolidayActive('spring-festival', '', '', at(7, 1))).toBe(true)
})

test('a plain window includes both ends', () => {
  const from = '02-01'
  const to = '02-24'
  expect(isHolidayActive('spring-festival', from, to, at(1, 31))).toBe(false)
  expect(isHolidayActive('spring-festival', from, to, at(2, 1))).toBe(true)
  expect(isHolidayActive('spring-festival', from, to, at(2, 24))).toBe(true)
  expect(isHolidayActive('spring-festival', from, to, at(2, 25))).toBe(false)
})

test('a window that wraps the new year stays open on both sides', () => {
  const from = '12-20'
  const to = '01-10'
  expect(isHolidayActive('christmas', from, to, at(12, 20))).toBe(true)
  expect(isHolidayActive('christmas', from, to, at(12, 31))).toBe(true)
  expect(isHolidayActive('christmas', from, to, at(1, 1))).toBe(true)
  expect(isHolidayActive('christmas', from, to, at(1, 10))).toBe(true)
  expect(isHolidayActive('christmas', from, to, at(1, 11))).toBe(false)
  expect(isHolidayActive('christmas', from, to, at(6, 1))).toBe(false)
})

test('single digit month and day are padded before comparing', () => {
  // '09-05' must sort after '09-04'; unpadded '9-5' would compare wrong.
  expect(isHolidayActive('mid-autumn', '09-01', '09-10', at(9, 5))).toBe(true)
  expect(isHolidayActive('mid-autumn', '09-01', '09-10', at(9, 10))).toBe(true)
  expect(isHolidayActive('mid-autumn', '09-01', '09-10', at(9, 11))).toBe(false)
})
