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
import { useQueryClient } from '@tanstack/react-query'
import { Check } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { cn } from '@/lib/utils'
import {
  APPEARANCE_PRESETS,
  matchPreset,
  presetToAppearance,
  type ApiduoAppearance,
  type ApiduoEffectStyle,
} from '@/features/apiduo-appearance'
import { APIDUO_HOLIDAYS } from '@/features/apiduo-appearance/holidays'

import {
  SettingsFormGrid,
  SettingsFormGridItem,
  SettingsSwitchField,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useUpdateOptionsBatch } from '../hooks/use-update-options-batch'
import type { SiteSettings } from '../types'

const SPEED_MIN = 600
const SPEED_MAX = 12000
const RING_MIN = 0.5
const RING_MAX = 4
const TAIL_MIN = 30
const TAIL_MAX = 300

/** Swatches are mid-luminance so the highlight stays legible on both themes. */
/** Readable names for the effect styles: the enum value never reaches the UI. */
const STYLE_LABELS: Record<ApiduoEffectStyle, string> = {
  flow: 'Flowing',
  rainbow: 'Rainbow',
  spotlight: 'Spotlight',
  sheen: 'Sheen',
  pulse: 'Pulse',
  none: 'None',
}

/** What "Restore defaults" puts back: the values a fresh install starts with. */
const DEFAULT_APPEARANCE: ApiduoAppearance = {
  enabled: true,
  style: 'flow',
  color: '',
  speed_ms: 3200,
  ring_px: 1.5,
  tail_deg: 120,
  glow: true,
  holiday: '',
  holiday_from: '',
  holiday_to: '',
}

const COLOR_SWATCHES = [
  { value: '', labelKey: 'Theme' },
  { value: '#e11d48', labelKey: 'Red' },
  { value: '#f97316', labelKey: 'Orange' },
  { value: '#f59e0b', labelKey: 'Amber' },
  { value: '#16a34a', labelKey: 'Green' },
  { value: '#06b6d4', labelKey: 'Teal' },
  { value: '#3b82f6', labelKey: 'Blue' },
  { value: '#8b5cf6', labelKey: 'Violet' },
  { value: '#ec4899', labelKey: 'Pink' },
] as const

function buildState(settings: SiteSettings): ApiduoAppearance {
  return {
    enabled: settings['apiduo_appearance_setting.enabled'] ?? true,
    style:
      (settings['apiduo_appearance_setting.style'] as ApiduoEffectStyle) ??
      'flow',
    color: settings['apiduo_appearance_setting.color'] ?? '',
    speed_ms: settings['apiduo_appearance_setting.speed_ms'] ?? 3200,
    ring_px: settings['apiduo_appearance_setting.ring_px'] ?? 1.5,
    tail_deg: settings['apiduo_appearance_setting.tail_deg'] ?? 120,
    glow: settings['apiduo_appearance_setting.glow'] ?? true,
    holiday:
      (settings[
        'apiduo_appearance_setting.holiday'
      ] as ApiduoAppearance['holiday']) ?? '',
    holiday_from: settings['apiduo_appearance_setting.holiday_from'] ?? '',
    holiday_to: settings['apiduo_appearance_setting.holiday_to'] ?? '',
  }
}

function serialize(state: ApiduoAppearance): string {
  return JSON.stringify(state)
}

/**
 * Rounds the two continuous fields the way they are stored. Comparing the raw
 * slider value against a saved integer would otherwise leave the form dirty
 * forever, and send a "change" that the server rounds back to the same number.
 */
function normalize(state: ApiduoAppearance): ApiduoAppearance {
  return {
    ...state,
    speed_ms: Math.round(state.speed_ms),
    tail_deg: Math.round(state.tail_deg),
  }
}

/**
 * The decoration apiduo paints on its own surfaces.
 *
 * The form edits a flat snapshot rather than a preset id. Presets are a way to
 * fill the form quickly, not a runtime binding: once an operator touches a
 * slider the numbers are theirs, and saving a preset name would quietly discard
 * that edit on the next load.
 */
export function AppearanceSettingsSection(props: {
  settings: SiteSettings
}) {
  const { t } = useTranslation()
  const updateOptions = useUpdateOptionsBatch()
  const queryClient = useQueryClient()

  const initial = useMemo(() => buildState(props.settings), [props.settings])
  const [state, setState] = useState<ApiduoAppearance>(initial)
  const [baseline, setBaseline] = useState<ApiduoAppearance>(initial)

  useEffect(() => {
    setState(initial)
    setBaseline(initial)
  }, [initial])

  const dirty = serialize(normalize(state)) !== serialize(normalize(baseline))
  const activePreset = matchPreset(state)

  const patch = (next: Partial<ApiduoAppearance>) =>
    setState((current) => ({ ...current, ...next }))

  const applyPreset = (presetId: string) => {
    const preset = APPEARANCE_PRESETS.find((item) => item.id === presetId)
    if (!preset) return
    setState((current) => ({
      ...presetToAppearance(preset),
      enabled: current.enabled,
      // A look describes the effect; the site-wide palette is not part of it.
      holiday: current.holiday,
      holiday_from: current.holiday_from,
      holiday_to: current.holiday_to,
    }))
  }

  const onSave = async () => {
    if (!dirty) {
      toast.info(t('No changes to save'))
      return
    }
    const next = normalize(state)
    const base = normalize(baseline)
    // Only the fields the operator actually touched: an unchanged field must
    // not reach the audit trail as if it had been rewritten.
    const options: Record<string, string | boolean | number> = {}
    if (next.enabled !== base.enabled) {
      options['apiduo_appearance_setting.enabled'] = next.enabled
    }
    if (next.style !== base.style) {
      options['apiduo_appearance_setting.style'] = next.style
    }
    if (next.color !== base.color) {
      options['apiduo_appearance_setting.color'] = next.color
    }
    if (next.speed_ms !== base.speed_ms) {
      options['apiduo_appearance_setting.speed_ms'] = next.speed_ms
    }
    if (next.ring_px !== base.ring_px) {
      options['apiduo_appearance_setting.ring_px'] = next.ring_px
    }
    if (next.tail_deg !== base.tail_deg) {
      options['apiduo_appearance_setting.tail_deg'] = next.tail_deg
    }
    if (next.glow !== base.glow) {
      options['apiduo_appearance_setting.glow'] = next.glow
    }
    if (next.holiday !== base.holiday) {
      options['apiduo_appearance_setting.holiday'] = next.holiday
    }
    if (next.holiday_from !== base.holiday_from) {
      options['apiduo_appearance_setting.holiday_from'] = next.holiday_from
    }
    if (next.holiday_to !== base.holiday_to) {
      options['apiduo_appearance_setting.holiday_to'] = next.holiday_to
    }

    if (Object.keys(options).length === 0) {
      setBaseline(next)
      toast.info(t('No changes to save'))
      return
    }

    await updateOptions.mutateAsync({ options })
    setBaseline(next)
    // The runtime reads /api/appearance, not /api/option. Refreshing that query
    // makes the new look visible in this tab without a reload.
    await queryClient.invalidateQueries({ queryKey: ['apiduo-appearance'] })
  }

  // Reset only touches the form. The stored configuration changes when the
  // operator saves, so a misclick is undone by reloading the page.
  const onReset = () => setState(DEFAULT_APPEARANCE)

  return (
    <SettingsSection title={t('Appearance effects')}>
      <SettingsPageFormActions
        onSave={onSave}
        onReset={onReset}
        isSaving={updateOptions.isPending}
        isResetDisabled={!dirty}
        resetLabel='Restore defaults'
      />

      <div className='space-y-8'>
        <p className='text-muted-foreground text-xs'>
          {t(
            'Decorative highlight painted on the surfaces apiduo adds. It is what makes them readable as one family, and it follows the theme colour unless a colour is chosen below.'
          )}
        </p>

        <SettingsFormGrid>
          <SettingsSwitchField
            controlId='apiduo-appearance-enabled'
            checked={state.enabled}
            onCheckedChange={(checked) => patch({ enabled: checked })}
            label={t('Enable appearance effects')}
            description={t(
              'When off, apiduo surfaces render exactly like upstream ones.'
            )}
          />
        </SettingsFormGrid>

        <div className='space-y-3'>
          <Label>{t('Appearance presets')}</Label>
          <div className='grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-5'>
            {APPEARANCE_PRESETS.map((preset) => (
              <button
                key={preset.id}
                type='button'
                aria-pressed={activePreset?.id === preset.id}
                onClick={() => applyPreset(preset.id)}
                className={cn(
                  'group relative flex flex-col gap-1 rounded-lg border p-2.5 text-left transition-colors',
                  'hover:border-primary/40 hover:bg-primary/5',
                  activePreset?.id === preset.id
                    ? 'border-primary/50 bg-primary/5'
                    : 'border-border'
                )}
              >
                <span className='flex items-center gap-1.5'>
                  <span
                    className='size-2.5 shrink-0 rounded-full'
                    style={{
                      background: preset.color || 'var(--primary)',
                    }}
                  />
                  <span className='truncate text-xs font-medium'>
                    {t(preset.labelKey)}
                  </span>
                  {activePreset?.id === preset.id && (
                    <Check className='text-primary ml-auto size-3 shrink-0' />
                  )}
                </span>
                <span className='text-muted-foreground line-clamp-2 text-[11px] leading-tight'>
                  {t(preset.hintKey)}
                </span>
              </button>
            ))}
          </div>
        </div>

        <div className='space-y-3'>
          <Label>{t('Highlight colour')}</Label>
          <div className='flex flex-wrap items-center gap-2'>
            {COLOR_SWATCHES.map((swatch) => (
              <button
                key={swatch.value || 'theme'}
                type='button'
                title={t(swatch.labelKey)}
                aria-label={t(swatch.labelKey)}
                aria-pressed={state.color === swatch.value}
                onClick={() => patch({ color: swatch.value })}
                className={cn(
                  'size-7 rounded-full border transition-transform hover:scale-110',
                  state.color === swatch.value
                    ? 'ring-primary ring-offset-background ring-2 ring-offset-2'
                    : 'border-black/10'
                )}
                style={{
                  background: swatch.value || 'var(--primary)',
                }}
              />
            ))}
            <Input
              type='color'
              aria-label={t('Custom colour')}
              className='size-7 cursor-pointer appearance-none rounded-full border border-black/10 bg-transparent p-0 [&::-moz-color-swatch]:rounded-full [&::-moz-color-swatch]:border-0 [&::-webkit-color-swatch-wrapper]:p-0 [&::-webkit-color-swatch]:rounded-full [&::-webkit-color-swatch]:border-0'
              value={state.color || '#3b82f6'}
              onChange={(event) => patch({ color: event.target.value })}
            />
          </div>
        </div>

        <SettingsFormGrid>
          <SettingsFormGridItem span='full'>
            <div className='grid gap-5 sm:grid-cols-3'>
              <div className='space-y-2'>
                <div className='flex items-baseline justify-between'>
                  <Label htmlFor='apiduo-appearance-speed'>
                    {t('One revolution')}
                  </Label>
                  <span className='text-muted-foreground text-xs tabular-nums'>
                    {(state.speed_ms / 1000).toFixed(1)}s
                  </span>
                </div>
                <input
                  id='apiduo-appearance-speed'
                  type='range'
                  min={SPEED_MIN}
                  max={SPEED_MAX}
                  step={100}
                  value={state.speed_ms}
                  onChange={(event) =>
                    patch({ speed_ms: Number(event.target.value) })
                  }
                  className='accent-primary w-full'
                />
              </div>
              <div className='space-y-2'>
                <div className='flex items-baseline justify-between'>
                  <Label htmlFor='apiduo-appearance-ring'>
                    {t('Stroke width')}
                  </Label>
                  <span className='text-muted-foreground text-xs tabular-nums'>
                    {state.ring_px.toFixed(1)}px
                  </span>
                </div>
                <input
                  id='apiduo-appearance-ring'
                  type='range'
                  min={RING_MIN}
                  max={RING_MAX}
                  step={0.1}
                  value={state.ring_px}
                  onChange={(event) =>
                    patch({ ring_px: Number(event.target.value) })
                  }
                  className='accent-primary w-full'
                />
              </div>
              <div className='space-y-2'>
                <div className='flex items-baseline justify-between'>
                  <Label htmlFor='apiduo-appearance-tail'>
                    {t('Highlight length')}
                  </Label>
                  <span className='text-muted-foreground text-xs tabular-nums'>
                    {Math.round(state.tail_deg)}°
                  </span>
                </div>
                <input
                  id='apiduo-appearance-tail'
                  type='range'
                  min={TAIL_MIN}
                  max={TAIL_MAX}
                  step={5}
                  value={state.tail_deg}
                  onChange={(event) =>
                    patch({ tail_deg: Number(event.target.value) })
                  }
                  className='accent-primary w-full'
                />
              </div>
            </div>
          </SettingsFormGridItem>

          <SettingsSwitchField
            controlId='apiduo-appearance-glow'
            checked={state.glow}
            onCheckedChange={(checked) => patch({ glow: checked })}
            label={t('Soft glow')}
            description={t(
              'Adds a bloom around the highlight. Costs one filter pass per surface.'
            )}
          />
        </SettingsFormGrid>

        <AppearancePreview appearance={state} />

        <div className='space-y-3'>
          <div>
            <Label>{t('Holiday skin')}</Label>
            <p className='text-muted-foreground mt-1 text-xs'>
              {t(
                'Recolours the whole console — buttons, links, charts and the effect together. A colour picked above still wins for the effect itself.'
              )}
            </p>
          </div>
          <div className='flex flex-wrap gap-2'>
            <button
              type='button'
              aria-pressed={state.holiday === ''}
              onClick={() =>
                patch({ holiday: '', holiday_from: '', holiday_to: '' })
              }
              className={cn(
                'rounded-lg border px-3 py-1.5 text-xs font-medium transition-colors',
                'hover:border-primary/40 hover:bg-primary/5',
                state.holiday === ''
                  ? 'border-primary/50 bg-primary/5'
                  : 'border-border'
              )}
            >
              {t('None')}
            </button>
            {APIDUO_HOLIDAYS.map((holiday) => (
              <button
                key={holiday.id}
                type='button'
                aria-pressed={state.holiday === holiday.id}
                onClick={() =>
                  patch({
                    holiday: holiday.id,
                    // Only seed the window when it is empty: an operator who
                    // moved the dates must not have them reset by a re-click.
                    ...(state.holiday_from || state.holiday_to
                      ? {}
                      : { holiday_from: holiday.from, holiday_to: holiday.to }),
                  })
                }
                className={cn(
                  'rounded-lg border px-3 py-1.5 text-xs font-medium transition-colors',
                  'hover:border-primary/40 hover:bg-primary/5',
                  state.holiday === holiday.id
                    ? 'border-primary/50 bg-primary/5'
                    : 'border-border'
                )}
              >
                {t(holiday.labelKey)}
              </button>
            ))}
          </div>
          {state.holiday !== '' && (
            <div className='flex flex-wrap items-end gap-3'>
              <div className='space-y-1'>
                <Label htmlFor='apiduo-holiday-from'>{t('From')}</Label>
                <Input
                  id='apiduo-holiday-from'
                  className='w-28'
                  placeholder='MM-DD'
                  value={state.holiday_from}
                  onChange={(event) =>
                    patch({ holiday_from: event.target.value })
                  }
                />
              </div>
              <div className='space-y-1'>
                <Label htmlFor='apiduo-holiday-to'>{t('To')}</Label>
                <Input
                  id='apiduo-holiday-to'
                  className='w-28'
                  placeholder='MM-DD'
                  value={state.holiday_to}
                  onChange={(event) =>
                    patch({ holiday_to: event.target.value })
                  }
                />
              </div>
              <p className='text-muted-foreground max-w-sm pb-2 text-xs leading-snug'>
                {t(
                  'Month and day only, so the same window works every year. Leave both empty to keep it on until you turn it off.'
                )}
              </p>
            </div>
          )}
        </div>
      </div>
    </SettingsSection>
  )
}

/**
 * Live sample rendered from the form's current numbers.
 *
 * It renders the layer directly instead of going through <ApiduoGlow>, because
 * that component reads the *saved* configuration: the preview has to show what
 * the operator is about to save, not what is currently live.
 */
function AppearancePreview(props: { appearance: ApiduoAppearance }) {
  const { t } = useTranslation()
  const { enabled, style, color, speed_ms, ring_px, tail_deg, glow, holiday } =
    props.appearance

  const vars = {
    '--ap-speed': `${speed_ms}ms`,
    '--ap-ring': `${ring_px}px`,
    '--ap-tail': `${tail_deg}deg`,
    ...(color ? { '--ap-color': color } : {}),
  } as React.CSSProperties

  return (
    <div className='space-y-2'>
      <Label>{t('Preview')}</Label>
      <div className='bg-muted/30 grid gap-3 rounded-lg border p-4 sm:grid-cols-2'>
        <div
          data-ap-surface=''
          data-ap-holiday={holiday || undefined}
          style={vars}
          className={cn(
            'relative overflow-visible rounded-xl border bg-card p-4',
            enabled ? 'border-primary/40' : 'border-border'
          )}
        >
          {enabled && style !== 'none' && (
            <>
              {(style === 'flow' || style === 'rainbow') && (
                <span
                  aria-hidden='true'
                  data-ap-glow={glow ? 'on' : 'off'}
                  className={cn(
                    'ap-glow',
                    style === 'rainbow' && 'ap-glow-rainbow'
                  )}
                />
              )}
              {style === 'spotlight' && (
                <span aria-hidden='true' className='ap-spot' />
              )}
              {style === 'sheen' && (
                <span aria-hidden='true' className='ap-sheen' />
              )}
              {style === 'pulse' && (
                <span aria-hidden='true' className='ap-pulse' />
              )}
            </>
          )}
          <div className='text-sm font-medium'>{t('Advanced Custom')}</div>
          <p className='text-muted-foreground mt-1 text-xs'>
            {t('This is how every apiduo surface will look.')}
          </p>
          <span className='bg-primary text-primary-foreground mt-2 inline-flex rounded-md px-2 py-0.5 text-[11px] font-medium'>
            {t('Accent')}
          </span>
        </div>
        <div className='text-muted-foreground flex flex-col justify-center gap-1 text-xs'>
          <p>
            {t('Style')}:{' '}
            <span className='text-foreground'>{t(STYLE_LABELS[style])}</span>
          </p>
          <p>
            {t('Highlight colour')}:{' '}
            <span className='text-foreground'>
              {color || t('Follow theme')}
            </span>
          </p>
          <p>
            {t('One revolution')}:{' '}
            <span className='text-foreground'>
              {(speed_ms / 1000).toFixed(1)}s
            </span>
          </p>
        </div>
      </div>
    </div>
  )
}
