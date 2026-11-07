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
import { createContext, useContext } from 'react'

import type { ApiduoAppearance } from './types'

/**
 * What a surface looks like before the server answers.
 *
 * It matches the server default on purpose: rendering the shipped look
 * immediately means the console never flashes "no effect" on a slow
 * connection. `data-ap-ready` is what actually gates painting, so a server that
 * turns the effect off removes it before anything is drawn.
 */
export const DEFAULT_APIDUO_APPEARANCE: ApiduoAppearance = {
  enabled: true,
  style: 'flow',
  color: '',
  speed_ms: 3200,
  ring_px: 1.5,
  tail_deg: 120,
  glow: true,
  holiday: '',
  holiday_from: '',
  holiday_to: '',
}

export const ApiduoAppearanceContext = createContext<ApiduoAppearance>(
  DEFAULT_APIDUO_APPEARANCE
)

/** The live configuration, for components that need to branch on the style. */
export function useApiduoAppearance(): ApiduoAppearance {
  return useContext(ApiduoAppearanceContext)
}
