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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createInstance, type i18n } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import en from '@/i18n/locales/en.json'
import { api } from '@/lib/api'

import { getSystemOptions } from '@/features/system-settings/api'
import { updateSystemOptionsBatch } from '@/features/system-settings/apiduo-option-batch'

import { ObservabilitySection } from '../observability-section'

vi.mock('@/features/system-settings/api', () => ({
  getSystemOptions: vi.fn(),
}))

// The batch helper lives in its own file so the upstream api.ts stays clean.
vi.mock('@/features/system-settings/apiduo-option-batch', () => ({
  updateSystemOptionsBatch: vi.fn(),
}))

const OPTION_KEY = 'observability_setting'

const traceConfig = {
  enabled: true,
  host: 'http://sink:3411',
  body_mode: 'headtail',
  body_head_bytes: 32768,
  body_tail_bytes: 16384,
  body_full_bytes: 2097152,
  error_sample_rate: 100,
  redact: true,
  environment: 'production',
  metadata_sample_rate: 10,
  content_sample_rate: 100,
  server_zone: 'Asia/Shanghai (UTC+08:00)',
  server_time: '2026-11-10T05:12:00+08:00',
  parent_based_sampling: true,
}

/** 选项系统按「模块.字段」一行一个键存，不是一份 JSON blob。 */
function options(overrides: Record<string, string> = {}) {
  const base: Record<string, string> = {
    [`${OPTION_KEY}.metadata_sample_rate`]: '10',
    [`${OPTION_KEY}.content_sample_rate`]: '100',
    [`${OPTION_KEY}.parent_based_sampling`]: 'true',
    [`${OPTION_KEY}.timezone`]: 'Asia/Shanghai',
    [`${OPTION_KEY}.rules`]: '[]',
    [`${OPTION_KEY}.trace_viewer_user_ids`]: '[]',
    [`${OPTION_KEY}.body_mode`]: 'headtail',
    [`${OPTION_KEY}.body_head_bytes`]: '32768',
    [`${OPTION_KEY}.body_tail_bytes`]: '16384',
    [`${OPTION_KEY}.body_full_bytes`]: '2097152',
    [`${OPTION_KEY}.error_sample_rate`]: '100',
    [`${OPTION_KEY}.redact`]: 'true',
    [`${OPTION_KEY}.environment`]: 'production',
    ...overrides,
  }
  return {
    success: true,
    data: Object.entries(base).map(([key, value]) => ({ key, value })),
  }
}

let testI18n: i18n

function renderSection() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <I18nextProvider i18n={testI18n}>
      <QueryClientProvider client={client}>
        <ObservabilitySection />
      </QueryClientProvider>
    </I18nextProvider>
  )
}

beforeEach(async () => {
  vi.mocked(getSystemOptions).mockResolvedValue(options() as never)
  vi.mocked(updateSystemOptionsBatch).mockResolvedValue({
    success: true,
    data: { count: 6 },
  } as never)
  vi.spyOn(api, 'get').mockResolvedValue({ data: { success: true, data: traceConfig } })

  testI18n = createInstance()
  await testI18n.init({
    lng: 'en',
    fallbackLng: 'en',
    resources: { en: { translation: en } },
    interpolation: { escapeValue: false },
  })
})

describe('ObservabilitySection', () => {
  it('shows what is actually running, not what the form would like to run', async () => {
    renderSection()

    // 关键不变量：状态条读的是运行时（trace config），不是表单草稿。
    await waitFor(() => {
      expect(screen.getByText('10% links · 100% content')).toBeTruthy()
    })
    // 运行值标签带实际窗口：只显示「Head + tail」看不出保留了多少。
    expect(screen.getByText('Head + tail · 32/16 KB')).toBeTruthy()
    // 时区既出现在状态条也出现在输入框，取「至少有一处」即可。
    expect(
      screen.getAllByText(/Asia\/Shanghai \(UTC\+08:00\)/).length
    ).toBeGreaterThan(0)
    expect(screen.getByText(/· 05:12/)).toBeTruthy()
    expect(
      (screen.getByLabelText('Metadata sampling (%)') as HTMLInputElement).value
    ).toBe('10')
  })

  it('applies a preset to the form without saving anything', async () => {
    const user = userEvent.setup()
    renderSection()

    await waitFor(() => {
      expect(screen.getByText('Full capture')).toBeTruthy()
    })
    await user.click(screen.getByRole('button', { name: 'Full capture' }))

    expect(
      (screen.getByLabelText('Metadata sampling (%)') as HTMLInputElement).value
    ).toBe('100')
    expect(
      (screen.getByLabelText('Content sampling (%)') as HTMLInputElement).value
    ).toBe('100')
    // 预设只填表；保存必须是操作员的显式动作。
    expect(updateSystemOptionsBatch).not.toHaveBeenCalled()
  })

  it('saves the whole form in one batch, rules included', async () => {
    const user = userEvent.setup()
    renderSection()

    // 必须等选项真的灌进表单再动手：草稿一旦从 EMPTY_DRAFT 起家，
    // 后面每次 update 都会把「时区」等未改字段覆盖成空值。
    await waitFor(() => {
      expect(
        (screen.getByLabelText('Metadata sampling (%)') as HTMLInputElement)
          .value
      ).toBe('10')
    })
    const metadata = screen.getByLabelText('Metadata sampling (%)')
    await user.clear(metadata)
    await user.type(metadata, '25')

    await user.click(screen.getByRole('button', { name: 'Add rule' }))
    const models = await screen.findByLabelText('Models (comma separated)')
    await user.type(models, 'seedance, seedream')
    const ruleMetadata = screen.getByLabelText('Rule metadata sampling (%)')
    await user.clear(ruleMetadata)
    await user.type(ruleMetadata, '50')

    await user.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => {
      expect(updateSystemOptionsBatch).toHaveBeenCalledTimes(1)
    })
    const payload = vi.mocked(updateSystemOptionsBatch).mock
      .calls[0][0] as Record<string, string>
    expect(payload[`${OPTION_KEY}.metadata_sample_rate`]).toBe('25')
    expect(payload[`${OPTION_KEY}.content_sample_rate`]).toBe('100')
    const rules = JSON.parse(payload[`${OPTION_KEY}.rules`])
    expect(rules).toHaveLength(1)
    expect(rules[0].models).toEqual(['seedance', 'seedream'])
    expect(rules[0].metadata_rate).toBe(50)
    expect(rules[0].exclude).toBe(false)
    // 一次保存 = 一次请求：分键写会把「新比率 + 旧规则」这种半套状态落库。
    expect(payload[`${OPTION_KEY}.timezone`]).toBe('Asia/Shanghai')
    // 采集窗口与质量参数同批写：body 模式单独落库而窗口没落，会得到
    // 「headtail 但窗口是上一版」的半套状态。
    expect(payload[`${OPTION_KEY}.body_mode`]).toBe('headtail')
    expect(payload[`${OPTION_KEY}.body_head_bytes`]).toBe('32768')
    expect(payload[`${OPTION_KEY}.body_tail_bytes`]).toBe('16384')
    // 上限与模式同批：只落 body_mode 而漏了上限，会得到「full 但用的是上一版
    // 的 2 MB」这种半套状态，而排查现场时那正是要看的东西。
    expect(payload[`${OPTION_KEY}.body_full_bytes`]).toBe('2097152')
    expect(payload[`${OPTION_KEY}.error_sample_rate`]).toBe('100')
    expect(payload[`${OPTION_KEY}.redact`]).toBe('true')
    expect(payload[`${OPTION_KEY}.environment`]).toBe('production')
  })

  it('refuses a rule with only one end of the time window', async () => {
    const user = userEvent.setup()
    renderSection()

    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Add rule' })).toBeTruthy()
    })
    await user.click(screen.getByRole('button', { name: 'Add rule' }))

    const start = await screen.findByLabelText('Window start')
    await user.type(start, '09:00')

    await user.click(screen.getByRole('button', { name: 'Save' }))

    // 后端遇到半个窗口会把两端都丢掉、规则变成全天匹配 —— 那会让「已保存」
    // 与「实际行为」不一致，所以这里直接拒绝，而不是替用户改写。
    expect(updateSystemOptionsBatch).not.toHaveBeenCalled()
  })

  it('keeps head and tail inside one budget while typing', async () => {
    const user = userEvent.setup()
    renderSection()

    const head = await screen.findByLabelText('Head window (KB)')
    await user.clear(head)
    await user.type(head, '128')

    // 两个 128 相加会被后端整批拒绝；表单在输入时就压尾，
    // 让屏幕上看到的数字等于能存下的数字。
    expect((head as HTMLInputElement).value).toBe('128')
    expect(
      (screen.getByLabelText('Tail window (KB)') as HTMLInputElement).value
    ).toBe('0')
  })

  it('warns about the full-payload cost with the number on the form', async () => {
    const user = userEvent.setup()
    renderSection()

    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Full payload' })).toBeTruthy()
    })
    await user.click(screen.getByRole('button', { name: 'Full payload' }))

    // 提示里的两个数字都来自表单，不是写死的常量：早先那句「2 MB … 768 MB」
    // 既错了约 5 倍，也会在这一栏变成可编辑之后立刻过期。
    expect(screen.getByText(/up to 2 MB per request/)).toBeTruthy()
    expect(
      screen.getByText(/3\.9 GB of capture buffers at 1000 concurrent streams/)
    ).toBeTruthy()

    const cap = screen.getByLabelText('Single-request cap (MB)')
    await user.clear(cap)
    await user.type(cap, '8')
    expect((cap as HTMLInputElement).value).toBe('8')
    expect(screen.getByText(/up to 8 MB per request/)).toBeTruthy()
    expect(
      screen.getByText(/15\.6 GB of capture buffers at 1000 concurrent streams/)
    ).toBeTruthy()

    // 只是填表，没保存。
    expect(updateSystemOptionsBatch).not.toHaveBeenCalled()
  })

  it('never saves a sub-megabyte cap the server would refuse', async () => {
    const user = userEvent.setup()
    renderSection()

    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Full payload' })).toBeTruthy()
    })
    await user.click(screen.getByRole('button', { name: 'Full payload' }))

    const cap = screen.getByLabelText('Single-request cap (MB)')
    await user.clear(cap)

    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => {
      expect(updateSystemOptionsBatch).toHaveBeenCalledTimes(1)
    })
    const payload = vi.mocked(updateSystemOptionsBatch).mock
      .calls[0][0] as Record<string, string>
    // 清空是输入过程中的中间态；落库必须是下界，而不是 0（后端会整批拒绝）。
    expect(payload[`${OPTION_KEY}.body_full_bytes`]).toBe('1048576')
  })

  it('warns that 0% metadata kills every trace link', async () => {
    const user = userEvent.setup()
    renderSection()

    const metadata = await screen.findByLabelText('Metadata sampling (%)')
    await user.clear(metadata)
    await user.type(metadata, '0')

    expect(
      screen.getByText(/At 0% the log page shows no trace links at all/)
    ).toBeTruthy()
  })
})
