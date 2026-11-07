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
 * The shapes `apiduo-appearance.css` knows how to draw.
 *
 * Closed set. The server validates against the same list: a style the
 * stylesheet does not implement would leave apiduo surfaces undecorated, which
 * reads as a bug rather than as a choice.
 */
export const APIDUO_EFFECT_STYLES = [
  'flow',
  'rainbow',
  'spotlight',
  'sheen',
  'pulse',
  'none',
] as const

export type ApiduoEffectStyle = (typeof APIDUO_EFFECT_STYLES)[number]

/**
 * Site-wide palettes.
 *
 * `''` means no holiday. The rest must stay in step with `ApiduoHolidays` on
 * the server: the console offers what the server will accept, and an unknown
 * value there is normalised away rather than stored.
 */
export const APIDUO_HOLIDAY_IDS = [
  'spring-festival',
  'mid-autumn',
  'national-day',
  'halloween',
  'christmas',
] as const

export type ApiduoHolidayId = (typeof APIDUO_HOLIDAY_IDS)[number]

export type ApiduoHoliday = '' | ApiduoHolidayId

/** Payload of `GET /api/appearance`. */
export type ApiduoAppearance = {
  enabled: boolean
  style: ApiduoEffectStyle
  /** Any CSS colour. Empty means "follow the theme". */
  color: string
  speed_ms: number
  ring_px: number
  tail_deg: number
  glow: boolean
  holiday: ApiduoHoliday
  /** MM-DD. Both empty means "on whenever a holiday is named". */
  holiday_from: string
  holiday_to: string
}

export type ApiduoAppearanceResponse = {
  success: boolean
  message: string
  data: ApiduoAppearance
}

/**
 * The shape the provider writes into `:root`.
 *
 * Kept separate from {@link ApiduoAppearance} because the console edits
 * snapshots taken at load time, and the runtime only ever needs the derived
 * CSS variables.
 */
export type ApiduoAppearanceVars = {
  color?: string
  speed: string
  ring: string
  tail: string
  glow: boolean
  style: ApiduoEffectStyle
}
