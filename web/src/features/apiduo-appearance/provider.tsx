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
import { useQuery } from '@tanstack/react-query'
import { useEffect, useMemo, useState, type ReactNode } from 'react'

import { getApiduoAppearance } from './api'
import { ApiduoParticles } from './components/apiduo-particles'
import {
  ApiduoAppearanceContext,
  ApiduoHolidayContext,
  DEFAULT_APIDUO_APPEARANCE,
} from './context'
import { isHolidayActive } from './holidays'
import type { ApiduoAppearance, ApiduoHoliday } from './types'

/**
 * Publishes the configuration as CSS variables on `:root`.
 *
 * Everything the stylesheet needs arrives through variables, so a console edit
 * retunes every mounted surface without re-rendering a single component.
 * The colour is removed rather than blanked when it is empty: an empty value
 * would make `var(--ap-color, var(--primary))` resolve to nothing and kill the
 * declaration, which is exactly the silent failure a bare `var()` invites.
 */
function publish(appearance: ApiduoAppearance) {
  const root = document.documentElement
  root.dataset.apEffect = appearance.enabled ? appearance.style : 'none'
  root.style.setProperty('--ap-speed', `${appearance.speed_ms}ms`)
  root.style.setProperty('--ap-ring', `${appearance.ring_px}px`)
  root.style.setProperty('--ap-tail', `${appearance.tail_deg}deg`)
  if (appearance.color) {
    root.style.setProperty('--ap-color', appearance.color)
  } else {
    root.style.removeProperty('--ap-color')
  }
  root.dataset.apReady = 'true'

  // The holiday lives on <body> so its palette outranks a bare
  // [data-theme-preset] attribute in the stylesheet. A colour the operator
  // picked explicitly still wins for the effect itself: --ap-color is only
  // cleared above when empty, and the skin supplies --primary underneath.
  const active = isHolidayActive(
    appearance.holiday,
    appearance.holiday_from,
    appearance.holiday_to
  )
    ? appearance.holiday
    : ''
  if (active) {
    document.body.dataset.apHoliday = active
  } else {
    document.body.removeAttribute('data-ap-holiday')
  }
  return active
}

/**
 * Feeds the pointer position to whichever surface is under the cursor.
 *
 * One delegated listener for the whole document rather than one per card: a
 * dashboard can hold dozens of apiduo surfaces, and the tracking is only ever
 * meaningful for the one being hovered. Coordinates are written inside a rAF so
 * a fast pointer cannot queue more style writes than the frame budget allows.
 */
function useSpotlightTracking(enabled: boolean) {
  useEffect(() => {
    if (!enabled) return

    let frame = 0
    let pending: { el: HTMLElement; x: number; y: number } | null = null

    const flush = () => {
      frame = 0
      if (!pending) return
      pending.el.style.setProperty('--ap-mx', `${pending.x}px`)
      pending.el.style.setProperty('--ap-my', `${pending.y}px`)
      pending = null
    }

    const onPointerMove = (event: PointerEvent) => {
      const surface = (event.target as Element | null)?.closest?.(
        '[data-ap-surface]'
      ) as HTMLElement | null
      if (!surface) return
      const rect = surface.getBoundingClientRect()
      pending = {
        el: surface,
        x: event.clientX - rect.left,
        y: event.clientY - rect.top,
      }
      if (frame === 0) frame = requestAnimationFrame(flush)
    }

    // Back to the centre on exit: a spotlight frozen in the last corner reads
    // as a rendering artefact rather than as a light.
    const onPointerOut = (event: PointerEvent) => {
      const surface = (event.target as Element | null)?.closest?.(
        '[data-ap-surface]'
      ) as HTMLElement | null
      if (!surface) return
      surface.style.removeProperty('--ap-mx')
      surface.style.removeProperty('--ap-my')
    }

    document.addEventListener('pointermove', onPointerMove, { passive: true })
    document.addEventListener('pointerout', onPointerOut, { passive: true })
    return () => {
      if (frame !== 0) cancelAnimationFrame(frame)
      document.removeEventListener('pointermove', onPointerMove)
      document.removeEventListener('pointerout', onPointerOut)
    }
  }, [enabled])
}

type ApiduoAppearanceProviderProps = {
  children: ReactNode
}

/**
 * Installs the effect for the whole console.
 *
 * Mounted once, inside the authenticated shell. Public marketing pages host no
 * apiduo surfaces, so paying for a request there would buy nothing.
 */
export function ApiduoAppearanceProvider(
  props: ApiduoAppearanceProviderProps
) {
  const { data } = useQuery({
    queryKey: ['apiduo-appearance'],
    queryFn: getApiduoAppearance,
    // Appearance changes at human pace. Five minutes keeps a tab from polling a
    // value that only moves when an operator saves the settings page.
    staleTime: 5 * 60 * 1000,
    retry: 1,
  })

  const appearance = data?.data ?? DEFAULT_APIDUO_APPEARANCE
  const style = appearance.enabled ? appearance.style : 'none'
  const [activeHoliday, setActiveHoliday] = useState<ApiduoHoliday>('')

  useEffect(() => {
    setActiveHoliday(publish(appearance))
  }, [appearance])

  useSpotlightTracking(style === 'spotlight')

  const value = useMemo(
    () => ({ ...appearance, enabled: appearance.enabled && style !== 'none' }),
    [appearance, style]
  )

  return (
    <ApiduoAppearanceContext value={value}>
      <ApiduoHolidayContext value={activeHoliday}>
        <ApiduoParticles />
        {props.children}
      </ApiduoHolidayContext>
    </ApiduoAppearanceContext>
  )
}
