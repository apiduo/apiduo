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
import { render, screen } from '@testing-library/react'
import { useForm } from 'react-hook-form'
import { describe, expect, test } from 'vitest'

import { Form } from '@/components/ui/form'

import { CHANNEL_FORM_DEFAULT_VALUES, type ChannelFormValues } from '../../../lib/channel-form'
import { RemapHardeningField } from '../remap-hardening-field'

function Harness(props: {
  modelMapping: string
  checked?: boolean
  disabled?: boolean
}) {
  const form = useForm<ChannelFormValues>({
    defaultValues: {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      model_mapping: props.modelMapping,
      remap_hardening: props.checked,
    },
  })
  return (
    <Form {...form}>
      <RemapHardeningField
        control={form.control}
        disabled={props.disabled === true}
        modelMapping={props.modelMapping}
      />
    </Form>
  )
}

describe('RemapHardeningField', () => {
  test('is usable only after a model mapping exists', () => {
    render(<Harness modelMapping='' />)
    // Base UI marks the state with data-* attributes rather than disabled.
    expect(screen.getByRole('switch')).toHaveAttribute('data-disabled')
  })

  test('unlocks with a mapping and reflects the saved value', () => {
    render(
      <Harness modelMapping='{"acme-pro":"deepseek-v3"}' checked={true} />
    )
    const control = screen.getByRole('switch')
    expect(control).not.toHaveAttribute('data-disabled')
    expect(control).toHaveAttribute('data-checked')
  })

  test('an empty mapping object does not count as configured', () => {
    render(<Harness modelMapping='{}' />)
    expect(screen.getByRole('switch')).toHaveAttribute('data-disabled')
  })

  test('stays disabled while the drawer is locked', () => {
    render(
      <Harness
        modelMapping='{"acme-pro":"deepseek-v3"}'
        disabled={true}
      />
    )
    expect(screen.getByRole('switch')).toHaveAttribute('data-disabled')
  })
})
