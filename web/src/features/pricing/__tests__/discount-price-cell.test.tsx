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
import { describe, expect, it } from 'vitest'

import { ModelPriceCell } from '../components/model-price-cell'
import type { PricingModel } from '../types'

const baseModel = {
  id: 1,
  model_name: 'gpt-4o',
  quota_type: 0,
  model_ratio: 1,
  completion_ratio: 4,
  enable_groups: ['vip', 'default'],
  group_ratio: { vip: 0.9, default: 1 },
} as PricingModel

const discountedModel = {
  ...baseModel,
  user_discount_ratios: { vip: { 'gpt-4o': 0.7 } },
} as PricingModel

describe('ModelPriceCell 折扣展示', () => {
  it('选中带折扣的分组时渲染划线原价与折扣标', () => {
    const { container } = render(
      <ModelPriceCell
        model={discountedModel}
        options={{ selectedGroup: 'vip', tokenUnit: 'M' }}
      />
    )

    const strikes = container.querySelectorAll('s')
    // 输入价与输出价各一条划线
    expect(strikes).toHaveLength(2)
    for (const strike of strikes) {
      expect(strike.textContent?.trim()).not.toBe('')
    }

    // 0.7 / 0.9 相对划线价降 22%
    const badge = screen.getAllByText(/^-\d+%$/)
    expect(badge).toHaveLength(2)
    expect(badge[0].textContent).toBe('-22%')
  })

  it('无折扣时不渲染任何划线或折扣标', () => {
    const { container } = render(
      <ModelPriceCell
        model={baseModel}
        options={{ selectedGroup: 'vip', tokenUnit: 'M' }}
      />
    )
    expect(container.querySelectorAll('s')).toHaveLength(0)
    expect(screen.queryByText(/^-\d+%$/)).toBeNull()
  })

  it('未选中具体分组时不渲染折扣元素', () => {
    const { container } = render(
      <ModelPriceCell model={discountedModel} options={{ tokenUnit: 'M' }} />
    )
    expect(container.querySelectorAll('s')).toHaveLength(0)
    expect(screen.queryByText(/^-\d+%$/)).toBeNull()
  })
})
