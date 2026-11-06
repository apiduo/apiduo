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
import { useFormContext } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Checkbox } from '@/components/ui/checkbox'
import {
  FormControl,
  FormDescription,
  FormItem,
  FormLabel,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'

import type { ChannelFormValues } from '../lib/channel-form'
import {
  FILE_RELAY_PURPOSES,
  defaultFileRelayPurposes,
  readFileRelayConfig,
  supportsFileRelay,
  type FileRelayConfig,
} from '../lib/file-relay'

const MEGABYTE = 1024 * 1024

const PURPOSE_LABELS: Record<string, string> = {
  'file-extract': 'Document extraction',
  image: 'Image',
  video: 'Video',
  batch: 'Batch',
  vision: 'Vision',
  assistants: 'Assistants',
  'fine-tune': 'Fine-tune',
}

function parseSettingsObject(raw: unknown): Record<string, unknown> {
  if (typeof raw !== 'string' || !raw.trim()) return {}
  try {
    const parsed: unknown = JSON.parse(raw)
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
      return parsed as Record<string, unknown>
    }
  } catch {
    // An unreadable blob is treated as empty; the rest of the form will
    // overwrite it on save anyway.
  }
  return {}
}

/**
 * Declares which /v1/files purposes this channel serves.
 *
 * The value is written into the channel's settings JSON under file_relay, so it
 * rides along with the normal channel save instead of needing its own endpoint.
 * buildSettingsJSON starts from the existing JSON and only touches the keys it
 * knows, which is why this field survives a save that does not mention it.
 */
export function FileRelaySetting(props: {
  channelType: number
  disabled?: boolean
}) {
  const { t } = useTranslation()
  const form = useFormContext<ChannelFormValues>()
  const settings = parseSettingsObject(form.watch('settings'))
  const config = readFileRelayConfig(settings)

  if (!supportsFileRelay(props.channelType)) return null

  const write = (next: FileRelayConfig) => {
    form.setValue('settings', JSON.stringify({ ...settings, file_relay: next }), {
      shouldDirty: true,
      shouldTouch: true,
    })
  }

  const suggested = defaultFileRelayPurposes(props.channelType)

  return (
    <div className='space-y-5'>
      <FormItem>
        <div className='flex items-start justify-between gap-4'>
          <div className='space-y-0.5'>
            <FormLabel>{t('File relay purposes')}</FormLabel>
            <FormDescription>
              {t(
                'Only the selected purposes are forwarded to this channel. Leaving every box clear keeps it out of /v1/files traffic.'
              )}
            </FormDescription>
          </div>
          <button
            type='button'
            className='text-muted-foreground hover:text-foreground shrink-0 text-xs underline underline-offset-4'
            disabled={props.disabled}
            onClick={() => write({ ...config, purposes: [...suggested] })}
          >
            {t('Use suggested')}
          </button>
        </div>
        <div className='grid grid-cols-2 gap-2 pt-3 sm:grid-cols-3'>
          {FILE_RELAY_PURPOSES.map((purpose) => (
            <label key={purpose} className='flex items-center gap-2 text-sm'>
              <Checkbox
                checked={config.purposes.includes(purpose)}
                disabled={props.disabled}
                onCheckedChange={(checked) => {
                  write({
                    ...config,
                    purposes:
                      checked === true
                        ? [...config.purposes, purpose]
                        : config.purposes.filter((item) => item !== purpose),
                  })
                }}
              />
              <span>{t(PURPOSE_LABELS[purpose] ?? purpose)}</span>
            </label>
          ))}
        </div>
      </FormItem>

      <FormItem className='flex items-center justify-between gap-4'>
        <div className='space-y-0.5'>
          <FormLabel>{t('Max upload size (MB)')}</FormLabel>
          <FormDescription>
            {t(
              'Per file, for this channel. Leave empty to use the provider default.'
            )}
          </FormDescription>
        </div>
        <FormControl>
          <Input
            type='number'
            min={0}
            inputMode='decimal'
            className='w-28'
            disabled={props.disabled}
            value={
              config.max_upload_bytes > 0
                ? String(
                    Math.round((config.max_upload_bytes / MEGABYTE) * 100) / 100
                  )
                : ''
            }
            onChange={(event) => {
              const megabytes = Number(event.target.value)
              write({
                ...config,
                max_upload_bytes:
                  Number.isFinite(megabytes) && megabytes > 0
                    ? Math.round(megabytes * MEGABYTE)
                    : 0,
              })
            }}
          />
        </FormControl>
      </FormItem>
    </div>
  )
}
