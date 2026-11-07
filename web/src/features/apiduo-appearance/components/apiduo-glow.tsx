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
import type { ReactNode } from 'react'

import { cn } from '@/lib/utils'

import { useApiduoAppearance } from '../context'

/**
 * Attribute that turns a container into an effect host.
 *
 * Exported as a constant rather than typed inline so a call site cannot invent
 * a variant: the pointer tracker and the stylesheet both match on this exact
 * name.
 */
export const APIDUO_SURFACE_ATTR = 'data-ap-surface'

/**
 * The effect layer for one surface.
 *
 * Renders exactly one element — the one the configured style needs — and lets
 * the stylesheet do the rest. Placement is always absolute, so the host
 * container must be `position: relative` (the attribute already implies it) and
 * should carry the `border-radius` the highlight is meant to follow.
 *
 * Returns `null` when the effect is off, so a disabled configuration costs no
 * DOM at all.
 */
export function ApiduoGlow(props: { className?: string }) {
  const { enabled, style, glow } = useApiduoAppearance()

  if (!enabled || style === 'none') return null

  const base = cn(props.className)

  switch (style) {
    case 'rainbow':
      return (
        <span
          aria-hidden='true'
          data-ap-glow={glow ? 'on' : 'off'}
          className={cn('ap-glow ap-glow-rainbow', base)}
        />
      )
    case 'spotlight':
      return <span aria-hidden='true' className={cn('ap-spot', base)} />
    case 'sheen':
      return <span aria-hidden='true' className={cn('ap-sheen', base)} />
    case 'pulse':
      return <span aria-hidden='true' className={cn('ap-pulse', base)} />
    default:
      return (
        <span
          aria-hidden='true'
          data-ap-glow={glow ? 'on' : 'off'}
          className={cn('ap-glow', base)}
        />
      )
  }
}

type ApiduoSurfaceProps = React.HTMLAttributes<HTMLDivElement> & {
  children: ReactNode
}

/**
 * A container that hosts the effect, for surfaces that do not already have a
 * wrapper of their own.
 *
 * Existing cards should not be wrapped in this — pass {@link APIDUO_SURFACE_ATTR}
 * and render {@link ApiduoGlow} inside instead, so the card keeps its own grid
 * and flex participation.
 */
export function ApiduoSurface(props: ApiduoSurfaceProps) {
  const { children, className, ...rest } = props
  return (
    <div data-ap-surface='' className={cn('relative', className)} {...rest}>
      <ApiduoGlow />
      {children}
    </div>
  )
}
