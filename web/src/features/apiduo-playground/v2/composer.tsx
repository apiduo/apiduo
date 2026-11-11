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
import {
  ArrowUpIcon,
  CheckIcon,
  ChevronDownIcon,
  ImageIcon,
  Trash2Icon,
} from 'lucide-react'
import { useCallback, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import type { ModelOption } from '@/features/playground/types'

import {
  capabilityFor,
  RATIOS,
  RATIO_SECTIONS,
  RESOLUTIONS,
  sizeFor,
} from './capabilities'
import { FluidPopover } from './popover'
import { ProviderMark } from './provider-mark'

interface ComposerProps {
  models: ModelOption[]
  model: string
  onModelChange: (model: string) => void
  prompt: string
  onPromptChange: (prompt: string) => void
  ratio: string
  onRatioChange: (ratio: string) => void
  resolution: string
  onResolutionChange: (resolution: string) => void
  count: number
  onCountChange: (count: number) => void
  busy: boolean
  onSubmit: () => void
  /** Formatted per-image price, already group- and discount-adjusted. */
  priceLabel: string | null
  placeholder: string
  /** Generations in flight and the ceiling for this mode. */
  running: number
  cap: number
  /** Clear only makes sense once something has been generated. */
  hasResults: boolean
  onClear: () => void
}

function Tile({
  active,
  ar,
  auto,
  label,
  onClick,
}: {
  active: boolean
  ar: number
  auto?: boolean
  label: string
  onClick: () => void
}) {
  // A 22 px box cannot show 21:9 literally, so the long side is compressed:
  // the point is which side is longer, not the exact proportion.
  let w = 16
  let h = 16
  if (!auto) {
    if (ar >= 1) {
      w = 22
      h = Math.max(6, Math.round(22 / ar))
    } else {
      w = Math.max(6, Math.round(22 * ar))
      h = 22
    }
  }

  return (
    <button
      aria-label={label}
      aria-pressed={active}
      className='apg-tile'
      onClick={onClick}
      title={label}
      type='button'
    >
      <span className='apg-tile-box'>
        <i
          style={{
            width: w,
            height: h,
            borderStyle: auto ? 'dashed' : 'solid',
          }}
        />
      </span>
      <span className='apg-tile-label'>{auto ? 'AUTO' : label}</span>
    </button>
  )
}

export function Composer({
  models,
  model,
  onModelChange,
  prompt,
  onPromptChange,
  ratio,
  onRatioChange,
  resolution,
  onResolutionChange,
  count,
  onCountChange,
  busy,
  onSubmit,
  priceLabel,
  placeholder,
  running,
  cap,
  hasResults,
  onClear,
}: ComposerProps) {
  const { t } = useTranslation()
  const textareaRef = useRef<HTMLTextAreaElement | null>(null)
  const surfaceRef = useRef<HTMLDivElement | null>(null)
  const [open, setOpen] = useState<'model' | 'ratio' | 'res' | 'count' | null>(
    null
  )
  const [anchor, setAnchor] = useState<HTMLElement | null>(null)

  const capability = capabilityFor(model)
  // `auto` is only offered where the model can actually be left to decide; a
  // fixed enum like DALL·E's has no such state.
  const ratiosForModel = RATIOS.filter((item) =>
    capability.ratios.includes(item.id)
  )
  const resolutions = RESOLUTIONS.filter((item) =>
    capability.resolutions.includes(item.id)
  )
  const maxCount = capability.maxCount
  const size = sizeFor(ratio, resolution)

  const openAt = useCallback(
    (kind: 'model' | 'ratio' | 'res' | 'count', node: HTMLElement) => {
      setAnchor(node)
      setOpen((current) => (current === kind ? null : kind))
    },
    []
  )

  const submit = useCallback(() => {
    if (busy || !prompt.trim()) return
    onSubmit()
  }, [busy, onSubmit, prompt])

  const currentModel = models.find((item) => item.value === model)

  return (
    <footer className='apg-composer'>
      <div className='apg-composer-inner' ref={surfaceRef}>
        <textarea
          aria-label={t('Prompt')}
          className='apg-prompt'
          onChange={(event) => onPromptChange(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === 'Enter' && !event.shiftKey) {
              event.preventDefault()
              submit()
            }
          }}
          placeholder={placeholder}
          ref={textareaRef}
          rows={2}
          spellCheck={false}
          value={prompt}
        />

        <div className='apg-bar'>
          <button
            aria-haspopup='dialog'
            aria-label={t('Model')}
            className='apg-chip'
            onClick={(event) => openAt('model', event.currentTarget)}
            type='button'
          >
            <ProviderMark
              fallback={<ImageIcon />}
              model={model}
              size={13}
            />
            <span className='mono'>{currentModel?.label ?? model}</span>
            <ChevronDownIcon className='chev' />
          </button>

          <button
            aria-haspopup='dialog'
            aria-label={t('Aspect ratio')}
            className='apg-chip'
            onClick={(event) => openAt('ratio', event.currentTarget)}
            type='button'
          >
            <span className='mono'>{ratio === 'auto' ? 'AUTO' : ratio}</span>
            <ChevronDownIcon className='chev' />
          </button>

          {resolutions.length > 1 ? (
            <button
              aria-haspopup='dialog'
              aria-label={t('Resolution')}
              className='apg-chip'
              onClick={(event) => openAt('res', event.currentTarget)}
              type='button'
            >
              <span className='mono'>
                {RESOLUTIONS.find((item) => item.id === resolution)?.label}
              </span>
              <ChevronDownIcon className='chev' />
            </button>
          ) : null}

          {/* Price and the primary action travel together: once the chips wrap
              on a narrow screen they stay right-aligned as one block instead
              of stranding the amount at the far left under the avatar. */}
          <div className='apg-bar-end'>
            {/* "how many" is a multiplier on "how much", so it belongs on the
                receipt side of the row — and it keeps the price from being the
                only thing pushed onto a second line on a narrow screen. */}
            <button
              aria-haspopup='dialog'
              aria-label={t('How many')}
              className='apg-chip'
              onClick={(event) => openAt('count', event.currentTarget)}
              type='button'
            >
              <span className='mono'>×{count}</span>
              <ChevronDownIcon className='chev' />
            </button>

            {priceLabel ? (
              /* Unit price, always. Quoting the total with a "/image" suffix
                 prints a number that is neither the price of one nor of the
                 batch; the count chip sits next to it, so the multiplication
                 is on screen. */
              <span className='apg-price'>
                <span aria-hidden='true'>≈</span>
                <b>{priceLabel}</b>
                <span>/{t('per image')}</span>
              </span>
            ) : null}

            <button
              aria-label={t('Clear results')}
              className='apg-clear'
              disabled={!hasResults}
              onClick={onClear}
              title={t('Clear results')}
              type='button'
            >
              <Trash2Icon />
            </button>

            <span
              className='apg-conc'
              data-live={running > 0}
              role='status'
              title={t('{{running}} running · this mode allows {{cap}} at once', {
                running,
                cap,
              })}
            >
              <i aria-hidden='true' />
              <b>
                {running}/{cap}
              </b>
            </span>

            <button
              className='apg-send'
              disabled={busy || prompt.trim().length === 0}
              onClick={submit}
              type='button'
            >
              {t('Generate')}
              <ArrowUpIcon />
            </button>
          </div>
        </div>
      </div>

      <p className='apg-hint'>
        {size ? `${t('Sends {{size}}', { size })} · ` : ''}
        {t('Parameters apply to this request only')}
        {' · ⏎ '}
        {t('Generate')}
        {' · ⇧⏎ '}
        {t('Line break')}
      </p>

      <FluidPopover
        anchor={anchor}
        boundary={surfaceRef.current}
        label={t('Model')}
        onClose={() => setOpen(null)}
        open={open === 'model'}
      >
        <div className='apg-pop-head'>{t('Model')}</div>
        <div className='apg-pop-body'>
          {models.map((item) => (
            <button
              aria-selected={item.value === model}
              className='apg-row'
              key={item.value}
              onClick={() => {
                onModelChange(item.value)
                setOpen(null)
              }}
              role='option'
              type='button'
            >
              <span className='apg-row-main'>
                <span className='apg-row-name'>{item.value}</span>
                {/* `label` is the display name and equals `value` for most
                    providers; repeating it just makes every row look doubled. */}
                {item.label && item.label !== item.value ? (
                  <span className='apg-row-desc'>{item.label}</span>
                ) : null}
              </span>
              {item.value === model ? (
                <CheckIcon className='apg-check' />
              ) : null}
            </button>
          ))}
        </div>
      </FluidPopover>

      <FluidPopover
        anchor={anchor}
        boundary={surfaceRef.current}
        label={t('Aspect ratio')}
        onClose={() => setOpen(null)}
        open={open === 'ratio'}
      >
        <div className='apg-pop-head'>{t('Aspect ratio')}</div>
        <div className='apg-pop-body'>
          {RATIO_SECTIONS.map(([section, label]) => {
            const items = ratiosForModel.filter(
              (item) => item.section === section
            )
            if (items.length === 0) return null
            return (
              <div key={section}>
                <div className='apg-pop-head'>{t(label)}</div>
                <div className='apg-tiles'>
                  {items.map((item) => (
                    <Tile
                      active={item.id === ratio}
                      ar={item.ar}
                      auto={item.auto}
                      key={item.id}
                      label={item.id}
                      onClick={() => {
                        onRatioChange(item.id)
                        setOpen(null)
                      }}
                    />
                  ))}
                </div>
              </div>
            )
          })}
        </div>
      </FluidPopover>

      <FluidPopover
        anchor={anchor}
        boundary={surfaceRef.current}
        label={t('Resolution')}
        onClose={() => setOpen(null)}
        open={open === 'res'}
      >
        <div className='apg-pop-head'>{t('Resolution')}</div>
        <div className='apg-pop-body'>
          {resolutions.map((item) => (
            <button
              aria-selected={item.id === resolution}
              className='apg-row'
              key={item.id}
              onClick={() => {
                onResolutionChange(item.id)
                setOpen(null)
              }}
              role='option'
              type='button'
            >
              <span className='apg-row-main'>
                <span className='apg-row-name'>{item.label}</span>
                <span className='apg-row-desc'>
                  {t(item.note)} · {sizeFor(ratio, item.id) ?? 'auto'}
                </span>
              </span>
              {item.id === resolution ? <CheckIcon className='apg-check' /> : null}
            </button>
          ))}
        </div>
      </FluidPopover>

      <FluidPopover
        anchor={anchor}
        boundary={surfaceRef.current}
        label={t('How many')}
        onClose={() => setOpen(null)}
        open={open === 'count'}
      >
        <div className='apg-pop-head'>{t('How many images')}</div>
        <div className='apg-pop-body'>
          {Array.from({ length: maxCount }, (_, index) => index + 1).map(
            (value) => (
              <button
                aria-selected={value === count}
                className='apg-row'
                key={value}
                onClick={() => {
                  onCountChange(value)
                  setOpen(null)
                }}
                role='option'
                type='button'
              >
                <span className='apg-row-main'>
                  <span className='apg-row-name'>
                    {value} {t('image(s)')}
                  </span>
                </span>
                {value === count ? <CheckIcon className='apg-check' /> : null}
              </button>
            )
          )}
        </div>
      </FluidPopover>
    </footer>
  )
}
