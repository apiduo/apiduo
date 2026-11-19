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

import { channelSchema, type AdvancedAsyncBillingFactor } from '../../types'
import {
  applyAdvancedAsyncProtocol,
  createAdvancedAsyncConfig,
  parseAdvancedAsyncConfig,
  stringifyAdvancedAsyncConfig,
} from '../advanced-async'
import {
  transformChannelToFormDefaults,
  transformFormDataToUpdatePayload,
} from '../channel-form'

const resolutionFactor: AdvancedAsyncBillingFactor = {
  key: 'resolution',
  path: ['resolution'],
  values: { '720p': 1, '1080p': 3.2 },
  default: 1,
}

function configWithFactor() {
  const config = createAdvancedAsyncConfig()
  config.billing = { ...config.billing, factors: [resolutionFactor] }
  return config
}

describe('advanced async billing factors', () => {
  // The channel form runs the config through the normalizer on load and again
  // on save, so one round trip is not enough to prove the table survives.
  test('a load/save round trip keeps the factor table', () => {
    const loaded = parseAdvancedAsyncConfig(
      stringifyAdvancedAsyncConfig(configWithFactor())
    )
    if (!loaded) throw new Error('the config must survive a load')

    const saved = parseAdvancedAsyncConfig(stringifyAdvancedAsyncConfig(loaded))

    expect(saved?.billing?.factors).toEqual([resolutionFactor])
  })

  // The panel reaches the normalizer twice through the form: once when the
  // channel loads and once when the update payload is serialized. This pins
  // the real save path, not just the normalizer in isolation.
  test('loading a channel and saving its settings keeps the factors', () => {
    const channel = channelSchema.parse({
      id: 1,
      type: 8001,
      key: 'sk-test',
      status: 1,
      name: 'async',
      created_time: 0,
      test_time: 0,
      response_time: 0,
      balance_updated_time: 0,
      settings: JSON.stringify({ advanced_async: configWithFactor() }),
    })

    const form = transformChannelToFormDefaults(channel)
    const payload = transformFormDataToUpdatePayload(form, channel.id)
    const saved = JSON.parse(payload.settings || '{}')

    expect(saved.advanced_async.billing.factors).toEqual([resolutionFactor])
  })

  test('switching the protocol keeps the factors', () => {
    const switched = applyAdvancedAsyncProtocol(configWithFactor(), 'ark')

    expect(switched.protocol).toBe('ark')
    expect(switched.billing?.mode).toBe('per_second')
    expect(switched.billing?.factors).toEqual([resolutionFactor])
  })

  test('a channel without factors gains no factors key', () => {
    const saved = JSON.parse(
      stringifyAdvancedAsyncConfig(createAdvancedAsyncConfig())
    )

    expect(saved.billing).not.toHaveProperty('factors')
  })

  test('an empty table is not written back', () => {
    const config = createAdvancedAsyncConfig()
    config.billing = { ...config.billing, factors: [] }
    const saved = JSON.parse(stringifyAdvancedAsyncConfig(config))

    expect(saved.billing).not.toHaveProperty('factors')
  })
})
