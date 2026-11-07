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
import { render } from '@testing-library/react'
import { useForm } from 'react-hook-form'
import { describe, expect, test } from 'vitest'

import { Form, FormField } from '@/components/ui/form'
import { MULTI_KEY_STATUS } from '../../constants'
import { MinActiveKeysField } from '../min-active-keys-field'

/**
 * The hint is the only place the console shows how wide the router actually is.
 * It is derived from the same inputs the backend uses, so a mismatch here is a
 * lie told to the operator, not a cosmetic bug.
 */
function Harness({
  keyText,
  limit,
  statusList,
}: {
  keyText: string
  limit: number
  statusList?: Record<string, number>
}) {
  const form = useForm({ defaultValues: { key: keyText, min_active_keys: limit } })
  return (
    <Form {...form}>
      <form>
        <FormField
          control={form.control}
          name='min_active_keys'
          render={({ field }) => (
            <MinActiveKeysField field={field} statusList={statusList} />
          )}
        />
      </form>
    </Form>
  )
}

describe('MinActiveKeysField', () => {
  test('reports how many of the available keys are in rotation', () => {
    const { container } = render(
      <Harness keyText={'a\nb\nc'} limit={2} />
    )
    expect(container.textContent).toContain(
      '2 of 3 available keys are in rotation now.'
    )
  })

  test('a cap above the key count reports the key count, not the cap', () => {
    const { container } = render(<Harness keyText={'a\nb'} limit={10} />)
    expect(container.textContent).toContain(
      '2 of 2 available keys are in rotation now.'
    )
  })

  test('stays quiet when no cap is set', () => {
    const { container } = render(<Harness keyText={'a\nb\nc'} limit={0} />)
    expect(container.textContent).not.toContain('in rotation now')
  })

  test('excludes disabled keys from the available count', () => {
    const { container } = render(
      <Harness
        keyText={'a\nb\nc'}
        limit={2}
        statusList={{ '1': MULTI_KEY_STATUS.AUTO_DISABLED }}
      />
    )
    expect(container.textContent).toContain(
      '2 of 2 available keys are in rotation now.'
    )
  })

  test('says nothing about rotation when the field is empty', () => {
    const { container } = render(<Harness keyText={''} limit={3} />)
    expect(container.textContent).not.toContain('in rotation now')
  })
})
