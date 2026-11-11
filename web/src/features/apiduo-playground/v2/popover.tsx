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
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'

import { cn } from '@/lib/utils'

import { cssVars } from './css-vars'

interface FluidPopoverProps {
  anchor: HTMLElement | null
  open: boolean
  onClose: () => void
  children: React.ReactNode
  /** Which edge of the anchor the panel should line up with. */
  align?: 'start' | 'end'
  className?: string
  label?: string
  /**
   * The surface the anchor sits inside. A panel opening upwards clears this
   * surface instead of the anchor: a chip in the composer is only 28 px tall,
   * and a menu that stops above *it* still covers the prompt being typed.
   */
  boundary?: HTMLElement | null
}

const GAP = 6
const MARGIN = 8

/** Bottom of the region a panel may not enter when it opens upwards. */
function aboveEdge(anchorRect: DOMRect, boundary?: HTMLElement | null): number {
  if (!boundary) return anchorRect.top
  return Math.min(anchorRect.top, boundary.getBoundingClientRect().top)
}

/**
 * An anchored panel that grows out of its trigger.
 *
 * Three things make this different from a plain absolutely-positioned div:
 *
 *  - It is portalled, so the stage's `overflow: auto` cannot clip it. A picker
 *    that opens upwards out of a composer near the bottom of a scroller is the
 *    one case where "just position it relative" always fails.
 *  - Placement writes `transform-origin` back onto the panel. Without it the
 *    panel scales up from its own centre, which reads as a modal appearing,
 *    not as a menu unfolding from the control that was pressed.
 *  - It re-measures on scroll and resize instead of caching. The composer
 *    moves when the page scrolls or the toolbar wraps, and a stale top would
 *    leave the panel floating over the wrong chip.
 */
export function FluidPopover({
  anchor,
  open,
  onClose,
  children,
  align = 'start',
  className,
  label,
  boundary,
}: FluidPopoverProps) {
  const panelRef = useRef<HTMLDivElement | null>(null)
  const [pos, setPos] = useState<{
    top: number
    left: number
    ox: string
    oy: string
  } | null>(null)

  const place = useCallback(() => {
    const panel = panelRef.current
    if (!anchor || !panel) return
    const r = anchor.getBoundingClientRect()
    const w = panel.offsetWidth
    const h = panel.offsetHeight

    const above = aboveEdge(r, boundary) - h - GAP
    const below = r.bottom + GAP
    // Prefer the side with more room; a composer sits at the bottom of the
    // page, so "above" is the common answer and flipping is the exception.
    const fitsAbove = above >= MARGIN
    const top = fitsAbove ? above : below

    let left = align === 'end' ? r.right - w : r.left
    left = Math.min(Math.max(MARGIN, left), window.innerWidth - w - MARGIN)

    // Origin is the anchor's corner nearest the panel, so the panel appears to
    // unfold from the control rather than from its own middle.
    const oy = fitsAbove ? 'bottom' : 'top'
    let ox = 'center'
    if (Math.abs(left - r.left) < 1) ox = 'left'
    else if (Math.abs(left + w - r.right) < 1) ox = 'right'

    setPos({ top, left, ox, oy })
  }, [align, anchor, boundary])

  useLayoutEffect(() => {
    if (!open) {
      setPos(null)
      return
    }
    place()
  }, [open, place, children])

  useEffect(() => {
    if (!open) return
    const onScroll = () => place()
    const onResize = () => place()
    window.addEventListener('scroll', onScroll, true)
    window.addEventListener('resize', onResize)
    return () => {
      window.removeEventListener('scroll', onScroll, true)
      window.removeEventListener('resize', onResize)
    }
  }, [open, place])

  useEffect(() => {
    if (!open) return
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.stopPropagation()
        onClose()
        anchor?.focus()
      }
    }
    const onDown = (event: PointerEvent) => {
      const target = event.target as Node | null
      if (!target) return
      if (panelRef.current?.contains(target)) return
      if (anchor?.contains(target)) return
      onClose()
    }
    document.addEventListener('keydown', onKey, true)
    document.addEventListener('pointerdown', onDown, true)
    return () => {
      document.removeEventListener('keydown', onKey, true)
      document.removeEventListener('pointerdown', onDown, true)
    }
  }, [anchor, onClose, open])

  if (!open || typeof document === 'undefined') return null

  return createPortal(
    <div
      aria-label={label}
      className={cn('apg-pop', className)}
      ref={panelRef}
      role='dialog'
      style={
        pos
          ? cssVars({
              top: pos.top,
              left: pos.left,
              '--pop-ox': pos.ox,
              '--pop-oy': pos.oy,
              visibility: 'visible',
            })
          : ({ visibility: 'hidden' } as React.CSSProperties)
      }
      tabIndex={-1}
    >
      {children}
    </div>,
    document.body
  )
}
