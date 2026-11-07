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
import type { Ref } from 'react'
import { useTranslation } from 'react-i18next'

import { useFormContext, useWatch } from 'react-hook-form'

import { apiduoBlockMark } from '@/features/apiduo-marks/marks'
import { countAvailableKeys, countServingKeys } from '../lib/multi-key-active'
import {
  FormControl,
  FormDescription,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'

type FieldLike = {
  value?: number | string
  onChange: (value: number) => void
  onBlur: () => void
  name: string
  ref?: Ref<HTMLInputElement>
}

/**
 * Channel-level setting: how many keys stay in rotation at once.
 *
 * Lives in its own file so the drawer keeps a single call site for the whole
 * min-active-keys feature — one block to resolve when this is rebased onto
 * upstream.
 */
export function MinActiveKeysField({
  field,
  disabled,
  statusList,
}: {
  field: FieldLike
  disabled?: boolean
  /** Per-key status of the channel being edited; absent for a new channel. */
  statusList?: Record<string, number>
}) {
  const { t } = useTranslation()
  const { control } = useFormContext()
  const keyText = useWatch({ control, name: 'key' }) as string | undefined

  // Derived, never stored: the backend has no standby state, it simply serves the
  // first N available keys. Showing the same arithmetic here keeps the field from
  // claiming a width the router will not honour.
  const available = countAvailableKeys(keyText, statusList)
  const limit = Number(field.value ?? 0)
  const serving = countServingKeys(available, limit)

  return (
    <FormItem>
      <FormLabel>{t('Min active keys')}</FormLabel>
      <FormControl>
        <Input
          type='number'
          min={0}
          inputMode='numeric'
          disabled={disabled}
          name={field.name}
          ref={field.ref}
          value={field.value ?? 0}
          onChange={(event) => {
            const next = Number.parseInt(event.target.value, 10)
            field.onChange(Number.isNaN(next) || next < 0 ? 0 : next)
          }}
          onBlur={field.onBlur}
        />
      </FormControl>
      <FormDescription data-apiduo={apiduoBlockMark('channel:min-active-keys')}>
        <span className='apiduo-text'>
          {t(
            'Serve at most this many keys at once, in list order. The rest stay enabled but idle, and take a slot on their own when an active key is disabled. 0 serves every key.'
          )}
        </span>
        {limit > 0 && available > 0 && (
          <span className='text-muted-foreground mt-1 block'>
            {t('{{serving}} of {{available}} available keys are in rotation now.', {
              serving,
              available,
            })}
          </span>
        )}
      </FormDescription>
      <FormMessage />
    </FormItem>
  )
}
