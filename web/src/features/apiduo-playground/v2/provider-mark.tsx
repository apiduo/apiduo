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

import { cssVars } from './css-vars'

/**
 * The author's mark, not a generic processor icon.
 *
 * A name like `flux-3-image` says nothing about who made it, and a card on the
 * shelf is a claim about one specific model — so it has to answer "whose model
 * is this" at a glance. `@lobehub/icons` already ships the real marks and the
 * logs page uses them, so this borrows the same source instead of drawing a
 * second set.
 *
 * Four deliberate choices:
 *
 * - The Mono cut, always. These marks sit on a dark scrim over artwork and in a
 *   grey chip; a four-colour logo at 11px is noise, and in dark mode it fights
 *   the surface it sits on. Mono inherits `currentColor`, so one rule covers
 *   white-on-badge and tinted-in-chip.
 * - One brand hue per lab, carried as `--apg-tint`. A monochrome set is only
 *   readable as a set if the entries differ by something, and the name is
 *   already doing that job; the hue is what makes two rows from different
 *   labs separable at the edge of vision instead of one grey column.
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

interface MarkSpec {
  Icon: MonoIcon
  /** Brand hue. Held to one lightness band so the set reads as one palette. */
  tint: string
  /** What the lab is called, for the badge's tooltip. */
  label: string
}

/**
 * Families the playground can realistically show. Each entry is its own chunk,
 * so adding a lab here costs nothing until a model from it is on screen.
 * `Wan` is deliberately absent: it is a hand-drawn custom icon in the shared
 * registry, not a LobeHub mark, and at 11px the substitution is not worth it.
 */
const MARKS: Record<string, MarkSpec> = {
  Bfl: {
    Icon: mono(() => import('@lobehub/icons/es/Bfl/components/Mono.js')),
    tint: 'oklch(0.46 0.03 300)',
    label: 'Black Forest Labs',
  },
  Doubao: {
    Icon: mono(() => import('@lobehub/icons/es/Doubao/components/Mono.js')),
    tint: 'oklch(0.6 0.115 205)',
    label: 'ByteDance',
  },
  Jimeng: {
    Icon: mono(() => import('@lobehub/icons/es/Jimeng/components/Mono.js')),
    tint: 'oklch(0.6 0.115 205)',
    label: 'ByteDance',
  },
  OpenAI: {
    Icon: mono(() => import('@lobehub/icons/es/OpenAI/components/Mono.js')),
    tint: 'oklch(0.58 0.02 260)',
    label: 'OpenAI',
  },
  Gemini: {
    Icon: mono(() => import('@lobehub/icons/es/Gemini/components/Mono.js')),
    tint: 'oklch(0.66 0.135 85)',
    label: 'Google',
  },
  Claude: {
    Icon: mono(() => import('@lobehub/icons/es/Claude/components/Mono.js')),
    tint: 'oklch(0.64 0.125 48)',
    label: 'Anthropic',
  },
  Grok: {
    Icon: mono(() => import('@lobehub/icons/es/Grok/components/Mono.js')),
    tint: 'oklch(0.36 0.008 260)',
    label: 'xAI',
  },
  DeepSeek: {
    Icon: mono(() => import('@lobehub/icons/es/DeepSeek/components/Mono.js')),
    tint: 'oklch(0.58 0.155 262)',
    label: 'DeepSeek',
  },
  Qwen: {
    Icon: mono(() => import('@lobehub/icons/es/Qwen/components/Mono.js')),
    tint: 'oklch(0.55 0.16 300)',
    label: 'Alibaba',
  },
  Zhipu: {
    Icon: mono(() => import('@lobehub/icons/es/Zhipu/components/Mono.js')),
    tint: 'oklch(0.53 0.14 255)',
    label: 'Zhipu',
  },
  Hunyuan: {
    Icon: mono(() => import('@lobehub/icons/es/Hunyuan/components/Mono.js')),
    tint: 'oklch(0.55 0.15 248)',
    label: 'Tencent',
  },
  Kling: {
    Icon: mono(() => import('@lobehub/icons/es/Kling/components/Mono.js')),
    tint: 'oklch(0.63 0.14 62)',
    label: 'Kuaishou',
  },
  Minimax: {
    Icon: mono(() => import('@lobehub/icons/es/Minimax/components/Mono.js')),
    tint: 'oklch(0.6 0.15 26)',
    label: 'MiniMax',
  },
  Midjourney: {
    Icon: mono(
      () => import('@lobehub/icons/es/Midjourney/components/Mono.js')
    ),
    tint: 'oklch(0.48 0.025 250)',
    label: 'Midjourney',
  },
  Stability: {
    Icon: mono(
      () => import('@lobehub/icons/es/Stability/components/Mono.js')
    ),
    tint: 'oklch(0.56 0.13 290)',
    label: 'Stability AI',
  },
  Vidu: {
    Icon: mono(() => import('@lobehub/icons/es/Vidu/components/Mono.js')),
    tint: 'oklch(0.58 0.1 190)',
    label: 'Vidu',
  },
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

function specOf(model: string): MarkSpec | null {
  const shared = resolveModelProvider(model)
  if (shared?.icon) {
    const found = MARKS[baseIcon(shared.icon)]
    if (found) return found
  }
  const local = FAMILY_MARKS.find(([pattern]) => pattern.test(model))
  return local ? MARKS[local[1]] : null
}

interface ProviderMarkProps {
  model: string
  size?: number
  /** Drawn when the family has no mark — the chip still needs a leading slot. */
  fallback?: ReactNode
}

/**
 * The bare mark. Used where the mark sits inline with text and a background
 * would fight the row it lives in: the composer chip, example metadata.
 */
export function ProviderMark({ model, size = 11, fallback }: ProviderMarkProps) {
  const spec = specOf(model)
  if (!spec) return fallback ? <span className='apg-by'>{fallback}</span> : null
  const Mark = spec.Icon
  return (
    // The box is sized up front: the chunk behind a lazy icon arrives a tick
    // after the first paint, and without a reserved box the chip would jump
    // sideways once it lands.
    <span
      aria-hidden='true'
      className='apg-by'
      style={cssVars({ height: size, width: size, '--apg-tint': spec.tint })}
    >
      <Suspense fallback={null}>
        <Mark size={size} />
      </Suspense>
    </span>
  )
}

interface ProviderBadgeProps {
  model: string
  /** Inner mark size; the well is always 20 px so the column stays aligned. */
  size?: number
}

/**
 * The mark in a tinted well. A 20 px square of the lab's own hue reads as an
 * author badge at a glance, where a bare 20 px logo would read as noise — and
 * it gives every row the same leading edge whether its icon is a solid flower
 * or a thin drawn glyph.
 */
export function ProviderBadge({ model, size = 12 }: ProviderBadgeProps) {
  const spec = specOf(model)
  if (!spec) return <span aria-hidden='true' className='apg-mby' />
  const Mark = spec.Icon
  return (
    <span
      aria-hidden='true'
      className='apg-mby'
      style={cssVars({ '--apg-tint': spec.tint })}
      title={spec.label}
    >
      <Suspense fallback={null}>
        <Mark size={size} />
      </Suspense>
    </span>
  )
}
