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

import {
  addDays,
  buildDailySeries,
  computeRunway,
  forecastUsage,
  roundTopUp,
  toDayKey,
  type DailyPoint,
} from '@/features/dashboard/lib/forecast'

function series(values: number[], start = '2026-11-01'): DailyPoint[] {
  return values.map((quota, index) => ({
    date: addDays(start, index),
    quota,
    count: 1,
    tokens: 0,
  }))
}

describe('date helpers', () => {
  test('round-trips a day key across month boundaries', () => {
    expect(addDays('2026-10-31', 1)).toBe('2026-11-01')
    expect(addDays('2026-11-01', -1)).toBe('2026-10-31')
    expect(toDayKey(new Date('2026-11-05T12:00:00'))).toBe('2026-11-05')
  })
})

describe('buildDailySeries', () => {
  test('pads missing days with zero and keeps chronological order', () => {
    const built = buildDailySeries(
      [
        { date: '2026-11-02', quota: 50, count: 2, token_used: 5 },
        { date: '2026-11-05', quota: 80, count: 3, token_used: 8 },
      ],
      { days: 5, endDate: '2026-11-05' }
    )
    expect(built.map((point) => point.date)).toEqual([
      '2026-11-01',
      '2026-11-02',
      '2026-11-03',
      '2026-11-04',
      '2026-11-05',
    ])
    expect(built.map((point) => point.quota)).toEqual([0, 50, 0, 0, 80])
  })

  test('handles a missing response', () => {
    const built = buildDailySeries(undefined, { days: 3, endDate: '2026-11-05' })
    expect(built).toHaveLength(3)
    expect(built.every((point) => point.quota === 0)).toBe(true)
  })
})

describe('forecastUsage', () => {
  test('a flat series projects the same number forward', () => {
    const result = forecastUsage(series([100, 100, 100, 100, 100, 100, 100]), {
      horizonDays: 7,
    })
    expect(result.trend).toBe('flat')
    expect(result.trendPct).toBeCloseTo(0, 5)
    expect(result.predictedTotal).toBeCloseTo(700, 5)
    expect(result.confidence).toBe('medium')
  })

  test('a rising series projects above the last observation', () => {
    const result = forecastUsage(series([10, 20, 30, 40, 50, 60, 70]), {
      horizonDays: 7,
    })
    expect(result.trend).toBe('up')
    expect(result.slope).toBeCloseTo(10, 5)
    expect(result.predictedTotal).toBeCloseTo(770, 5)
    // Perfectly linear, so the band collapses to the point estimate.
    expect(result.upperTotal - result.lowerTotal).toBeLessThan(1)
  })

  test('never projects a negative day', () => {
    const result = forecastUsage(series([100, 90, 80, 70, 60, 50, 40]), {
      horizonDays: 7,
    })
    expect(result.trend).toBe('down')
    expect(result.predictedDaily.every((value) => value >= 0)).toBe(true)
    expect(result.predictedTotal).toBeCloseTo(60, 5)
  })

  test('reports the change against the last 7 days, never below -100%', () => {
    const result = forecastUsage(series([100, 90, 80, 70, 60, 50, 40]), {
      horizonDays: 7,
    })
    // A series that roughly halves must not read as "-143%".
    expect(result.trendPct).toBeGreaterThan(-100)
    expect(result.trendPct).toBeLessThan(-50)
  })

  test('a steady series with mild noise still counts as high confidence', () => {
    const result = forecastUsage(
      series([100, 96, 102, 99, 101, 97, 103, 100, 98, 102, 99, 101, 100, 97]),
      { horizonDays: 7 }
    )
    // Flat with +-3% noise: highly predictable even though R² is near zero.
    expect(result.confidence).toBe('high')
  })

  test('marks an empty window as insufficient instead of guessing', () => {
    const result = forecastUsage(series([0, 0, 0, 0, 0, 0, 0]))
    expect(result.confidence).toBe('insufficient')
    expect(result.predictedTotal).toBe(0)
    expect(result.predictedDaily).toEqual([])
  })

  test('falls back to the recent average when there are too few active days', () => {
    const result = forecastUsage(series([0, 0, 0, 0, 0, 60, 60]), {
      horizonDays: 7,
    })
    expect(result.confidence).toBe('low')
    // mean3 counts the empty day, so an intermittent user is not projected as if
    // they spent every day.
    expect(result.predictedTotal).toBeCloseTo(280, 5)
  })

  test('raises confidence only with enough samples and a real fit', () => {
    const steady = forecastUsage(
      series(Array.from({ length: 14 }, (_, index) => 100 + index * 5)),
      { horizonDays: 7 }
    )
    expect(steady.confidence).toBe('high')
  })

  test('widens the band when the series is noisy', () => {
    const noisy = forecastUsage(
      series([50, 400, 10, 300, 20, 250, 60, 380, 30, 290, 70, 350]),
      { horizonDays: 7 }
    )
    expect(noisy.upperTotal - noisy.lowerTotal).toBeGreaterThan(
      noisy.predictedTotal * 0.2
    )
  })
})

describe('computeRunway', () => {
  test('divides the balance by the recent daily average', () => {
    const runway = computeRunway(1000, series([100, 100, 100, 100, 100, 100, 100]), {
      today: '2026-11-05',
    })
    expect(runway.dailyAverage).toBeCloseTo(100, 5)
    expect(runway.daysLeft).toBeCloseTo(10, 5)
    expect(runway.emptyDate).toBe('2026-11-15')
    // 30 days of runway at 100/day minus the existing 1000 balance.
    expect(runway.suggestedTopUp).toBe(2000)
  })

  test('treats a zero balance as empty today', () => {
    const runway = computeRunway(0, series([100, 100, 100, 100, 100]), {
      today: '2026-11-05',
    })
    expect(runway.daysLeft).toBe(0)
    expect(runway.emptyDate).toBe('2026-11-05')
    expect(runway.suggestedTopUp).toBe(3000)
  })

  test('returns no runway when nothing is being spent', () => {
    const runway = computeRunway(500, series([0, 0, 0, 0, 0]), {
      today: '2026-11-05',
    })
    expect(runway.dailyAverage).toBe(0)
    expect(runway.daysLeft).toBeNull()
    expect(runway.emptyDate).toBeNull()
    expect(runway.suggestedTopUp).toBe(0)
    expect(runway.confidence).toBe('insufficient')
  })

  test('flags a spend that is speeding up', () => {
    const runway = computeRunway(
      1000,
      series([20, 20, 20, 20, 100, 120, 140]),
      { today: '2026-11-05' }
    )
    expect(runway.accelerating).toBe(true)
  })

  test('does not flag a steady spend as accelerating', () => {
    const runway = computeRunway(
      1000,
      series([100, 100, 100, 100, 100, 100, 100]),
      { today: '2026-11-05' }
    )
    expect(runway.accelerating).toBe(false)
  })
})

describe('roundTopUp', () => {
  test('rounds up to a friendly number', () => {
    expect(roundTopUp(0)).toBe(0)
    expect(roundTopUp(-50)).toBe(0)
    expect(roundTopUp(12)).toBe(15)
    expect(roundTopUp(123)).toBe(130)
    expect(roundTopUp(1234)).toBe(1300)
  })
})
