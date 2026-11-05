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
import type { SeriesPoint } from '@/features/dashboard/types'

export interface DailyPoint {
  date: string
  quota: number
  count: number
  tokens: number
}

export type ForecastConfidence = 'high' | 'medium' | 'low' | 'insufficient'
export type TrendDirection = 'up' | 'down' | 'flat'

export interface ForecastResult {
  samples: number
  mean7: number
  mean3: number
  slope: number
  trend: TrendDirection
  trendPct: number
  predictedDaily: number[]
  predictedTotal: number
  lowerTotal: number
  upperTotal: number
  confidence: ForecastConfidence
  r2: number
}

export interface RunwayResult {
  dailyAverage: number
  daysLeft: number | null
  emptyDate: string | null
  suggestedTopUp: number
  confidence: ForecastConfidence
  accelerating: boolean
}

function numberValue(value: unknown): number {
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : 0
}

export function toDayKey(date: Date): string {
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}

export function addDays(dayKey: string, delta: number): string {
  // Parsed as local midnight so a DST shift cannot change the date.
  const date = new Date(`${dayKey}T00:00:00`)
  date.setDate(date.getDate() + delta)
  return toDayKey(date)
}

// buildDailySeries pads the sparse server response into one point per day.
// The server omits empty days, but a gap must count as zero when fitting a
// trend: "stopped using it on Tuesday" is information, not missing data.
export function buildDailySeries(
  points: SeriesPoint[] | undefined,
  options: { days: number; endDate?: string }
): DailyPoint[] {
  const days = Math.max(1, Math.floor(options.days))
  const endKey = options.endDate ?? toDayKey(new Date())
  const byDate = new Map<string, DailyPoint>()
  for (const point of points ?? []) {
    byDate.set(point.date, {
      date: point.date,
      quota: numberValue(point.quota),
      count: numberValue(point.count),
      tokens: numberValue(point.token_used),
    })
  }

  const series: DailyPoint[] = []
  for (let offset = days - 1; offset >= 0; offset--) {
    const key = addDays(endKey, -offset)
    series.push(byDate.get(key) ?? { date: key, quota: 0, count: 0, tokens: 0 })
  }
  return series
}

function mean(values: number[]): number {
  if (values.length === 0) return 0
  return values.reduce((sum, value) => sum + value, 0) / values.length
}

// linearFit returns the least-squares line plus R², which is what the UI uses
// to decide whether to trust the projection.
function linearFit(values: number[]): {
  slope: number
  intercept: number
  r2: number
} {
  const n = values.length
  if (n < 2) return { slope: 0, intercept: values[0] ?? 0, r2: 0 }

  const meanX = (n - 1) / 2
  const meanY = mean(values)
  let covariance = 0
  let varianceX = 0
  for (let i = 0; i < n; i++) {
    const dx = i - meanX
    covariance += dx * (values[i] - meanY)
    varianceX += dx * dx
  }
  const slope = varianceX === 0 ? 0 : covariance / varianceX
  const intercept = meanY - slope * meanX

  let totalVariance = 0
  let residual = 0
  for (let i = 0; i < n; i++) {
    const predicted = intercept + slope * i
    totalVariance += (values[i] - meanY) ** 2
    residual += (values[i] - predicted) ** 2
  }
  // A perfectly flat series has zero total variance. The textbook convention is
  // R² = 1 there: the fit explains all of it. Treating it as 0 would mark the
  // most predictable series as the least trustworthy.
  const r2 = totalVariance === 0 ? 1 : 1 - residual / totalVariance
  return { slope, intercept, r2 }
}

function confidenceOf(
  samples: number,
  totalSamples: number,
  residualCv: number
): ForecastConfidence {
  if (samples === 0) return 'insufficient'
  // Fewer than 3 days with usage cannot support a projection no matter how
  // clean the line looks.
  if (samples < 3 || totalSamples < 3) return 'low'
  // R² measures how much of the variance the trend explains, which punishes a
  // steady series with small noise exactly as much as a chaotic one. The
  // residual coefficient of variation measures what we actually care about:
  // how far a day can stray from the fitted line, relative to its size.
  if (samples >= 10 && residualCv < 0.25) return 'high'
  if (samples >= 5 && residualCv < 0.5) return 'medium'
  return 'low'
}

// confidenceFromSamples is the runway variant: no trend is fitted there, so the
// only signal is how many days actually carry usage.
function confidenceFromSamples(
  samples: number,
  totalSamples: number
): ForecastConfidence {
  if (samples === 0) return 'insufficient'
  if (samples < 3 || totalSamples < 3) return 'low'
  return samples >= 5 ? 'medium' : 'low'
}

export function forecastUsage(
  series: DailyPoint[],
  options: { horizonDays?: number; minSamples?: number } = {}
): ForecastResult {
  const horizon = Math.max(1, Math.floor(options.horizonDays ?? 7))
  const minSamples = Math.max(1, Math.floor(options.minSamples ?? 3))
  const values = series.map((point) => point.quota)
  const samples = values.filter((value) => value > 0).length
  const last7 = values.slice(-7)
  const last3 = values.slice(-3)
  const mean7 = mean(last7)
  const mean3 = mean(last3)

  if (samples === 0 || values.length === 0) {
    return {
      samples: 0,
      mean7: 0,
      mean3: 0,
      slope: 0,
      trend: 'flat',
      trendPct: 0,
      predictedDaily: [],
      predictedTotal: 0,
      lowerTotal: 0,
      upperTotal: 0,
      confidence: 'insufficient',
      r2: 0,
    }
  }

  // Too few days to fit a line: fall back to the recent average, and say so.
  if (samples < minSamples) {
    const flat = mean3 > 0 ? mean3 : mean7
    return {
      samples,
      mean7,
      mean3,
      slope: 0,
      trend: 'flat',
      trendPct: 0,
      predictedDaily: Array.from({ length: horizon }, () => flat),
      predictedTotal: flat * horizon,
      lowerTotal: flat * horizon * 0.5,
      upperTotal: flat * horizon * 1.5,
      confidence: 'low',
      r2: 0,
    }
  }

  const { slope, intercept, r2 } = linearFit(values)
  const n = values.length
  const predictedDaily: number[] = []
  for (let step = 1; step <= horizon; step++) {
    predictedDaily.push(Math.max(0, intercept + slope * (n - 1 + step)))
  }
  const predictedTotal = predictedDaily.reduce((sum, value) => sum + value, 0)

  // Residual spread, widened by sqrt(horizon) because errors accumulate.
  let residual = 0
  for (let i = 0; i < n; i++) {
    residual += (values[i] - (intercept + slope * i)) ** 2
  }
  const sigma = Math.sqrt(residual / Math.max(1, n - 2))
  const margin = 1.96 * sigma * Math.sqrt(horizon)

  // trendPct compares like with like: the projection normalised to 7 days
  // against the last 7 days of actuals. Dividing the cumulative slope by the
  // current mean instead would read "-143%" for a series that simply halved.
  const last7Total = values.slice(-7).reduce((sum, value) => sum + value, 0)
  const projected7 = predictedTotal * (7 / horizon)
  let trendPct = 0
  if (last7Total > 0) {
    trendPct = ((projected7 - last7Total) / last7Total) * 100
  } else if (projected7 > 0) {
    trendPct = 100
  }
  let trend: TrendDirection = 'flat'
  if (trendPct > 10) trend = 'up'
  else if (trendPct < -10) trend = 'down'

  const meanFitted = mean(values)
  const residualCv = meanFitted > 0 ? sigma / meanFitted : 1

  return {
    samples,
    mean7,
    mean3,
    slope,
    trend,
    trendPct,
    predictedDaily,
    predictedTotal,
    lowerTotal: Math.max(0, predictedTotal - margin),
    upperTotal: predictedTotal + margin,
    confidence: confidenceOf(samples, n, residualCv),
    r2,
  }
}

export type RunwayLevel =
  | 'critical'
  | 'warning'
  | 'watch'
  | 'healthy'
  | 'idle'

// runwayLevel buckets a projection into the action bands the UI uses. The
// boundaries are about what an operator would do, not round numbers: under a
// day is already lost, under three days is a same-week call, under a week is a
// reminder.
export function runwayLevel(runway: RunwayResult): RunwayLevel {
  if (runway.daysLeft === null) return 'idle'
  if (runway.daysLeft < 1) return 'critical'
  if (runway.daysLeft < 3) return 'warning'
  if (runway.daysLeft < 7) return 'watch'
  return 'healthy'
}

export function roundTopUp(value: number): number {
  if (!Number.isFinite(value) || value <= 0) return 0
  // Round to a number a human would actually type into a top-up form.
  let step = 5
  if (value >= 1000) step = 100
  else if (value >= 100) step = 10
  return Math.ceil(value / step) * step
}

// computeRunway answers "when does the balance run out" and "how much should be
// topped up". Both numbers are only as good as the recent spend, so the caller
// gets a confidence flag alongside them.
export function computeRunway(
  balance: number,
  series: DailyPoint[],
  options: { targetDays?: number; today?: string } = {}
): RunwayResult {
  const targetDays = Math.max(1, Math.floor(options.targetDays ?? 30))
  const today = options.today ?? toDayKey(new Date())
  const values = series.map((point) => point.quota)
  const samples = values.filter((value) => value > 0).length
  const mean7 = mean(values.slice(-7))
  const mean3 = mean(values.slice(-3))
  const dailyAverage = mean7 > 0 ? mean7 : mean3

  if (dailyAverage <= 0) {
    return {
      dailyAverage: 0,
      daysLeft: null,
      emptyDate: null,
      suggestedTopUp: 0,
      confidence: 'insufficient',
      accelerating: false,
    }
  }

  const safeBalance = Math.max(0, balance)
  const daysLeft = safeBalance / dailyAverage
  const emptyDate =
    daysLeft === 0 ? today : addDays(today, Math.ceil(daysLeft))

  return {
    dailyAverage,
    daysLeft,
    emptyDate,
    // Kept raw: rounding to a human-friendly number only makes sense once the
    // caller converts quota to currency. roundTopUp() is exported for that.
    suggestedTopUp: Math.max(0, dailyAverage * targetDays - safeBalance),
    confidence: confidenceFromSamples(samples, values.length),
    accelerating: mean7 > 0 && mean3 > mean7 * 1.5,
  }
}
