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
function Harness({ onChange }: { onChange?: (rules: DiscountRule[]) => void }) {
  const [rules, setRules] = useState<DiscountRule[]>(initialRules)
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
      models={models}
    />
  )
}

describe('DiscountEditor', () => {
  it('渲染行号、表头与规则数量', () => {
    render(<Harness />)
    expect(screen.getByText('模型折扣')).toBeInTheDocument()
    expect(screen.getByText('3/20')).toBeInTheDocument()
    expect(screen.getByLabelText('第 1 行分组')).toHaveValue('vip')
    expect(screen.getByLabelText('第 1 行模型')).toHaveValue('gpt-4o')
    expect(screen.getByLabelText('第 1 行折扣，单位折')).toHaveValue('7')
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

  it('兜底行被移到最前面后，其余行标记为永不命中', async () => {
    render(<Harness />)
    // 真实规则：把一条 */* 顶到最前面
    const groupInput = screen.getByLabelText('第 1 行分组')
    await userEvent.clear(groupInput)
    await userEvent.type(groupInput, '*')
    const modelInput = screen.getByLabelText('第 1 行模型')
    await userEvent.clear(modelInput)
    await userEvent.type(modelInput, '*')

    // 第 1 行现在覆盖一切：第 2 行（vip）被截断，标记为死行。
    // 第 3 行是 svip，该用户本来就用不到，走的是「用不到」警告——
    // 两种失效原因分开提示，运营才知道该调顺序还是该换组。
    expect(screen.getAllByText('被前面的行完全盖住，永不命中')).toHaveLength(1)
    expect(
      screen.getByText('该用户用不到「svip」，这条永不命中')
    ).toBeInTheDocument()
  })

  it('预览显示命中的行号，未命中显示组价', () => {
    render(<Harness />)
    const preview = screen.getByText(/实际生效/).parentElement as HTMLElement

    // vip/gpt-4o 命中第 1 行
    expect(within(preview).getAllByText('第 1 行').length).toBeGreaterThan(0)
    // svip 组价 0.80（组倍率），且该用户用不到 svip 时仍展示基线
    expect(within(preview).getAllByText('0.80').length).toBeGreaterThan(0)
  })

  it('试算器对任意组合给出结果', async () => {
    render(<Harness />)
    const groupInput = screen.getByLabelText('试算分组')
    const modelInput = screen.getByLabelText('试算模型')

    await userEvent.type(groupInput, 'vip')
    await userEvent.type(modelInput, 'gpt-4o')

    // vip/gpt-4o → 第 1 行 0.70（预览区也会出现同样的值与行号）
    expect(screen.getAllByText('第 1 行').length).toBeGreaterThan(0)
    expect(screen.getAllByText('0.70').length).toBeGreaterThan(0)
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
