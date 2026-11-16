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
  models?: string
  settings?: string
  disabled?: boolean
  onSave: (values: ChannelFormValues) => void
}) {
  const form = useForm<ChannelFormValues>({
    defaultValues: {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      type: props.channelType,
      models: props.models ?? 'gpt-4o',
      settings: props.settings ?? '{}',
    },
  })
  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit((values) => props.onSave(values))}>
        <FileRelaySetting
          channelType={props.channelType}
          disabled={props.disabled}
        />
        <button type='submit'>Save</button>
      </form>
    </Form>
  )
}

function savedChannel(values: ChannelFormValues) {
  return transformFormDataToCreatePayload(values).channel
}

describe('File relay channel setting', () => {
  test.each([1, 8, 25, 26])(
    'channel type %s writes each purpose into the model list',
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

      expect(savedChannel(saved!).models).toBe(
        'gpt-4o,@files/upload/file-extract,@files/upload/batch'
      )
      // The retired per-channel purpose list must not come back.
      expect(
        JSON.parse(savedChannel(saved!).settings ?? '{}').file_relay?.purposes
      ).toBeUndefined()
    }
  )

  test('a declared purpose is read back from the model list', () => {
    render(
      <SettingsForm
        channelType={1}
        models='gpt-4o,@files/upload/file-extract'
        onSave={() => {}}
      />
    )
    expect(
      screen.getByRole('checkbox', { name: 'Document extraction' })
    ).toBeChecked()
    expect(screen.getByRole('checkbox', { name: 'Batch' })).not.toBeChecked()
  })

  test('unchecking a purpose only removes its name', async () => {
    const user = userEvent.setup()
    let saved: ChannelFormValues | null = null
    render(
      <SettingsForm
        channelType={1}
        models='gpt-4o,@files/upload/file-extract,@files/upload/batch'
        onSave={(values) => {
          saved = values
        }}
      />
    )

    await user.click(
      screen.getByRole('checkbox', { name: 'Document extraction' })
    )
    await user.click(screen.getByRole('button', { name: 'Save' }))

    expect(savedChannel(saved!).models).toBe('gpt-4o,@files/upload/batch')
  })

  test('an unrelated save keeps an existing file relay block', async () => {
    // This is the invariant that lets the ceiling live in the settings blob:
    // buildSettingsJSON starts from the stored JSON, so keys it does not manage
    // survive a save that never mentions them.
    const user = userEvent.setup()
    let saved: ChannelFormValues | null = null
    render(
      <SettingsForm
        channelType={1}
        settings={JSON.stringify({
          file_relay: { max_upload_bytes: 1048576 },
          some_unknown_key: 'keep-me',
        })}
        onSave={(values) => {
          saved = values
        }}
      />
    )

    await user.click(screen.getByRole('button', { name: 'Save' }))

    expect(JSON.parse(savedChannel(saved!).settings ?? '{}')).toMatchObject({
      file_relay: { max_upload_bytes: 1048576 },
      some_unknown_key: 'keep-me',
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

    expect(JSON.parse(savedChannel(saved!).settings ?? '{}')).toMatchObject({
      file_relay: { max_upload_bytes: 200 * 1024 * 1024 },
    })
  })

  test('the suggested button adds the provider defaults', async () => {
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

    expect(savedChannel(saved!).models).toBe(
      'gpt-4o,@files/upload/file-extract,@files/upload/image,@files/upload/video,@files/upload/batch'
    )
  })

  test('unsupported channel types render nothing', () => {
    render(<SettingsForm channelType={14} onSave={() => {}} />)
    expect(screen.queryByRole('checkbox')).not.toBeInTheDocument()
  })
})
