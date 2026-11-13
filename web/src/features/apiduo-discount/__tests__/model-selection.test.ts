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
import { describe, expect, it } from 'vitest'

import { mergeModelSelection, selectionSummary } from '../lib/model-selection'

describe('mergeModelSelection', () => {
  it('多选结果按逗号拼接', () => {
    expect(mergeModelSelection('gpt-4o', ['gpt-4o', 'claude-3.7'])).toBe(
      'gpt-4o,claude-3.7'
    )
  })

  it('删除芯片后只留剩下的名字', () => {
    expect(mergeModelSelection('gpt-4o,claude-3.7', ['claude-3.7'])).toBe(
      'claude-3.7'
    )
    expect(mergeModelSelection('gpt-4o', [])).toBe('')
  })

  it('新加 * 会清掉所有具体名字', () => {
    expect(mergeModelSelection('gpt-4o,claude-3.7', ['*'])).toBe('*')
    expect(
      mergeModelSelection('gpt-4o', ['gpt-4o', 'claude-3.7', '*'])
    ).toBe('*')
  })

  it('在 * 之上新加具体模型会丢掉 *', () => {
    expect(mergeModelSelection('*', ['gpt-4o'])).toBe('gpt-4o')
    // 一次粘贴多个名字同样顶掉 *
    expect(mergeModelSelection('*', ['gpt-4o', 'claude-3.7'])).toBe(
      'gpt-4o,claude-3.7'
    )
  })

  it('* 与名字并存时以最后新增的一侧为准', () => {
    expect(mergeModelSelection('gpt-4o', ['gpt-4o', '*'])).toBe('*')
    expect(mergeModelSelection('*', ['*', 'claude-3.7'])).toBe('claude-3.7')
  })
})

describe('selectionSummary', () => {
  it('单个模型直接显示名字，* 显示为「全部模型」', () => {
    expect(selectionSummary(['gpt-4o'])).toBe('gpt-4o')
    expect(selectionSummary(['*'])).toBe('全部模型')
  })

  it('多个模型只留首个 + 计数，避免把行撑高', () => {
    expect(selectionSummary(['gpt-4o', 'claude-3.7'])).toBe('gpt-4o +1')
    expect(selectionSummary(['*', 'gpt-4o'])).toBe('全部模型 +1')
  })

  it('空选择返回空串', () => {
    expect(selectionSummary([])).toBe('')
  })
})
