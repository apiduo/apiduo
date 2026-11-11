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

/**
 * The stage before the first request.
 *
 * Image opens on a shelf of worked examples because a picture prompt is hard
 * to write. Chat and video open on one line, because their prompts are not:
 * a question is a question, a shot is a shot. Same block, same spacing, so
 * switching modes moves the content, not the furniture.
 */
export function StageEmpty({
  icon,
  title,
  hint,
}: {
  icon: ReactNode
  title: string
  hint: string
}) {
  return (
    <div className='apg-empty'>
      <span aria-hidden='true' className='apg-empty-icon'>
        {icon}
      </span>
      <p className='apg-empty-title'>{title}</p>
      <p className='apg-empty-sub'>{hint}</p>
    </div>
  )
}
