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

import { lazy, Suspense, type ComponentType, type ReactNode } from 'react'

import { resolveModelProvider } from '@/lib/model-provider'

/**
 * The author's mark, not a generic processor icon.
 *
 * A name like `flux-3-image` says nothing about who made it, and a card on the
 * shelf is a claim about one specific model — so it has to answer "whose model
 * is this" at a glance. `@lobehub/icons` already ships the real marks and the
 * logs page uses them, so this borrows the same source instead of drawing a
 * second set.
 *
 * Three deliberate choices:
 *
 * - The Mono cut, always. These marks sit on a dark scrim over artwork and in a
 *   grey chip; a four-colour logo at 11px is noise, and in dark mode it fights
 *   the surface it sits on. Mono inherits `currentColor`, so one rule covers
 *   white-on-badge and ink-in-chip.
 * - Explicit imports rather than the generic loader in `lib/lobe-icon`. That
 *   loader renders the icon's first letter while the chunk is in flight, which
 *   at 11px inside a badge reads as a typo ("Ddoubao-…"). Here the suspense
 *   fallback is nothing: the mark arrives, it never masquerades as text.
 * - Unknown families render nothing. A generic chip icon claims "this is a
 *   model" without saying which one, which is exactly what the badge is for.
 */

type MonoIcon = ComponentType<{ size?: number }>

const mono = (
  loader: () => Promise<{ default: ComponentType<Record<string, unknown>> }>
): MonoIcon => lazy(loader) as unknown as MonoIcon

/**
 * Families the playground can realistically show. Each entry is its own chunk,
 * so adding a lab here costs nothing until a model from it is on screen.
 * `Wan` is deliberately absent: it is a hand-drawn custom icon in the shared
 * registry, not a LobeHub mark, and at 11px the substitution is not worth it.
 */
const MARKS: Record<string, MonoIcon> = {
  Bfl: mono(() => import('@lobehub/icons/es/Bfl/components/Mono.js')),
  Doubao: mono(() => import('@lobehub/icons/es/Doubao/components/Mono.js')),
  OpenAI: mono(() => import('@lobehub/icons/es/OpenAI/components/Mono.js')),
  Gemini: mono(() => import('@lobehub/icons/es/Gemini/components/Mono.js')),
  Qwen: mono(() => import('@lobehub/icons/es/Qwen/components/Mono.js')),
  Kling: mono(() => import('@lobehub/icons/es/Kling/components/Mono.js')),
  Minimax: mono(() => import('@lobehub/icons/es/Minimax/components/Mono.js')),
  Midjourney: mono(
    () => import('@lobehub/icons/es/Midjourney/components/Mono.js')
  ),
  Stability: mono(
    () => import('@lobehub/icons/es/Stability/components/Mono.js')
  ),
  Claude: mono(() => import('@lobehub/icons/es/Claude/components/Mono.js')),
  Grok: mono(() => import('@lobehub/icons/es/Grok/components/Mono.js')),
  DeepSeek: mono(
    () => import('@lobehub/icons/es/DeepSeek/components/Mono.js')
  ),
  Jimeng: mono(() => import('@lobehub/icons/es/Jimeng/components/Mono.js')),
  Vidu: mono(() => import('@lobehub/icons/es/Vidu/components/Mono.js')),
  Hunyuan: mono(() => import('@lobehub/icons/es/Hunyuan/components/Mono.js')),
  Zhipu: mono(() => import('@lobehub/icons/es/Zhipu/components/Mono.js')),
}

/**
 * Families the shared rules cannot see. `flux-3-image` has no dot after the
 * family name and the log-side rule only knows the `flux.` spelling, so without
 * this the most common model on the shelf would be the one without a mark.
 */
const FAMILY_MARKS: ReadonlyArray<[RegExp, string]> = [
  [/(?:^|[/._-])flux(?:[._-]|$)/i, 'Bfl'],
]

/** `OpenAI.Color` → `OpenAI`: the caller always wants the Mono cut. */
const baseIcon = (icon: string) => icon.split('.')[0]

function providerIconOf(model: string): string | null {
  const shared = resolveModelProvider(model)
  if (shared?.icon) return baseIcon(shared.icon)
  const local = FAMILY_MARKS.find(([pattern]) => pattern.test(model))
  return local ? local[1] : null
}

interface ProviderMarkProps {
  model: string
  size?: number
  /** Drawn when the family has no mark — the chip still needs a leading slot. */
  fallback?: ReactNode
}

export function ProviderMark({ model, size = 11, fallback }: ProviderMarkProps) {
  const icon = providerIconOf(model)
  const Mark = icon ? MARKS[icon] : undefined
  if (!Mark) return fallback ? <span className='apg-by'>{fallback}</span> : null
  return (
    // The box is sized up front: the chunk behind a lazy icon arrives a tick
    // after the first paint, and without a reserved box the chip would jump
    // sideways once it lands.
    <span
      aria-hidden='true'
      className='apg-by'
      style={{ height: size, width: size }}
    >
      <Suspense fallback={null}>
        <Mark size={size} />
      </Suspense>
    </span>
  )
}
