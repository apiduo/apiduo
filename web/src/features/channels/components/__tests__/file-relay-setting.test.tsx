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
import { useForm } from 'react-hook-form'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, test } from 'vitest'

import { Form } from '@/components/ui/form'

import {
  CHANNEL_FORM_DEFAULT_VALUES,
  transformFormDataToCreatePayload,
  type ChannelFormValues,
} from '../../lib/channel-form'
import { FileRelaySetting } from '../file-relay-setting'

function SettingsForm(props: {
  channelType: number
  settings?: string
  disabled?: boolean
  onSave: (values: ChannelFormValues) => void
}) {
  const form = useForm<ChannelFormValues>({
    defaultValues: {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      type: props.channelType,
      settings: props.settings ?? '{}',
    },
  })
  return (
    <Form {...form}>
      <form
        onSubmit={form.handleSubmit((values) => props.onSave(values))}
      >
        <FileRelaySetting
          channelType={props.channelType}
          disabled={props.disabled}
        />
        <button type='submit'>Save</button>
      </form>
    </Form>
  )
}

function savedSettings(values: ChannelFormValues): Record<string, unknown> {
  const payload = transformFormDataToCreatePayload(values)
  return JSON.parse(payload.channel.settings ?? '{}')
}

describe('File relay channel setting', () => {
  test.each([1, 8, 25, 26])(
    'channel type %s records the selected purposes in the settings blob',
    async (channelType) => {
      const user = userEvent.setup()
      let saved: ChannelFormValues | null = null
      render(
        <SettingsForm
          channelType={channelType}
          onSave={(values) => {
            saved = values
          }}
        />
      )

      await user.click(
        screen.getByRole('checkbox', { name: 'Document extraction' })
      )
      await user.click(screen.getByRole('checkbox', { name: 'Batch' }))
      await user.click(screen.getByRole('button', { name: 'Save' }))

      expect(savedSettings(saved!)).toMatchObject({
        file_relay: { purposes: ['file-extract', 'batch'] },
      })
    }
  )

  test('an unrelated save keeps an existing file relay block', async () => {
    // This is the invariant that lets the configuration live in the settings
    // blob: buildSettingsJSON starts from the stored JSON, so keys it does not
    // manage survive a save that never mentions them.
    const user = userEvent.setup()
    let saved: ChannelFormValues | null = null
    render(
      <SettingsForm
        channelType={1}
        settings={JSON.stringify({
          file_relay: { purposes: ['batch'], max_upload_bytes: 1048576 },
          some_unknown_key: 'keep-me',
        })}
        onSave={(values) => {
          saved = values
        }}
      />
    )

    await user.click(screen.getByRole('button', { name: 'Save' }))

    expect(savedSettings(saved!)).toMatchObject({
      file_relay: { purposes: ['batch'], max_upload_bytes: 1048576 },
      some_unknown_key: 'keep-me',
    })
  })

  test('unchecking every purpose clears the selection but keeps the block', async () => {
    const user = userEvent.setup()
    let saved: ChannelFormValues | null = null
    render(
      <SettingsForm
        channelType={1}
        settings={JSON.stringify({
          file_relay: { purposes: ['file-extract'] },
        })}
        onSave={(values) => {
          saved = values
        }}
      />
    )

    const checkbox = screen.getByRole('checkbox', {
      name: 'Document extraction',
    })
    expect(checkbox).toBeChecked()
    await user.click(checkbox)
    await user.click(screen.getByRole('button', { name: 'Save' }))

    expect(savedSettings(saved!).file_relay).toEqual({
      purposes: [],
      max_upload_bytes: 0,
    })
  })

  test('megabytes are stored as bytes', async () => {
    const user = userEvent.setup()
    let saved: ChannelFormValues | null = null
    render(
      <SettingsForm
        channelType={25}
        onSave={(values) => {
          saved = values
        }}
      />
    )

    await user.type(screen.getByRole('spinbutton'), '200')
    await user.click(screen.getByRole('button', { name: 'Save' }))

    expect(savedSettings(saved!).file_relay).toMatchObject({
      max_upload_bytes: 200 * 1024 * 1024,
    })
  })

  test('the suggested button fills the provider defaults', async () => {
    const user = userEvent.setup()
    let saved: ChannelFormValues | null = null
    render(
      <SettingsForm
        channelType={25}
        onSave={(values) => {
          saved = values
        }}
      />
    )

    await user.click(screen.getByRole('button', { name: 'Use suggested' }))
    await user.click(screen.getByRole('button', { name: 'Save' }))

    expect(savedSettings(saved!).file_relay).toMatchObject({
      purposes: ['file-extract', 'image', 'video', 'batch'],
    })
  })

  test('unsupported channel types render nothing', () => {
    render(<SettingsForm channelType={14} onSave={() => {}} />)
    expect(screen.queryByRole('checkbox')).not.toBeInTheDocument()
  })
})
