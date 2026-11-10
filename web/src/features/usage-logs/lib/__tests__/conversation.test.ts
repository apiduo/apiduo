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
along with this program. If not <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { describe, expect, it } from 'vitest'

import { parseJsonBody, parsePayload, scrubBase64 } from '../conversation'

describe('parsePayload request', () => {
  it('renders an OpenAI chat body as turns and keeps the knobs apart', () => {
    const raw = JSON.stringify({
      model: 'gpt-4o-mini',
      stream: true,
      temperature: 0.3,
      messages: [
        { role: 'system', content: '你是客服。' },
        { role: 'user', content: '端到端验证：流式' },
      ],
    })

    const parsed = parsePayload(raw, 'request')
    expect(parsed).not.toBeNull()
    expect(parsed!.turns).toEqual([
      { role: 'system', name: undefined, text: '你是客服。' },
      { role: 'user', name: undefined, text: '端到端验证：流式' },
    ])
    // model/stream/temperature describe the call, not a message: they must not
    // be mixed into the bubbles or the reader cannot tell prompt from config.
    expect(parsed!.extras).toEqual([
      { key: 'model', value: 'gpt-4o-mini' },
      { key: 'stream', value: 'true' },
      { key: 'temperature', value: '0.3' },
    ])
  })

  it('lifts an Anthropic top-level system prompt ahead of the messages', () => {
    const raw = JSON.stringify({
      model: 'claude-3-7-sonnet',
      system: 'Be brief.',
      messages: [{ role: 'user', content: 'hi' }],
    })
    const parsed = parsePayload(raw, 'request')!
    expect(parsed.turns.map((t) => t.role)).toEqual(['system', 'user'])
    expect(parsed.turns[0].text).toBe('Be brief.')
  })

  it('treats a Responses API string input as a single user turn', () => {
    const parsed = parsePayload(JSON.stringify({ model: 'o3', input: 'ping' }), 'request')!
    expect(parsed.turns).toEqual([{ role: 'user', name: undefined, text: 'ping' }])
  })

  it('keeps non-text parts visible as placeholders', () => {
    const raw = JSON.stringify({
      messages: [
        {
          role: 'user',
          content: [
            { type: 'text', text: '这张图里是什么' },
            { type: 'image_url', image_url: { url: 'data:image/png;base64,AAA' } },
          ],
        },
      ],
    })
    const parsed = parsePayload(raw, 'request')!
    // Dropping the image would make the model's answer look ungrounded.
    expect(parsed.turns[0].text).toBe('这张图里是什么\n[image]')
  })

  it('renders tool calls next to the assistant text', () => {
    const raw = JSON.stringify({
      messages: [
        {
          role: 'assistant',
          content: '',
          tool_calls: [
            { id: 'call_1', function: { name: 'get_weather', arguments: '{"city":"北京"}' } },
          ],
        },
      ],
    })
    const parsed = parsePayload(raw, 'request')!
    expect(parsed.turns[0].text).toBe('→ get_weather({"city":"北京"})')
  })

  it('keeps an empty turn instead of dropping it', () => {
    const raw = JSON.stringify({ messages: [{ role: 'assistant', content: '' }] })
    const parsed = parsePayload(raw, 'request')!
    // Order matters to a reader reconstructing the exchange.
    expect(parsed.turns).toHaveLength(1)
    expect(parsed.turns[0].text).toBe('（空）')
  })
})

describe('parsePayload response', () => {
  it('reads the exporter normalised shape', () => {
    const raw = JSON.stringify({
      finish_reason: 'stop',
      text: '北京明天多云转晴。[REDACTED:api_key]',
    })
    const parsed = parsePayload(raw, 'response')!
    expect(parsed.turns).toEqual([
      { role: 'assistant', name: undefined, text: '北京明天多云转晴。[REDACTED:api_key]' },
    ])
    expect(parsed.extras).toEqual([{ key: 'finish_reason', value: 'stop' }])
  })

  it('falls back to an OpenAI raw completion body', () => {
    const raw = JSON.stringify({
      choices: [
        {
          index: 0,
          message: { role: 'assistant', content: 'hello' },
          finish_reason: 'stop',
        },
      ],
      usage: { prompt_tokens: 21, completion_tokens: 13 },
    })
    const parsed = parsePayload(raw, 'response')!
    expect(parsed.turns[0].text).toBe('hello')
    expect(parsed.extras).toContainEqual({ key: 'finish_reason', value: 'stop' })
    expect(parsed.extras).toContainEqual({ key: 'prompt_tokens', value: '21' })
  })

  it('falls back to an Anthropic raw content-block body', () => {
    const raw = JSON.stringify({
      content: [{ type: 'text', text: 'hi there' }],
      stop_reason: 'end_turn',
    })
    const parsed = parsePayload(raw, 'response')!
    expect(parsed.turns[0].text).toBe('hi there')
    expect(parsed.extras).toContainEqual({ key: 'stop_reason', value: 'end_turn' })
  })
})

describe('parsePayload fallbacks', () => {
  it('returns null for truncated JSON so the caller shows the raw text', () => {
    // headtail capture cuts the body at the byte level; a half-parsed view
    // would hide whichever fields were cut off.
    const truncated = '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"端到'
    expect(parsePayload(truncated, 'request')).toBeNull()
  })

  it('returns null for plain text', () => {
    expect(parsePayload('upstream 500: rate limited', 'response')).toBeNull()
  })

  it('returns null when the JSON has no recognisable conversation', () => {
    expect(parsePayload(JSON.stringify({ ok: true }), 'request')).toBeNull()
    expect(parsePayload(JSON.stringify({ ok: true }), 'response')).toBeNull()
  })
})

describe('scrubBase64', () => {
  it('replaces a long base64 run with a size marker', () => {
    // 400 chars of base64-ish text: one unbreakable token the browser would
    // have to lay out as a single line.
    const blob = 'A'.repeat(400)
    const cleaned = scrubBase64(`图片如下：${blob} 结束`)
    expect(cleaned).toContain('[base64 0 KB]')
    expect(cleaned).not.toContain(blob)
    expect(cleaned).toContain('图片如下：')
  })

  it('leaves ordinary text alone', () => {
    const text = '这是一段普通的中文，长度不到阈值。'
    expect(scrubBase64(text)).toBe(text)
  })

  it('is applied to content parts as well', () => {
    const raw = JSON.stringify({
      messages: [
        {
          role: 'user',
          content: [{ type: 'text', text: `data:image/png;base64,${'B'.repeat(500)}` }],
        },
      ],
    })
    const parsed = parsePayload(raw, 'request')!
    expect(parsed.turns[0].text).toContain('[base64')
    expect(parsed.turns[0].text.length).toBeLessThan(200)
  })
})

describe('parseJsonBody', () => {
  it('decodes a body the conversation view rejects', () => {
    const raw = JSON.stringify({
      created: 1762740000,
      data: [{ b64_json: 'AAAA' }],
      usage: { total_tokens: 12 },
    })
    // The conversation parser declines (no messages, no choices); the tree is
    // the view that still makes an image response readable.
    expect(parsePayload(raw, 'response')).toBeNull()
    expect(parseJsonBody(raw)).toEqual({
      created: 1762740000,
      data: [{ b64_json: 'AAAA' }],
      usage: { total_tokens: 12 },
    })
  })

  it('returns null for anything that is not an object or array', () => {
    expect(parseJsonBody('"just a string"')).toBeNull()
    expect(parseJsonBody('123')).toBeNull()
    expect(parseJsonBody('true')).toBeNull()
    expect(parseJsonBody('null')).toBeNull()
    expect(parseJsonBody('upstream 500: rate limited')).toBeNull()
  })

  it('returns null for a truncated body so the caller shows raw text', () => {
    const raw = JSON.stringify({
      messages: [{ role: 'user', content: 'x'.repeat(80) }],
    })
    expect(parseJsonBody(raw)).not.toBeNull()
    expect(parseJsonBody(raw.slice(0, 40))).toBeNull()
  })
})
