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
import userEvent from '@testing-library/user-event'
import { useForm } from 'react-hook-form'
import { describe, expect, test } from 'vitest'

import { Form } from '@/components/ui/form'
import type { ChannelFormValues } from '../../lib'
import {
  ADVANCED_ASYNC_DEFAULT_INCOMING_PATH,
  createAdvancedAsyncConfig,
} from '../../lib/advanced-async'
import { AdvancedAsyncPanel } from '../advanced-async-panel'

/**
 * 这个面板是从 drawer 整块搬出来的。它决定运营者看到的路由摘要与入口清单，
 * 搬动时最容易出的事是「条件还在、内容不渲染」—— 用文案与路径断言守住这一点。
 */
function Harness({ value, locked }: { value: string; locked: boolean }) {
  const form = useForm<ChannelFormValues>({
    defaultValues: { advanced_async: value },
  })
  return (
    <Form {...form}>
      <form>
        <AdvancedAsyncPanel form={form} locked={locked} />
      </form>
    </Form>
  )
}

describe('AdvancedAsyncPanel', () => {
  test('an empty config reads as not configured rather than as a blank list', () => {
    const { container } = render(<Harness value='' locked={false} />)
    expect(container.textContent).toContain('Not configured')
  })

  test('a configured channel shows its incoming paths', () => {
    const value = JSON.stringify(createAdvancedAsyncConfig())
    const { container } = render(<Harness value={value} locked={false} />)
    expect(container.textContent).toContain('Configured')
    expect(container.textContent).toContain(ADVANCED_ASYNC_DEFAULT_INCOMING_PATH)
  })

  test('a locked sensitive field never opens the route editor', async () => {
    const user = userEvent.setup()
    render(<Harness value='' locked />)
    await user.click(
      screen.getByRole('button', { name: /Configure async routes/ })
    )
    expect(screen.queryByRole('dialog')).toBeNull()
  })
})
