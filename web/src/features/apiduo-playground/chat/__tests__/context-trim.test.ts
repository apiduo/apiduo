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

import type { Message } from '@/features/playground/types'

import { estimateTokens, trimToContext } from '../use-chat-handler'

function message(key: string, content: string, from: 'user' | 'assistant' = 'user'): Message {
  return {
    key,
    from,
    versions: [{ id: `${key}-v0`, content }],
    status: 'complete',
  }
}

describe('estimateTokens', () => {
  it('counts a CJK character as one token and four Latin characters as one', () => {
    // 4 Latin chars -> 1 token; 3 CJK chars -> 3 tokens.
    expect(estimateTokens('abcd')).toBe(1)
    expect(estimateTokens('你好呀')).toBe(3)
    expect(estimateTokens('')).toBe(0)
  })
})

describe('trimToContext', () => {
  const messages = [
    message('a', '一'.repeat(600)),
    message('b', '二'.repeat(600), 'assistant'),
    message('c', '三'.repeat(600)),
  ]

  it('leaves the list alone when no budget is given', () => {
    expect(trimToContext(messages)).toHaveLength(3)
  })

  it('leaves the list alone when it fits', () => {
    expect(trimToContext(messages, 5000)).toHaveLength(3)
  })

  it('drops the oldest turns until the newest fit', () => {
    const kept = trimToContext(messages, 1500)
    expect(kept.map((item) => item.key)).toEqual(['b', 'c'])
  })

  it('always keeps the newest message, even when it alone exceeds the budget', () => {
    const kept = trimToContext(messages, 10)
    expect(kept).toHaveLength(1)
    expect(kept[0].key).toBe('c')
  })

  it('handles a message without versions', () => {
    const broken: Message = { key: 'x', from: 'assistant', versions: [] }
    expect(trimToContext([broken, message('y', 'hi')], 1000).map((m) => m.key)).toEqual([
      'x',
      'y',
    ])
  })
})
