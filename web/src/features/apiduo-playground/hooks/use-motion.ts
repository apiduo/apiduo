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
import { useReducedMotion, type Transition } from 'motion/react'

/**
 * Springs are named by intent, not by stiffness. Callers say "this settled"
 * or "this popped"; the physics stays in one place, and retuning the whole page
 * means editing three numbers.
 *
 * `visualDuration` is how long the motion *looks* like it takes; `bounce` is
 * overshoot. `bounce: 0` is critical damping — the default, because most of
 * this interface is reading, and reading does not want to bounce.
 */
const SPRINGS = {
  settle: { type: 'spring', visualDuration: 0.35, bounce: 0 },
  pop: { type: 'spring', visualDuration: 0.42, bounce: 0.22 },
  sheet: { type: 'spring', visualDuration: 0.3, bounce: 0.18 },
} satisfies Record<string, Transition>

export type ApiduoSpring = keyof typeof SPRINGS

/**
 * Reduced motion means "no displacement", not "no feedback": callers still get
 * the final state, they just reach it on the next frame.
 */
export function useApiduoSpring(name: ApiduoSpring = 'settle'): Transition {
  const reduce = useReducedMotion()
  return reduce ? { duration: 0 } : SPRINGS[name]
}
