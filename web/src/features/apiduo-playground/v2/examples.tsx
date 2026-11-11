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
import { ShuffleIcon } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

/**
 * The empty stage is a shelf of prompts, not a blank box with a placeholder.
 *
 * A prompt box asks "what do you want?"; three worked examples answer it by
 * showing the *shape* of a good request — subject, light, framing — which is
 * the thing people actually get stuck on.
 *
 * The artwork is drawn here rather than shipped as files: three 20 kB
 * illustrations would be the heaviest thing on a page whose real content is
 * generated, and inline SVG inherits the theme without a second asset set.
 */

interface Example {
  id: string
  model: string
  ratio: string
  title: string
  prompt: string
  art: 0 | 1 | 2
}

const EXAMPLES: Example[] = [
  {
    id: 'server-room',
    model: 'flux-3-lage',
    ratio: '16:9',
    title: 'Isometric server room',
    prompt:
      'An isometric server room at night, teal and amber lighting, cables floating as light trails, clean vector look, deep shadows',
    art: 0,
  },
  {
    id: 'coastal-map',
    model: 'gpt-image-1',
    ratio: '3:2',
    title: 'Hand-drawn coastal map',
    prompt:
      'A hand-drawn map of a small coastal town, ink on aged paper, route line with a few marked stops, pencil contour lines, no labels',
    art: 1,
  },
  {
    id: 'glass-teapot',
    model: 'sdream-3.0',
    ratio: '1:1',
    title: 'Glass teapot, morning light',
    prompt:
      'A glass teapot on a windowsill in morning light, soft shadows, dust in the air, muted cream background, product photography',
    art: 2,
  },
]

function ArtIso() {
  return (
    <svg aria-hidden='true' viewBox='0 0 240 180'>
      <defs>
        <linearGradient id='apg-iso-bg' x1='0' x2='1' y1='0' y2='1'>
          <stop offset='0' stopColor='#0d2a2e' />
          <stop offset='0.55' stopColor='#12383a' />
          <stop offset='1' stopColor='#3a2a12' />
        </linearGradient>
        <radialGradient id='apg-iso-glow' cx='0.74' cy='0.82' r='0.5'>
          <stop offset='0' stopColor='#f0a93c' stopOpacity='0.85' />
          <stop offset='1' stopColor='#f0a93c' stopOpacity='0' />
        </radialGradient>
      </defs>
      <rect fill='url(#apg-iso-bg)' height='180' width='240' />
      <rect fill='url(#apg-iso-glow)' height='180' width='240' />
      <g
        fill='none'
        stroke='#7fe3d4'
        strokeLinecap='round'
        strokeLinejoin='round'
        strokeWidth='1.1'
      >
        <path d='M120 52l38 20v40l-38 20-38-20V72z' opacity='0.9' />
        <path d='M82 72l38 20 38-20M120 92v40' opacity='0.55' />
        <path d='M96 64l24 13 24-13' opacity='0.35' />
      </g>
      <g fill='#7fe3d4'>
        <circle cx='120' cy='92' opacity='0.9' r='2.2' />
        <circle cx='96' cy='146' opacity='0.75' r='2' />
        <circle cx='151' cy='132' opacity='0.6' r='1.6' />
      </g>
      <g fill='#f6c66b' opacity='0.9'>
        <circle cx='150' cy='63' r='1.7' />
        <circle cx='158' cy='70' r='1.2' opacity='0.7' />
        <circle cx='88' cy='120' r='1.4' opacity='0.8' />
      </g>
    </svg>
  )
}

function ArtMap() {
  return (
    <svg aria-hidden='true' viewBox='0 0 240 180'>
      <defs>
        <linearGradient id='apg-map-bg' x1='0' x2='1' y1='0' y2='1'>
          <stop offset='0' stopColor='#f3e7cf' />
          <stop offset='1' stopColor='#e8d8b8' />
        </linearGradient>
      </defs>
      <rect fill='url(#apg-map-bg)' height='180' width='240' />
      <g stroke='#c8ad7e' strokeWidth='0.6' opacity='0.75'>
        <path d='M0 45h240M0 90h240M0 135h240M60 0v180M120 0v180M180 0v180' />
      </g>
      <g fill='none' stroke='#7d8a5c' strokeWidth='1.4'>
        <path d='M-6 118c34-10 52-34 76-32s30 26 56 22 42-22 60-18' />
        <path d='M6 60c26 6 40 22 62 20s28-18 48-16 32 16 52 12' opacity='0.7' />
      </g>
      <g fill='none' stroke='#3f5f72' strokeWidth='1.2' opacity='0.85'>
        <path d='M118 4c-14 26 8 44-4 66s-26 30-18 52 30 26 26 54' />
      </g>
      <g fill='#b4763f'>
        <circle cx='118' cy='72' r='2.4' />
        <circle cx='166' cy='86' r='2' />
        <circle cx='84' cy='118' r='1.8' />
      </g>
      <g fill='none' stroke='#b4763f' strokeWidth='0.9' opacity='0.8'>
        <path d='M100 100l10-8 9 3 8-11' />
      </g>
      <path
        d='M176 44l14 2-3 12-13-2z'
        fill='none'
        opacity='0.5'
        stroke='#8b6b45'
        strokeWidth='0.9'
      />
      <path
        d='M198 128l10-5 6 8-9 6z'
        fill='none'
        opacity='0.45'
        stroke='#8b6b45'
        strokeWidth='0.9'
      />
    </svg>
  )
}

function ArtOrb() {
  return (
    <svg aria-hidden='true' viewBox='0 0 240 180'>
      <defs>
        <linearGradient id='apg-orb-bg' x1='0' x2='0' y1='0' y2='1'>
          <stop offset='0' stopColor='#efe4d2' />
          <stop offset='0.62' stopColor='#e2d3bd' />
          <stop offset='1' stopColor='#b9a184' />
        </linearGradient>
        <radialGradient id='apg-orb-ball' cx='0.38' cy='0.32' r='0.72'>
          <stop offset='0' stopColor='#ffffff' />
          <stop offset='0.42' stopColor='#dfe8ec' />
          <stop offset='1' stopColor='#aebfc6' />
        </radialGradient>
        <radialGradient id='apg-orb-shadow' cx='0.5' cy='0.5' r='0.5'>
          <stop offset='0' stopColor='#6b5335' stopOpacity='0.42' />
          <stop offset='1' stopColor='#6b5335' stopOpacity='0' />
        </radialGradient>
      </defs>
      <rect fill='url(#apg-orb-bg)' height='180' width='240' />
      <ellipse cx='120' cy='150' fill='url(#apg-orb-shadow)' rx='52' ry='11' />
      <circle cx='120' cy='96' fill='url(#apg-orb-ball)' r='42' />
      <ellipse
        cx='104'
        cy='78'
        fill='#ffffff'
        opacity='0.75'
        rx='11'
        ry='8'
        transform='rotate(-24 104 78)'
      />
      <path
        d='M150 60l16 4-3 12-16-4z'
        fill='#f4e2a8'
        opacity='0.7'
        stroke='none'
      />
    </svg>
  )
}

const ART = [ArtIso, ArtMap, ArtOrb]

interface ExamplesProps {
  onPick: (prompt: string) => void
}

export function ExamplesShelf({ onPick }: ExamplesProps) {
  const { t } = useTranslation()
  const [offset, setOffset] = useState(0)

  const visible = [0, 1, 2].map((i) => EXAMPLES[(i + offset) % EXAMPLES.length])

  return (
    <section className='apg-suggest'>
      <div className='apg-suggest-head'>
        <div>
          <div className='apg-suggest-title'>{t('Start from an idea')}</div>
          <div className='apg-suggest-sub'>
            {t('A finished prompt, so you can see the shape of one')}
          </div>
        </div>
        <button
          className='apg-shuffle'
          onClick={() => setOffset((value) => (value + 3) % EXAMPLES.length || 1)}
          type='button'
        >
          <ShuffleIcon className='size-3' />
          {t('Shuffle')}
        </button>
      </div>

      <div className='apg-ex-row'>
        {visible.map((example, index) => {
          const Art = ART[example.art]
          return (
            <button
              className='apg-ex'
              key={example.id}
              onClick={() => onPick(example.prompt)}
              style={{ animationDelay: `${index * 60}ms` }}
              type='button'
            >
              <span className='apg-ex-thumb'>
                <Art />
              </span>
              <span className='apg-ex-copy'>
                <span className='apg-ex-meta'>
                  <span className='apg-ex-model'>{example.model}</span>
                  <span className='apg-ex-ratio'>{example.ratio}</span>
                </span>
                <span className='apg-ex-title'>{t(example.title)}</span>
                <span className='apg-ex-p'>{t(example.prompt)}</span>
              </span>
            </button>
          )
        })}
      </div>
    </section>
  )
}
