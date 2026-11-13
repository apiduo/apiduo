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
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'

import { DiscountEditor } from '../components/discount-editor'
import type { DiscountRule } from '../types'

const groups = ['default', 'vip', 'svip']
const usableGroups = ['default', 'vip']
const groupRatios = { default: 1, vip: 0.9, svip: 0.8 }
const models = ['gpt-4o', 'claude-3.7', 'gemini-2.5']

const initialRules: DiscountRule[] = [
  { groups: 'vip', models: 'gpt-4o', rate: 0.7, until: 0 },
  { groups: 'vip', models: '*', rate: 0.8, until: 0 },
  { groups: 'svip', models: '*', rate: 0.9, until: 0 },
]

/** 受控组件的测试宿主：让 onChange 真的回写，才能验证排序/删除后的渲染。 */
function Harness({
  onChange,
  modelNames = models,
  initial = initialRules,
}: {
  onChange?: (rules: DiscountRule[]) => void
  modelNames?: string[]
  initial?: DiscountRule[]
}) {
  const [rules, setRules] = useState<DiscountRule[]>(initial)
  return (
    <DiscountEditor
      rules={rules}
      onChange={(next) => {
        setRules(next)
        onChange?.(next)
      }}
      groups={groups}
      usableGroups={usableGroups}
      groupRatios={groupRatios}
      models={modelNames}
    />
  )
}

describe('DiscountEditor', () => {
  it('渲染行号、表头与规则数量', () => {
    render(<Harness />)
    expect(screen.getByText('模型折扣')).toBeInTheDocument()
    expect(screen.getByText('3/20')).toBeInTheDocument()
    expect(screen.getByLabelText('第 1 行分组')).toHaveValue('vip')
    // 模型列是搜索式多选：一个值时直接显示名字，多值时显示首个 + 计数
    expect(screen.getByText('gpt-4o')).toBeInTheDocument()
    expect(screen.getAllByText('全部模型').length).toBeGreaterThan(0)
    expect(screen.getByLabelText('第 1 行折扣，单位折')).toHaveValue('7')
  })

  it('模型列用一行摘要代替逗号串', () => {
    render(
      <Harness
        initial={[
          { groups: 'vip', models: 'gpt-4o, claude-3.7', rate: 0.7, until: 0 },
        ]}
      />
    )

    expect(screen.getByText('gpt-4o +1')).toBeInTheDocument()
    expect(screen.queryByText('gpt-4o, claude-3.7')).toBeNull()
    expect(screen.getByText('1/20')).toBeInTheDocument()
  })

  it('允许录入目录里还没有的模型名', async () => {
    render(<Harness />)
    const input = screen.getByLabelText('第 1 行模型')
    await userEvent.type(input, 'zz-not-in-catalog')

    // 没有 allowCreate 时这里只会显示「没有匹配项」，运营无法先配规则后上渠道
    expect(await screen.findByText(/zz-not-in-catalog/)).toBeInTheDocument()
  })

  it('折扣输入即时换算成倍率', async () => {
    const onChange = vi.fn()
    render(<Harness onChange={onChange} />)

    const input = screen.getByLabelText('第 1 行折扣，单位折')
    await userEvent.clear(input)
    await userEvent.type(input, '7.5')

    const last = onChange.mock.calls.at(-1)?.[0] as DiscountRule[]
    expect(last[0].rate).toBe(0.75)
  })

  it('上移一行改变顺序', async () => {
    const onChange = vi.fn()
    render(<Harness onChange={onChange} />)

    await userEvent.click(screen.getByLabelText('上移第 2 行'))

    const last = onChange.mock.calls.at(-1)?.[0] as DiscountRule[]
    expect(last[0].models).toBe('*')
    expect(last[1].models).toBe('gpt-4o')
  })

  it('第一行不能上移，最后一行不能下移', () => {
    render(<Harness />)
    expect(screen.getByLabelText('上移第 1 行')).toBeDisabled()
    expect(screen.getByLabelText('下移第 3 行')).toBeDisabled()
  })

  it('删除一行后行数减少', async () => {
    render(<Harness />)
    await userEvent.click(screen.getByLabelText('删除第 2 行'))
    expect(screen.getByText('2/20')).toBeInTheDocument()
    expect(screen.queryByLabelText('第 3 行分组')).toBeNull()
  })

  it('空分组给出阻断级错误', async () => {
    render(<Harness />)
    const input = screen.getByLabelText('第 1 行分组')
    await userEvent.clear(input)
    expect(screen.getByText('填分组，或 * 表示全部')).toBeInTheDocument()
    expect(screen.getByText(/保存会被拒绝/)).toBeInTheDocument()
  })

  it('该用户用不到的分组给出警告，但不阻断保存', async () => {
    render(<Harness />)
    // 第 3 行是 svip，该用户可用分组里没有
    expect(
      screen.getByText('该用户用不到「svip」，这条永不命中')
    ).toBeInTheDocument()
    expect(screen.queryByText(/保存会被拒绝/)).toBeNull()
  })

  it('兜底行被顶到最前面时，后面两行都标记为永不命中', () => {
    render(
      <Harness
        initial={[
          { groups: '*', models: '*', rate: 0.95, until: 0 },
          { groups: 'vip', models: 'gpt-4o', rate: 0.7, until: 0 },
          { groups: 'vip', models: '*', rate: 0.8, until: 0 },
          { groups: 'svip', models: '*', rate: 0.9, until: 0 },
        ]}
      />
    )

    // 第 1 行覆盖一切：点名 gpt-4o 的行和 vip 兜底行都轮不到。
    // 第 4 行是 svip，该用户本来就用不到，走的是「用不到」警告——
    // 两种失效原因分开提示，运营才知道该调顺序还是该换组。
    expect(screen.getAllByText('被前面的行完全盖住，永不命中')).toHaveLength(2)
    expect(
      screen.getByText('该用户用不到「svip」，这条永不命中')
    ).toBeInTheDocument()
  })

  it('默认组没有规则命中时，生效检查只报一段组价', () => {
    render(<Harness />)
    const panel = screen.getByRole('region', { name: '生效检查' })

    expect(within(panel).getByText('组价')).toBeInTheDocument()
    expect(within(panel).getByText('×1.00')).toBeInTheDocument()
    expect(within(panel).getByText('全部 3 个模型')).toBeInTheDocument()
    expect(within(panel).queryByText('第 1 行')).toBeNull()
  })

  it('切到某个分组后，按「点名模型 + 兜底」分段', async () => {
    render(<Harness />)
    await userEvent.click(screen.getByRole('tab', { name: 'vip' }))

    const panel = screen.getByRole('region', { name: '生效检查' })
    // 第 1 行点名 gpt-4o，第 2 行 * 收走其余全部，因此没有组价段
    expect(within(panel).getByText('第 1 行')).toBeInTheDocument()
    expect(within(panel).getByText('×0.70')).toBeInTheDocument()
    expect(within(panel).getByText('第 2 行')).toBeInTheDocument()
    expect(within(panel).getByText('×0.80')).toBeInTheDocument()
    expect(within(panel).queryByText('组价')).toBeNull()
    // vip 组价 0.90：7 折实际是 -22.2%，不是名义上的 -30%
    expect(within(panel).getByText('较组价 -22.2%')).toBeInTheDocument()
    expect(within(panel).getByText('较组价 -11.1%')).toBeInTheDocument()
  })

  it('规则折后仍高于组价时，标出涨价幅度', async () => {
    render(
      <Harness initial={[{ groups: 'vip', models: '*', rate: 0.95, until: 0 }]} />
    )
    await userEvent.click(screen.getByRole('tab', { name: 'vip' }))

    const panel = screen.getByRole('region', { name: '生效检查' })
    // 名义上「9.5 折」，对组价 0.90 的用户其实是涨价 5.6%
    expect(within(panel).getByText('较组价 +5.6%')).toBeInTheDocument()
  })

  it('涨价的行内提示与面板数字一致', () => {
    render(
      <Harness initial={[{ groups: 'vip', models: '*', rate: 0.95, until: 0 }]} />
    )
    expect(
      screen.getByText('「vip」组价 0.90，这条 0.95 是涨价 5.6%')
    ).toBeInTheDocument()
  })

  it('模型名拼错时提示不在目录里', () => {
    render(
      <Harness
        initial={[{ groups: 'vip', models: 'gpt-4o-mini2', rate: 0.8, until: 0 }]}
      />
    )
    expect(screen.getByText(/不在目录里，确认没拼错/)).toBeInTheDocument()
  })

  it('试算只针对当前分组，命中即停', async () => {
    render(<Harness />)
    await userEvent.click(screen.getByRole('tab', { name: 'vip' }))
    await userEvent.type(screen.getByLabelText('试算模型'), 'gpt-4o')

    const result = screen.getByRole('status', { name: '试算结果' })
    expect(within(result).getByText('×0.70')).toBeInTheDocument()
    expect(within(result).getByText('第 1 行')).toBeInTheDocument()
  })

  it('1000 个模型时预览仍只按规则分段，不逐模型铺开', () => {
    const many = Array.from({ length: 1000 }, (_, index) => `model-${index}`)
    render(<Harness modelNames={many} />)

    const panel = screen.getByRole('region', { name: '生效检查' })
    expect(within(panel).getByText('全部 1000 个模型')).toBeInTheDocument()
    // 候选模型全部进 datalist，不再被 slice(0, 200) 静默截断
    expect(
      document.querySelectorAll('#apiduo-discount-models option').length
    ).toBe(1001)
  })

  it('行容器带 apiduo 标识', () => {
    const { container } = render(<Harness />)
    const marked = container.querySelector('[data-apiduo="text"]')
    expect(marked).not.toBeNull()
  })

  it('到期列回填日期，空值表示永久', () => {
    render(<Harness />)
    expect(screen.getByLabelText('第 1 行到期日')).toHaveValue('')
  })
})
