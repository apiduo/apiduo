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
import type { ApiduoHoliday, ApiduoHolidayId } from './types'

/**
 * Particle ambience for one holiday.
 *
 * Drawn on a full-viewport canvas when the holiday is active; the canvas is
 * not mounted at all otherwise, so an ordinary day pays nothing for the
 * feature. Colours are hex because the canvas 2D API predates oklch() string
 * support; they mirror the chart palette in styles/apiduo-appearance.css and
 * must be kept in step with it by hand.
 */
export type ApiduoParticleMode =
  | 'confetti'
  | 'snow'
  | 'pumpkin'
  | 'firework'
  | 'moon'

export type ApiduoHolidayParticles = {
  mode: ApiduoParticleMode
  colors: readonly [string, string]
}

/**
 * One site-wide palette, with a suggested window.
 *
 * The window is a form-filling shortcut only: it is copied into the two date
 * fields when picked, and the operator can move it. Nothing resolves a holiday
 * to a window at runtime, so a fixed lunar date here cannot silently misfire
 * next year — it is a starting point, not a rule.
 */
export type ApiduoHolidayOption = {
  id: ApiduoHolidayId
  /** English source string; the console translates it. */
  labelKey: string
  hintKey: string
  from: string
  to: string
  particles: ApiduoHolidayParticles
}

export const APIDUO_HOLIDAYS: readonly ApiduoHolidayOption[] = [
  {
    id: 'spring-festival',
    labelKey: 'Spring Festival',
    hintKey: 'Chinese red and gold',
    from: '02-01',
    to: '02-24',
    particles: { mode: 'confetti', colors: ['#e64545', '#f5c542'] },
  },
  {
    id: 'mid-autumn',
    labelKey: 'Mid-Autumn',
    hintKey: 'Moonlight amber',
    from: '09-10',
    to: '09-25',
    particles: { mode: 'moon', colors: ['#f5ecd7', '#f5c542'] },
  },
  {
    id: 'national-day',
    labelKey: 'National Day',
    hintKey: 'Deep celebration red',
    from: '09-29',
    to: '10-08',
    particles: { mode: 'firework', colors: ['#e64545', '#f5c542'] },
  },
  {
    id: 'halloween',
    labelKey: 'Halloween',
    hintKey: 'Pumpkin orange and violet',
    from: '10-24',
    to: '11-01',
    particles: { mode: 'pumpkin', colors: ['#ff8c1a', '#7b4bd6'] },
  },
  {
    id: 'christmas',
    labelKey: 'Christmas',
    hintKey: 'Pine green and berry red',
    from: '12-18',
    to: '12-27',
    particles: { mode: 'snow', colors: ['#eef4ff', '#ffffff'] },
  },
]

export function holidayById(id: string): ApiduoHolidayOption | undefined {
  return APIDUO_HOLIDAYS.find((holiday) => holiday.id === id)
}

export function particlesForHoliday(
  id: ApiduoHolidayId
): ApiduoHolidayParticles | undefined {
  return holidayById(id)?.particles
}

/** Zero-pads to `MM-DD`, the only shape the server stores. */
function monthDay(date: Date): string {
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${month}-${day}`
}

/**
 * Whether the holiday should be painted right now.
 *
 * The comparison runs in the visitor's timezone because the browser is the only
 * place that knows it: an operator in UTC+8 turning on Spring Festival means
 * that date where the visitors are, not where the server is.
 *
 * A window that wraps the new year (12-20 to 01-10) is compared in two halves
 * rather than treated as a mistake: for holidays at year end that is the normal
 * shape, and reversing it would silently switch the skin off for the week it
 * matters most.
 */
export function isHolidayActive(
  holiday: ApiduoHoliday,
  from: string,
  to: string,
  now: Date = new Date()
): boolean {
  if (!holiday) return false
  if (!from || !to) return true
  const today = monthDay(now)
  if (from <= to) return today >= from && today <= to
  return today >= from || today <= to
}
