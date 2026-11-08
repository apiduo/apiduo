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
import { AnimatePresence, motion } from 'motion/react'

import { cn } from '@/lib/utils'

import { useApiduoSpring } from '../hooks/use-motion'

interface RollingValueProps {
  value: string
  className?: string
}

/**
 * Slot-machine roll for a number the user is watching change.
 *
 * The old digits leave upward and the new ones arrive from below, so the eye
 * reads "this became that" instead of two unrelated strings swapping places.
 * Reserve it for values that change in place while visible — the session total,
 * a bill settling. A value that changes because the user navigated somewhere
 * else should just render, not roll.
 */
export function RollingValue({ value, className }: RollingValueProps) {
  const spring = useApiduoSpring('settle')

  return (
    <span
      className={cn(
        // `overflow-hidden` turns the vertical offset into a window rather than
        // overlapping the neighbouring label; `leading-none` keeps that window
        // tight to the glyphs so the crop lands on the em box, not on descenders.
        'relative inline-flex overflow-hidden leading-none',
        className
      )}
    >
      <AnimatePresence initial={false} mode='popLayout'>
        <motion.span
          animate={{ y: 0, opacity: 1 }}
          className='inline-block tabular-nums'
          exit={{ y: '-0.9em', opacity: 0 }}
          initial={{ y: '0.9em', opacity: 0 }}
          key={value}
          transition={spring}
        >
          {value}
        </motion.span>
      </AnimatePresence>
    </span>
  )
}
