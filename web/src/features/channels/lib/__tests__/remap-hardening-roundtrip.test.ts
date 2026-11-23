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
import { describe, expect, test } from 'vitest'

import type { Channel } from '../../types'
import {
  CHANNEL_FORM_DEFAULT_VALUES,
  transformChannelToFormDefaults,
  transformFormDataToUpdatePayload,
} from '../channel-form'

function channelWith(settings: string): Channel {
  return {
    id: 251,
    type: 1,
    name: 'mock-remap',
    base_url: 'http://mock-upstream:18080',
    key: '',
    models: 'acme-pro',
    group: 'default',
    setting: '',
    settings,
    channel_info: {
      is_multi_key: false,
      multi_key_size: 0,
      multi_key_mode: 'single',
      min_active_keys: 0,
    },
  } as unknown as Channel
}

function settingsOf(form: typeof CHANNEL_FORM_DEFAULT_VALUES) {
  const payload = transformFormDataToUpdatePayload(form, 251)
  return JSON.parse(payload.settings as string) as Record<string, unknown>
}

describe('remap_hardening form round trip', () => {
  test('loads the saved flag from channel settings', () => {
    const form = transformChannelToFormDefaults(
      channelWith('{"remap_hardening":true}')
    )
    expect(form.remap_hardening).toBe(true)
  })

  test('defaults to off for a channel that never set it', () => {
    const form = transformChannelToFormDefaults(channelWith('{}'))
    expect(form.remap_hardening).toBe(false)
    expect(CHANNEL_FORM_DEFAULT_VALUES.remap_hardening).toBe(false)
  })

  test('saving keeps the flag', () => {
    const form = {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'mock-remap',
      models: 'acme-pro',
      remap_hardening: true,
    }
    expect(settingsOf(form).remap_hardening).toBe(true)
  })

  test('saving an off flag writes nothing', () => {
    const form = {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'mock-remap',
      models: 'acme-pro',
      remap_hardening: false,
    }
    expect(settingsOf(form)).not.toHaveProperty('remap_hardening')
  })

  test('does not disturb neighbouring settings', () => {
    const form = transformChannelToFormDefaults(
      channelWith(
        '{"remap_hardening":true,"pass_through_body_enabled":true,"local_worker":{"concurrency":2}}'
      )
    )
    const settings = settingsOf(form)
    expect(settings.remap_hardening).toBe(true)
    expect(settings.pass_through_body_enabled).toBe(true)
    expect(settings.local_worker).toEqual({ concurrency: 2 })
  })
})
