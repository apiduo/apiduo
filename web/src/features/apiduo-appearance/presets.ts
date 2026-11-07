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
import type { ApiduoAppearance, ApiduoEffectStyle } from './types'

/**
 * One complete look.
 *
 * A preset is a console convenience, not a runtime dependency: picking one
 * fills the form, saving writes the expanded numbers. The server therefore
 * never has to resolve a preset, and adding a look here needs no backend
 * change — which is the whole point of keeping the list in the frontend.
 */
export type AppearancePreset = {
  id: string
  /** English source string; the console translates it. */
  labelKey: string
  /** Short explanation shown under the label. */
  hintKey: string
  style: ApiduoEffectStyle
  /** Empty follows the theme colour. */
  color: string
  speedMs: number
  ringPx: number
  tailDeg: number
  glow: boolean
}

/**
 * The catalogue.
 *
 * Ordered by how often an operator is likely to want it: the theme-following
 * default first, the seasonal looks next — those are the reason the feature
 * exists — and the decorative ones last.
 *
 * Colours are mid-luminance on purpose. A highlight is only legible against
 * both the light and the dark card, and anything near white or near black
 * disappears on one of them.
 */
export const APPEARANCE_PRESETS: readonly AppearancePreset[] = [
  {
    id: 'default',
    labelKey: 'Follow theme',
    hintKey: 'Uses the current theme colour',
    style: 'flow',
    color: '',
    speedMs: 3200,
    ringPx: 1.5,
    tailDeg: 120,
    glow: true,
  },
  {
    id: 'spring-festival',
    labelKey: 'Spring Festival',
    hintKey: 'Festive red and gold',
    style: 'flow',
    color: '#e11d48',
    speedMs: 2600,
    ringPx: 2,
    tailDeg: 150,
    glow: true,
  },
  {
    id: 'mid-autumn',
    labelKey: 'Mid-Autumn',
    hintKey: 'Moonlight amber',
    style: 'pulse',
    color: '#f59e0b',
    speedMs: 3000,
    ringPx: 1.5,
    tailDeg: 120,
    glow: true,
  },
  {
    id: 'national-day',
    labelKey: 'National Day',
    hintKey: 'Celebration red',
    style: 'rainbow',
    color: '#dc2626',
    speedMs: 3600,
    ringPx: 2,
    tailDeg: 160,
    glow: true,
  },
  {
    id: 'halloween',
    labelKey: 'Halloween',
    hintKey: 'Pumpkin orange',
    style: 'flow',
    color: '#f97316',
    speedMs: 3000,
    ringPx: 2,
    tailDeg: 130,
    glow: true,
  },
  {
    id: 'christmas',
    labelKey: 'Christmas',
    hintKey: 'Pine green',
    style: 'sheen',
    color: '#16a34a',
    speedMs: 3800,
    ringPx: 1.5,
    tailDeg: 120,
    glow: true,
  },
  {
    id: 'aurora',
    labelKey: 'Aurora',
    hintKey: 'Cool teal, slow drift',
    style: 'flow',
    color: '#06b6d4',
    speedMs: 5200,
    ringPx: 1.5,
    tailDeg: 200,
    glow: true,
  },
  {
    id: 'cyber',
    labelKey: 'Cyber',
    hintKey: 'Violet, fast and tight',
    style: 'flow',
    color: '#8b5cf6',
    speedMs: 1800,
    ringPx: 1,
    tailDeg: 80,
    glow: true,
  },
  {
    id: 'spotlight',
    labelKey: 'Spotlight',
    hintKey: 'Only lights up under the cursor',
    style: 'spotlight',
    color: '',
    speedMs: 3200,
    ringPx: 1.5,
    tailDeg: 120,
    glow: false,
  },
  {
    id: 'quiet',
    labelKey: 'Quiet',
    hintKey: 'Static hairline, no motion',
    style: 'none',
    color: '',
    speedMs: 3200,
    ringPx: 1,
    tailDeg: 120,
    glow: false,
  },
]

export function presetById(id: string): AppearancePreset | undefined {
  return APPEARANCE_PRESETS.find((preset) => preset.id === id)
}

/** Expands a preset into the flat shape the server stores. */
export function presetToAppearance(preset: AppearancePreset): ApiduoAppearance {
  return {
    enabled: true,
    style: preset.style,
    color: preset.color,
    speed_ms: preset.speedMs,
    ring_px: preset.ringPx,
    tail_deg: preset.tailDeg,
    glow: preset.glow,
    // Presets describe the effect only: the holiday palette is a separate,
    // site-wide decision and picking a look must not clear it.
    holiday: '',
    holiday_from: '',
    holiday_to: '',
  }
}

/**
 * The preset currently matching a configuration, if any.
 *
 * Used to highlight the active card. A configuration that differs in any field
 * simply matches nothing, which is the honest answer — the form keeps the
 * operator's numbers rather than pretending they are still "Aurora".
 */
export function matchPreset(
  appearance: ApiduoAppearance
): AppearancePreset | undefined {
  return APPEARANCE_PRESETS.find((preset) => {
    const expanded = presetToAppearance(preset)
    return (
      expanded.style === appearance.style &&
      expanded.color === appearance.color &&
      expanded.speed_ms === appearance.speed_ms &&
      expanded.ring_px === appearance.ring_px &&
      expanded.tail_deg === appearance.tail_deg &&
      expanded.glow === appearance.glow
    )
  })
}
