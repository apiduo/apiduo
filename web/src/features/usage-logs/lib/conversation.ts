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
/**
 * Turns a captured request/response body into conversation turns.
 *
 * Why this exists: the trace drawer used to show the raw JSON. That is fine for
 * the person who wrote the gateway, and useless for the person who opens the
 * drawer to answer "did we send the right prompt to this customer". Langfuse
 * renders the same payload as User / Assistant blocks; this is the same idea,
 * applied locally so the console does not have to round-trip to Langfuse for a
 * view it can build itself.
 *
 * Contract: parse what you can, return null when nothing is recognisable, and
 * let the caller fall back to the raw text. A half-parsed payload that silently
 * drops fields is worse than a JSON blob — the operator would not know whether
 * the missing part was never sent or was eaten by the renderer.
 */

export interface ConversationTurn {
  role: string
  /** Tool/function name, shown as a suffix on the role badge. */
  name?: string
  text: string
}

export interface ParsedPayload {
  turns: ConversationTurn[]
  /** Scalar request knobs (model, stream, temperature…) kept out of the bubbles. */
  extras: Array<{ key: string; value: string }>
}

/** Keys that describe the call itself rather than a message. */
const KNOB_KEYS = [
  'model',
  'stream',
  'temperature',
  'top_p',
  'max_tokens',
  'max_completion_tokens',
  'stop',
  'n',
  'size',
  'quality',
  'style',
  'response_format',
  'tool_choice',
  'parallel_tool_calls',
  'reasoning_effort',
  'frequency_penalty',
  'presence_penalty',
  'seed',
  'user',
]

function asRecord(value: unknown): Record<string, unknown> | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null
  return value as Record<string, unknown>
}

/**
 * Flattens a message body that may be a plain string, an array of content
 * parts (OpenAI vision / Anthropic blocks), or missing entirely.
 *
 * Non-text parts become short placeholders rather than being dropped: an
 * operator who sees "[image]" knows the model got a picture, which is often the
 * thing they were checking.
 */
/**
 * Replaces long base64 runs with a size marker.
 *
 * A data URI that arrives as message *text* (rather than as an `image_url`
 * part) is several hundred KB of one unbroken token. The browser has to lay it
 * out as a single line that cannot wrap, which is where the drawer freezes.
 * The marker keeps the fact that there was an image and drops the payload,
 * which the drawer cannot render usefully at any size.
 */
const BASE64_RUN = /[A-Za-z0-9+/]{300,}={0,2}/g

export function scrubBase64(value: string): string {
  if (value.length < 300) return value
  return value.replace(
    BASE64_RUN,
    (run) => `[base64 ${Math.round(run.length / 1024)} KB]`
  )
}

export function contentToText(content: unknown): string {
  if (typeof content === 'string') return scrubBase64(content)
  if (!Array.isArray(content)) return ''

  const parts: string[] = []
  for (const part of content) {
    const p = asRecord(part)
    if (!p) {
      if (typeof part === 'string') parts.push(part)
      continue
    }
    const type = typeof p.type === 'string' ? p.type : ''
    if (type === 'image_url' || type === 'input_image' || type === 'image') {
      parts.push('[image]')
    } else if (typeof p.text === 'string') {
      parts.push(scrubBase64(p.text))
    } else if (type === 'tool_result' || type === 'tool_use') {
      parts.push(`[${type}]`)
    }
  }
  return parts.join('\n')
}

function toolCallsToText(calls: unknown): string {
  if (!Array.isArray(calls) || calls.length === 0) return ''
  const lines: string[] = []
  for (const call of calls) {
    const c = asRecord(call)
    if (!c) continue
    const fn = asRecord(c.function)
    const name =
      (typeof fn?.name === 'string' && fn.name) ||
      (typeof c.name === 'string' && c.name) ||
      'tool'
    const args = fn?.arguments ?? c.arguments ?? c.input
    let shown = ''
    if (typeof args === 'string') shown = args
    else if (args !== undefined) {
      try {
        shown = JSON.stringify(args)
      } catch {
        shown = ''
      }
    }
    lines.push(`→ ${name}(${shown})`)
  }
  return lines.join('\n')
}

function scalarToString(value: unknown): string | null {
  if (typeof value === 'string') return value
  if (typeof value === 'number' || typeof value === 'boolean') return String(value)
  return null
}

function collectExtras(obj: Record<string, unknown>): Array<{ key: string; value: string }> {
  const extras: Array<{ key: string; value: string }> = []
  for (const key of KNOB_KEYS) {
    const shown = scalarToString(obj[key])
    if (shown !== null) extras.push({ key, value: shown })
  }
  return extras
}

/** Normalises one raw message object into a turn, or null if it has no role. */
function messageToTurn(raw: unknown): ConversationTurn | null {
  const msg = asRecord(raw)
  if (!msg) return null

  const role = typeof msg.role === 'string' ? msg.role : ''
  if (!role) return null

  const text = contentToText(msg.content)
  const calls = toolCallsToText(msg.tool_calls ?? msg.tool_use)
  const combined = [text, calls].filter(Boolean).join('\n')

  let name: string | undefined
  if (typeof msg.name === 'string') {
    name = msg.name
  } else {
    // Legacy OpenAI function-calling nests the name one level down.
    const fnName = asRecord(msg.function)?.name
    if (typeof fnName === 'string') name = fnName
  }

  // A message with a role but no renderable body still carries information
  // (an empty assistant turn before tool calls). Keep it with a placeholder
  // rather than dropping it, so the turn order stays intact.
  return { role, name, text: combined || '（空）' }
}

/**
 * Request bodies. Covers the three shapes that actually reach this gateway:
 * OpenAI chat (`messages`), Anthropic (`system` + `messages`), and the
 * Responses API (`input` as string or array).
 */
function parseRequest(obj: Record<string, unknown>): ParsedPayload | null {
  const turns: ConversationTurn[] = []

  const system = obj.system
  if (typeof system === 'string' && system.trim()) {
    turns.push({ role: 'system', text: system })
  } else {
    const blocks = contentToText(system)
    if (blocks.trim()) turns.push({ role: 'system', text: blocks })
  }

  const messages = obj.messages
  if (Array.isArray(messages)) {
    for (const raw of messages) {
      const turn = messageToTurn(raw)
      if (turn) turns.push(turn)
    }
  }

  // Responses API: `input` is either a plain string or the same message array.
  const input = obj.input
  if (turns.length === 0 && typeof input === 'string' && input.trim()) {
    turns.push({ role: 'user', text: input })
  } else if (turns.length === 0 && Array.isArray(input)) {
    for (const raw of input) {
      if (typeof raw === 'string') {
        turns.push({ role: 'user', text: raw })
        continue
      }
      const turn = messageToTurn(raw)
      if (turn) turns.push(turn)
    }
  }

  if (turns.length === 0) return null
  return { turns, extras: collectExtras(obj) }
}

/**
 * Response bodies. The exporter already normalises the common case to
 * `{text, tool_calls, finish_reason}`; the OpenAI and Anthropic raw shapes are
 * handled too, because headtail capture can hand back the untouched body when
 * the parser declines to claim a format.
 */
function parseResponse(obj: Record<string, unknown>): ParsedPayload | null {
  const turns: ConversationTurn[] = []
  const extras: Array<{ key: string; value: string }> = []

  const finish = scalarToString(obj.finish_reason)
  if (finish) extras.push({ key: 'finish_reason', value: finish })

  const usage = asRecord(obj.usage)
  if (usage) {
    const prompt = scalarToString(usage.prompt_tokens)
    const completion = scalarToString(usage.completion_tokens)
    if (prompt) extras.push({ key: 'prompt_tokens', value: prompt })
    if (completion) extras.push({ key: 'completion_tokens', value: completion })
  }

  // Anthropic puts tool calls inside `content` as tool_use blocks, so the
  // generic path has to look there too — otherwise the drawer shows "[tool_use]"
  // and hides which function was called with what.
  const anthropicCalls = Array.isArray(obj.content)
    ? obj.content.filter((b) => asRecord(b)?.type === 'tool_use')
    : []

  const text = contentToText(obj.text ?? obj.content)
  const calls = toolCallsToText(obj.tool_calls ?? obj.tool_use ?? anthropicCalls)
  const body = [text, calls].filter(Boolean).join('\n')
  if (body) turns.push({ role: 'assistant', text: body })

  // Stop reasons are reported per protocol; collect them unconditionally so a
  // body that already produced a turn still shows why generation ended.
  const stop = scalarToString(obj.stop_reason)
  if (stop) extras.push({ key: 'stop_reason', value: stop })

  // OpenAI raw completion: choices[0].message
  const choices = obj.choices
  if (turns.length === 0 && Array.isArray(choices) && choices.length > 0) {
    const choice = asRecord(choices[0])
    const message = asRecord(choice?.message)
    if (message) {
      const turn = messageToTurn({ ...message, role: message.role ?? 'assistant' })
      if (turn) turns.push(turn)
    }
    const reason = scalarToString(choice?.finish_reason)
    if (reason && !finish) extras.push({ key: 'finish_reason', value: reason })
  }

  if (turns.length === 0) return null
  return { turns, extras }
}

/**
 * Decodes a body for the JSON tree, or null when it is not an object/array.
 *
 * Separate from parsePayload because the two answer different questions: a
 * payload can be perfectly good JSON and still not be a conversation (an image
 * response is `{created, data, usage}`), and that is exactly the case the tree
 * exists for. A headtail-truncated body fails to decode and falls back to raw
 * text, which is the honest rendering of "we only kept both ends".
 */
export function parseJsonBody(raw: string): unknown | null {
  const trimmed = raw.trim()
  if (!trimmed.startsWith('{') && !trimmed.startsWith('[')) return null
  try {
    const decoded: unknown = JSON.parse(trimmed)
    return decoded !== null && typeof decoded === 'object' ? decoded : null
  } catch {
    return null
  }
}

/**
 * A streamed body after decoding: the frames themselves, plus the answer they
 * add up to.
 *
 * Splitting these two matters. `parsed` answers "what did the model say",
 * which is the question the drawer exists for; `frames` answers "what did the
 * upstream actually send", which is what an operator reads when the answer
 * never assembled (an error frame mid-stream, a protocol nobody taught the
 * assembler).
 */
export interface SseBody {
  /** Decoded JSON payloads, in arrival order. */
  frames: unknown[]
  /** Lines that did not decode — a headtail cut, or a non-JSON event. */
  badFrames: number
  /** Assembled answer, or null when the frames carry no text. */
  parsed: ParsedPayload | null
}

/**
 * Decodes an SSE body, or null when the text is not one.
 *
 * Why it exists: a streaming answer reaches the drawer as `data: {…}` lines,
 * one JSON object per token delta. That is neither a conversation (several
 * objects, not one) nor a single JSON document, so both of the other views
 * decline and the drawer fell back to a wall of frames — the exact complaint
 * that produced this function.
 *
 * Frames are collected defensively: a headtail capture can cut the last line
 * mid-object, and a stream can carry non-JSON keep-alives. Those are counted,
 * not fatal.
 */
export function parseSseBody(raw: string): SseBody | null {
  const trimmed = raw.trim()
  if (!trimmed.includes('\n')) return null
  // The first line is often `event: …`, which carries the name but no payload.
  // Look at the first line that could actually hold JSON before deciding.
  const firstPayload = trimmed
    .split('\n')
    .map((line) => line.trim())
    .find((line) => line && !line.startsWith('event:') && !line.startsWith(':'))
  if (!firstPayload) return null
  if (!firstPayload.startsWith('data:') && !firstPayload.startsWith('{')) {
    return null
  }

  const frames: unknown[] = []
  let badFrames = 0
  for (const line of trimmed.split('\n')) {
    const text = line.trim()
    // SSE framing lines (event/id/comment) carry no payload of their own.
    if (!text || text.startsWith(':') || text.startsWith('event:')) continue
    const payload = text.startsWith('data:') ? text.slice(5).trim() : text
    if (!payload || payload === '[DONE]') continue
    try {
      frames.push(JSON.parse(payload))
    } catch {
      badFrames++
    }
  }
  if (frames.length === 0) return null
  return { frames, badFrames, parsed: assembleFrames(frames) }
}

/**
 * Folds stream frames into the normalised response body that `parseResponse`
 * already understands, so the bubble renderer needs no stream-specific path.
 *
 * Four wire formats reach this gateway and each puts the token somewhere
 * different: OpenAI chat in `choices[].delta.content`, the Responses API in
 * `response.output_text.delta`, Anthropic in `content_block_delta`, Gemini in
 * a chunk array. A frame nobody recognises is skipped — silent, because the
 * caller still has the frames.
 */
function assembleFrames(frames: unknown[]): ParsedPayload | null {
  let text = ''
  let finish = ''
  let model = ''
  let promptTokens: number | string = ''
  let completionTokens: number | string = ''
  let error = ''
  const tools = new Map<number, { name: string; arguments: string }>()

  const noteTokens = (usage: Record<string, unknown> | null) => {
    if (!usage) return
    const prompt = scalarToString(usage.input_tokens ?? usage.prompt_tokens)
    const completion = scalarToString(
      usage.output_tokens ?? usage.completion_tokens
    )
    if (prompt !== null) promptTokens = prompt
    if (completion !== null) completionTokens = completion
  }

  const noteTool = (
    index: number,
    fn: Record<string, unknown> | null,
    fallbackName?: unknown
  ) => {
    const acc = tools.get(index) ?? { name: '', arguments: '' }
    const name = scalarToString(fn?.name) ?? scalarToString(fallbackName)
    if (name) acc.name = name
    const args = scalarToString(fn?.arguments)
    if (args) acc.arguments += args
    tools.set(index, acc)
  }

  for (const frame of frames) {
    // Gemini emits one JSON array per chunk.
    if (Array.isArray(frame)) {
      for (const chunk of frame) {
        const candidate = asRecord(
          (asRecord(chunk)?.candidates as unknown[] | undefined)?.[0]
        )
        const parts = asRecord(candidate?.content)?.parts
        if (Array.isArray(parts)) {
          for (const part of parts) {
            const t = scalarToString(asRecord(part)?.text)
            if (t) text += t
          }
        }
        const reason = scalarToString(candidate?.finishReason)
        if (reason) finish = reason
      }
      continue
    }

    const obj = asRecord(frame)
    if (!obj) continue
    const type = scalarToString(obj.type) ?? ''

    // OpenAI Responses API.
    if (type === 'response.output_text.delta') {
      text += scalarToString(obj.delta) ?? ''
      continue
    }
    if (type === 'response.output_text.done' && !text) {
      text += scalarToString(obj.text) ?? ''
      continue
    }
    if (type.startsWith('response.')) {
      const resp = asRecord(obj.response)
      noteTokens(asRecord(resp?.usage))
      model ||= scalarToString(resp?.model) ?? ''
      // The lifecycle frames all carry a status, and they arrive in order:
      // `response.created`/`in_progress` say "in_progress", only the closing
      // `response.completed` says "completed". Taking the last one that is
      // *not* completed leaves a finished stream labelled `in_progress`.
      const status = scalarToString(resp?.status)
      if (status === 'completed') {
        finish = 'stop'
      } else if (status === 'incomplete') {
        finish =
          scalarToString(asRecord(resp?.incomplete_details)?.reason) ??
          'incomplete'
      } else if (status) {
        finish = status
      }
      if (type === 'response.failed' || type === 'response.error') {
        const err = asRecord(resp?.error) ?? asRecord(obj.error)
        error ||= scalarToString(err?.message) ?? errorTextOf(obj, resp)
      }
      continue
    }

    // Anthropic.
    if (type === 'content_block_delta') {
      const delta = asRecord(obj.delta)
      if (scalarToString(delta?.type) === 'text_delta') {
        text += scalarToString(delta?.text) ?? ''
      }
      continue
    }
    if (type === 'message_delta') {
      const delta = asRecord(obj.delta)
      const reason = scalarToString(delta?.stop_reason)
      if (reason) finish = reason
      noteTokens(asRecord(obj.usage))
      continue
    }
    if (type === 'error') {
      error ||= scalarToString(asRecord(obj.error)?.message) ?? ''
      continue
    }

    // OpenAI chat/completions.
    const choices = obj.choices
    if (Array.isArray(choices) && choices.length > 0) {
      const choice = asRecord(choices[0])
      const delta = asRecord(choice?.delta) ?? asRecord(choice?.message)
      text += scalarToString(delta?.content) ?? ''
      const reason = scalarToString(choice?.finish_reason)
      if (reason) finish = reason
      const calls = delta?.tool_calls
      if (Array.isArray(calls)) {
        for (const call of calls) {
          const c = asRecord(call)
          noteTool(Number(c?.index) || 0, asRecord(c?.function), c?.name)
        }
      }
      noteTokens(asRecord(obj.usage))
    }
  }

  if (!text && tools.size === 0) return null

  const body: Record<string, unknown> = { text }
  if (finish) body.finish_reason = finish
  if (model) body.model = model
  if (error) body.error = error
  if (tools.size > 0) {
    body.tool_calls = [...tools.entries()]
      .sort((a, b) => a[0] - b[0])
      .map(([, call]) => ({
        function: { name: call.name, arguments: call.arguments },
      }))
  }
  if (promptTokens !== '' || completionTokens !== '') {
    body.usage = {
      prompt_tokens: promptTokens === '' ? undefined : promptTokens,
      completion_tokens: completionTokens === '' ? undefined : completionTokens,
    }
  }
  return parseResponse(body)
}

/** Error text from either the nested response or the frame itself. */
function errorTextOf(
  frame: Record<string, unknown>,
  resp: Record<string, unknown> | null
): string {
  return (
    scalarToString(frame.message) ??
    scalarToString(resp?.message) ??
    'stream failed'
  )
}

/**
 * Returns null whenever the body is not a recognisable conversation — the
 * caller then shows the raw text, which is the only honest fallback for a
 * truncated payload, an SSE frame dump, or an error string.
 */
export function parsePayload(
  raw: string,
  kind: 'request' | 'response'
): ParsedPayload | null {
  const trimmed = raw.trim()
  if (!trimmed.startsWith('{') && !trimmed.startsWith('[')) return null

  let decoded: unknown
  try {
    decoded = JSON.parse(trimmed)
  } catch {
    // Truncated by headtail capture, or a stream of frames. Raw view it is.
    return null
  }

  const obj = asRecord(decoded)
  if (!obj) return null

  return kind === 'request' ? parseRequest(obj) : parseResponse(obj)
}
