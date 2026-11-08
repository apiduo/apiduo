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
import i18next from 'i18next'
import { beforeAll, describe, expect, test, vi } from 'vitest'

import type { MessageBilling } from '../../hooks/use-billing-ledger'
import { BillPanel, MoneyChip } from '../message-billing'

vi.mock('@/lib/currency', () => ({
  getCurrencyDisplay: () => ({
    config: { quotaPerUnit: 500000 },
    meta: {
      kind: 'currency',
      symbol: '$',
      currencyCode: 'USD',
      exchangeRate: 1,
    },
  }),
  formatCurrencyFromUSD: (usd: number) => `$${usd.toFixed(2)}`,
  formatQuotaWithCurrency: (quota: number) => `Q${quota}`,
}))

beforeAll(async () => {
  await i18next.init({
    lng: 'en',
    fallbackLng: 'en',
    resources: { en: { translation: {} } },
    interpolation: { escapeValue: false },
  })
})

function billing(overrides: Partial<MessageBilling> = {}): MessageBilling {
  return {
    status: 'settled',
    quota: 2724,
    promptTokens: 1180,
    completionTokens: 386,
    cacheTokens: 742,
    breakdown: {
      quota: 2724,
      usd: 0.005448,
      effectiveGroupRatio: 0.8,
      discountRate: 0.8,
      baseGroupRatio: 1,
      modelRatio: 1.25,
      completionRatio: 4,
      cacheRatio: 0.5,
      baseTokens: 438,
      cacheTokens: 742,
      formula: '(438 + 742 × 0.50 + 386 × 4.00) × 1.25 × 0.80 = 2,724 额度',
    },
    requestId: 'req_abc',
    channelId: 12,
    channelName: 'OpenAI',
    firstTokenMs: 420,
    durationMs: 2810,
    source: 'usage',
    stopped: false,
    ...overrides,
  }
}

describe('MoneyChip', () => {
  test('已对账时显示金额，不显示额外标注', () => {
    render(
      <MoneyChip
        billing={billing()}
        expanded={false}
        onToggle={() => undefined}
      />
    )
    expect(screen.getByText('Q2724')).toBeTruthy()
    expect(screen.queryByText(/est\./)).toBeNull()
  })

  test('对账中显示进行态而不是金额', () => {
    render(
      <MoneyChip
        billing={billing({ status: 'pending' })}
        expanded={false}
        onToggle={() => undefined}
      />
    )
    expect(screen.getByText('Reconciling…')).toBeTruthy()
  })

  test('估算态带 est. 标记，不冒充实际扣费', () => {
    render(
      <MoneyChip
        billing={billing({ status: 'estimated', source: 'estimate' })}
        expanded={false}
        onToggle={() => undefined}
      />
    )
    expect(screen.getByText('Q2724 est.')).toBeTruthy()
  })

  test('aria-expanded 跟随外部展开状态，点击触发切换', async () => {
    const onToggle = vi.fn()
    render(
      <MoneyChip
        billing={billing()}
        expanded={false}
        onToggle={onToggle}
      />
    )
    const chip = screen.getByRole('button')
    expect(chip.getAttribute('aria-expanded')).toBe('false')
    await userEvent.click(chip)
    expect(onToggle).toHaveBeenCalledTimes(1)
  })
})

describe('BillPanel', () => {
  test('命中个人折扣时，组倍率被划掉并单列折扣', () => {
    render(
      <BillPanel
        billing={billing()}
        modelName='gpt-4o'
        onCopyCurl={() => undefined}
        onOpenRequest={() => undefined}
      />
    )

    expect(screen.getByText('Personal discount')).toBeTruthy()
    expect(screen.getByText('×0.80')).toBeTruthy()
    // 原始组倍率以删除线展示，避免用户按乘法复算发现对不上
    const struck = screen.getByText('×1.00')
    expect(struck.className).toContain('line-through')
  })

  test('未命中折扣时只显示组倍率', () => {
    const noDiscount = billing()
    const baseBreakdown = noDiscount.breakdown
    expect(baseBreakdown).not.toBeNull()
    noDiscount.breakdown = baseBreakdown
      ? { ...baseBreakdown, discountRate: null, effectiveGroupRatio: 1 }
      : null
    render(
      <BillPanel
        billing={noDiscount}
        modelName='gpt-4o'
        onCopyCurl={() => undefined}
        onOpenRequest={() => undefined}
      />
    )

    expect(screen.queryByText('Personal discount')).toBeNull()
    expect(screen.getByText('×1.00')).toBeTruthy()
  })

  test('已对账显示实际扣费，估算显示预计扣费', () => {
    const { unmount } = render(
      <BillPanel
        billing={billing()}
        modelName='gpt-4o'
        onCopyCurl={() => undefined}
        onOpenRequest={() => undefined}
      />
    )
    expect(screen.getByText('Actual charge')).toBeTruthy()
    expect(screen.getByText('Settled')).toBeTruthy()
    unmount()

    render(
      <BillPanel
        billing={billing({ status: 'estimated', source: 'estimate' })}
        modelName='gpt-4o'
        onCopyCurl={() => undefined}
        onOpenRequest={() => undefined}
      />
    )
    expect(screen.getByText('Estimated charge')).toBeTruthy()
    expect(screen.getByText('Estimate · log not written yet')).toBeTruthy()
  })

  test('公式与缓存命中都可见，方便逐项复算', () => {
    render(
      <BillPanel
        billing={billing()}
        modelName='gpt-4o'
        onCopyCurl={() => undefined}
        onOpenRequest={() => undefined}
      />
    )
    expect(screen.getByText(/2,724 额度/)).toBeTruthy()
    expect(screen.getByText('Cache hit')).toBeTruthy()
  })
})
