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

import { scanMedia } from '../payload-media'

/** 192 base64 chars decode to exactly 144 bytes. */
const B64 = 'A'.repeat(192)

describe('scanMedia images response', () => {
  it('lifts b64_json out of an OpenAI image response and leaves a marker', () => {
    const body = {
      created: 1,
      data: [{ b64_json: B64, revised_prompt: 'a cat' }],
      usage: { generated_images: 1 },
    }

    const { items, view } = scanMedia(body)

    expect(items).toHaveLength(1)
    expect(items[0].src).toBe(`data:image/png;base64,${B64}`)
    expect(items[0].label).toBe('PNG · 144 B')
    expect(items[0].remote).toBe(false)

    const first = (view as { data: Array<{ b64_json: string }> }).data[0]
    expect(first.b64_json).toBe('[image 1 · PNG · 144 B]')
    // The rest of the body survives untouched.
    expect((view as { usage: unknown }).usage).toEqual({ generated_images: 1 })
  })

  it('honours output_format instead of assuming png', () => {
    const { items } = scanMedia({
      data: [{ b64_json: B64, output_format: 'jpeg' }],
    })
    expect(items[0].src.startsWith('data:image/jpeg;base64,')).toBe(true)
    expect(items[0].label).toBe('JPEG · 144 B')
  })

  it('takes a plain url next to b64_json even without an extension', () => {
    // Signed CDN links carry no extension; the envelope is the evidence.
    const { items } = scanMedia({
      created: 1730000000,
      data: [{ url: 'https://cdn.example.com/9f2c?token=abc' }],
    })
    expect(items).toHaveLength(1)
    expect(items[0].src).toBe('https://cdn.example.com/9f2c?token=abc')
    expect(items[0].remote).toBe(true)
    expect(items[0].label).toBe('URL')
  })

  it('does not trust a bare data array as an image envelope', () => {
    const { items } = scanMedia({
      data: [{ url: 'https://api.example.com/v1/users' }],
    })
    expect(items).toHaveLength(0)
  })

  it('collects every image in a batch, up to the cap', () => {
    const data = Array.from({ length: 15 }, () => ({ b64_json: B64 }))
    const { items } = scanMedia({ data })
    expect(items).toHaveLength(12)
  })
})

describe('scanMedia vision input', () => {
  it('reads image_url given as {url} with a data URI', () => {
    const uri = `data:image/webp;base64,${B64}`
    const body = {
      messages: [
        {
          role: 'user',
          content: [
            { type: 'text', text: 'what is this' },
            { type: 'image_url', image_url: { url: uri } },
          ],
        },
      ],
    }

    const { items, view } = scanMedia(body)
    expect(items).toHaveLength(1)
    expect(items[0].src).toBe(uri)
    expect(items[0].label).toBe('WEBP · 144 B')

    const part = (
      view as { messages: Array<{ content: Array<{ image_url: { url: string } }> }> }
    ).messages[0].content[1]
    expect(part.image_url.url).toBe('[image 1 · WEBP · 144 B]')
    // The text part is untouched.
    expect(
      (view as { messages: Array<{ content: Array<{ text?: string }> }> }).messages[0]
        .content[0].text
    ).toBe('what is this')
  })

  it('keeps a remote image_url', () => {
    const { items } = scanMedia({
      messages: [
        {
          role: 'user',
          content: [{ type: 'image_url', image_url: { url: 'https://x.test/a.png' } }],
        },
      ],
    })
    expect(items[0].remote).toBe(true)
  })

  it('reads the Anthropic base64 source block', () => {
    const { items, view } = scanMedia({
      content: [
        {
          type: 'image',
          source: { type: 'base64', media_type: 'image/png', data: B64 },
        },
      ],
    })
    expect(items[0].src).toBe(`data:image/png;base64,${B64}`)
    const block = (view as { content: Array<{ source: { data: string } }> }).content[0]
    expect(block.source.data).toBe('[image 1 · PNG · 144 B]')
  })

  it('reads Gemini inlineData', () => {
    const { items } = scanMedia({
      contents: [
        { parts: [{ inlineData: { mimeType: 'image/jpeg', data: B64 } }] },
      ],
    })
    expect(items[0].src.startsWith('data:image/jpeg;base64,')).toBe(true)
  })
})

describe('scanMedia limits', () => {
  it('leaves ordinary JSON untouched and returns the same reference', () => {
    const body = { model: 'gpt-4o-mini', messages: [{ role: 'user', content: 'hi' }] }
    const { items, view } = scanMedia(body)
    expect(items).toHaveLength(0)
    // Copy-on-write: no media means no clone, which is what keeps a large chat
    // payload from being duplicated just to be scanned.
    expect(view).toBe(body)
  })

  it('does not treat an arbitrary link field as an image', () => {
    const { items } = scanMedia({ link: 'https://example.com/docs' })
    expect(items).toHaveLength(0)
  })

  it('refuses svg data URIs', () => {
    const { items } = scanMedia({
      data: [{ b64_json: B64, output_format: 'svg' }],
    })
    // output_format falls back to png; an svg is never accepted as a source.
    expect(items[0].src.startsWith('data:image/png;base64,')).toBe(true)
    expect(scanMedia({ u: `data:image/svg+xml;base64,${B64}` }).items).toHaveLength(0)
  })

  it('marks an oversized blob instead of putting it in the DOM', () => {
    const huge = 'A'.repeat(6 * 1024 * 1024)
    const { items, view } = scanMedia({ data: [{ b64_json: huge }] })
    expect(items).toHaveLength(0)
    expect(
      (view as { data: Array<{ b64_json: string }> }).data[0].b64_json
    ).toContain('too large to preview')
  })
})
