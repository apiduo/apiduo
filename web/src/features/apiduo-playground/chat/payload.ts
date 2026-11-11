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
import { isValidMessage } from '@/features/playground/lib'
import type {
  ChatCompletionRequest,
  ContentPart,
  Message,
  ParameterEnabled,
  PlaygroundConfig,
} from '@/features/playground/types'

/** message key → attachments, in the order they were attached. */
export type ChatAttachment = { id: string; url: string }
export type ChatImages = Record<string, ChatAttachment[]>

/**
 * Same request the shared builder emits, except a user turn that carries
 * pictures becomes an OpenAI content array: text first, then one `image_url`
 * part per attachment.
 *
 * The images live beside the messages instead of inside them on purpose: the
 * playground persists messages to localStorage through a zod schema, and a few
 * base64 pictures would blow that quota. Keeping them in memory means history
 * reloads without thumbnails, which is the honest portable shape.
 */
export function buildChatPayload(
  messages: Message[],
  config: PlaygroundConfig,
  parameterEnabled: ParameterEnabled,
  images: ChatImages
): ChatCompletionRequest {
  const processed = messages.filter(isValidMessage).map((message) => {
    const version = message.versions[0]
    const content = version?.content ?? ''
    const attached = message.from === 'user' ? (images[message.key] ?? []) : []
    if (attached.length === 0) return { role: message.from, content }

    const parts: ContentPart[] = []
    if (content.trim()) parts.push({ type: 'text', text: content })
    for (const item of attached) {
      parts.push({ type: 'image_url', image_url: { url: item.url } })
    }
    return { role: message.from, content: parts }
  })

  const payload: ChatCompletionRequest = {
    model: config.model,
    group: config.group,
    messages: processed,
    stream: config.stream,
  }

  if (parameterEnabled.temperature) payload.temperature = config.temperature
  if (parameterEnabled.top_p) payload.top_p = config.top_p
  if (parameterEnabled.max_tokens) payload.max_tokens = config.max_tokens
  if (parameterEnabled.frequency_penalty) {
    payload.frequency_penalty = config.frequency_penalty
  }
  if (parameterEnabled.presence_penalty) {
    payload.presence_penalty = config.presence_penalty
  }
  if (parameterEnabled.seed && config.seed !== null) {
    payload.seed = config.seed
  }

  return payload
}
