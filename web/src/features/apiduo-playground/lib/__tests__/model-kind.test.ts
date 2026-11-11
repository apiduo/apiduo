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

import { modelKind } from '../model-kind'

describe('modelKind', () => {
  it('trusts an explicit video endpoint', () => {
    expect(modelKind('whatever-1', ['openai-video'])).toBe('video')
  })

  it('recognises the production hailuo models even though they report openai', () => {
    for (const name of ['hailuo-video', 'hailuo-2.3', 'hailuo-video-sec']) {
      expect(modelKind(name, ['openai'])).toBe('video')
    }
  })

  it('recognises other video families by name', () => {
    expect(modelKind('doubao-seedance-1-0-pro', ['openai'])).toBe('video')
    expect(modelKind('veo-3', [])).toBe('video')
    expect(modelKind('kling-v2', ['openai'])).toBe('video')
    expect(modelKind('wanx2.1-t2v-turbo', ['openai'])).toBe('video')
  })

  it('trusts an explicit image endpoint', () => {
    expect(modelKind('flux-3-image', ['image-generation', 'openai'])).toBe(
      'image'
    )
    expect(modelKind('doubao-seedream-5-0-flash-260915', ['image-generation'])).toBe(
      'image'
    )
  })

  it('recognises image families that only report openai', () => {
    expect(modelKind('gpt-image-2.6-flare', ['openai'])).toBe('image')
    expect(modelKind('nano-banana-2', ['openai'])).toBe('image')
    expect(modelKind('dall-e-3', [])).toBe('image')
  })

  it('leaves ordinary text models in chat', () => {
    expect(modelKind('gpt-4o-mini', ['openai'])).toBe('chat')
    expect(modelKind('deepseek-v4-1-flash-261017', ['openai', 'anthropic'])).toBe(
      'chat'
    )
    expect(modelKind('deepseek-search', ['deepseek-tools-search'])).toBe('chat')
    expect(modelKind('mystery-model', [])).toBe('chat')
  })
})
