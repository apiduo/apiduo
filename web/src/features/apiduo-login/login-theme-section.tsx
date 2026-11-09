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
import { Check } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { cn } from '@/lib/utils'

import { SettingsPageFormActions } from '../system-settings/components/settings-page-context'
import { SettingsSection } from '../system-settings/components/settings-section'
import { useSystemOptions } from '../system-settings/hooks/use-system-options'
import { useUpdateOption } from '../system-settings/hooks/use-update-option'
import {
  DEFAULT_LOGIN_SKIN,
  LOGIN_SKIN_IDS,
  LOGIN_SKIN_LABELS,
  isLoginSkinId,
  type LoginSkinId,
} from './skin'

/** 缩略图只用几块色，不渲染整页；选中与否靠边框和勾，不靠色差。 */
const SWATCH: Record<LoginSkinId, { bg: string; card: string; bar: string }> = {
  aurora: {
    bg: 'linear-gradient(135deg,#0b1020,#101a2e 55%,#1a1030)',
    card: 'rgba(22,27,39,.95)',
    bar: 'linear-gradient(90deg,#35d6f0,#7c5cf6,#ff8fc7)',
  },
  light: {
    bg: 'linear-gradient(160deg,#f7f9fc,#eef1f7)',
    card: '#ffffff',
    bar: 'linear-gradient(90deg,#2f5bff,#7b4dff)',
  },
  terminal: {
    bg: '#060b09',
    card: '#0a100d',
    bar: '#5cf2a0',
  },
  editorial: {
    bg: 'linear-gradient(160deg,#f7f3ea,#ece6da)',
    card: 'transparent',
    bar: '#a8341b',
  },
  cyber: {
    bg: 'linear-gradient(180deg,#0a0416 50%,#1a0630)',
    card: 'rgba(16,8,28,.95)',
    bar: 'linear-gradient(90deg,#ff2d95,#22e0ff)',
  },
  warm: {
    bg: 'linear-gradient(150deg,#f8f3ea,#f1e9dc)',
    card: '#fffdf8',
    bar: '#1d3b32',
  },
}

export function LoginThemeSection() {
  const { t } = useTranslation()
  const { data } = useSystemOptions()
  const updateOption = useUpdateOption()

  const saved = useMemo<LoginSkinId>(() => {
    const raw = data?.data?.find((option) => option.key === 'LoginTheme')?.value
    return isLoginSkinId(raw) ? raw : DEFAULT_LOGIN_SKIN
  }, [data])

  const [selected, setSelected] = useState<LoginSkinId>(saved)

  useEffect(() => {
    setSelected(saved)
  }, [saved])

  const dirty = selected !== saved

  return (
    <SettingsSection title={t('Sign-in style')}>
      <SettingsPageFormActions
        onSave={() => {
          if (!dirty) return
          void updateOption.mutateAsync({ key: 'LoginTheme', value: selected })
        }}
        isSaving={updateOption.isPending}
        saveLabel='Save sign-in style'
        isSaveDisabled={!dirty}
      />

      <p className='text-muted-foreground mb-4 text-sm'>
        {t(
          'Choose the skin visitors see on the sign-in page. All skins ship with the build, so switching does not require a rebuild or a restart.'
        )}
      </p>

      <div className='grid gap-3 sm:grid-cols-2 xl:grid-cols-3'>
        {LOGIN_SKIN_IDS.map((id) => {
          const meta = LOGIN_SKIN_LABELS[id]
          const swatch = SWATCH[id]
          const active = selected === id
          return (
            <button
              key={id}
              type='button'
              onClick={() => setSelected(id)}
              aria-pressed={active}
              className={cn(
                'group relative overflow-hidden rounded-xl border p-2 text-left transition-colors',
                active
                  ? 'border-primary/60 bg-primary/5'
                  : 'border-border hover:border-border/80 hover:bg-muted/40'
              )}
            >
              <span
                className='relative block h-[72px] w-full overflow-hidden rounded-lg border border-black/5'
                style={{ background: swatch.bg }}
              >
                <span
                  className='absolute top-2.5 bottom-2.5 right-2.5 w-[46%] rounded-[3px] border border-white/15'
                  style={{ background: swatch.card }}
                />
                <span
                  className='absolute left-2.5 top-3 h-1 w-8 rounded-full opacity-80'
                  style={{ background: swatch.bar }}
                />
                <span
                  className='absolute left-2.5 top-6 h-1 w-5 rounded-full opacity-40'
                  style={{ background: swatch.bar }}
                />
              </span>

              <span className='mt-2 flex items-center gap-2'>
                <span className='text-sm font-medium'>{meta.name}</span>
                <span className='text-muted-foreground text-xs'>{meta.cn}</span>
                {active && (
                  <span className='bg-primary text-primary-foreground ml-auto grid size-4 place-items-center rounded-full'>
                    <Check className='size-2.5' strokeWidth={3} />
                  </span>
                )}
              </span>
              <span className='text-muted-foreground mt-0.5 block text-xs leading-5'>
                {meta.note}
              </span>
            </button>
          )
        })}
      </div>

      <p className='text-muted-foreground mt-4 text-xs'>
        {t('Preview a skin without saving with')}{' '}
        <code className='bg-muted rounded px-1 py-0.5'>/sign-in?theme=light</code>
      </p>
    </SettingsSection>
  )
}
