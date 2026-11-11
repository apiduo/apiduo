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

export type ModelKind = 'chat' | 'image' | 'video'

/**
 * Endpoint types are the first authority: a model that advertises
 * `image-generation` or `openai-video` knows what it is.
 *
 * In production they are not always filled in: the hailuo video models report
 * `['openai']`, the same as a chat model, so a pure endpoint filter drops every
 * video model and the mode falls back to "show everything". The name patterns
 * below are the second authority — they only ever *narrow* a model to video or
 * image, never promote it back into chat.
 */
const VIDEO_PATTERNS: RegExp[] = [
  /hailuo/i,
  /seedance/i,
  /doubao.*video/i,
  /jimeng.*video/i,
  /(^|[/._-])(sora|veo|kling|runway|pika|luma|vidu|cogvideo)([/._-]|$)/i,
  /(^|[/._-])(t2v|i2v|fl2v)([/._-]|$)/i,
]

const IMAGE_PATTERNS: RegExp[] = [
  /(^|[/._-])(flux|seedream|gpt-image|dall-e|midjourney|imagen|ideogram|recraft|kolors|nano-banana|qwen-image|stable-diffusion|sdxl)([/._-]|$)/i,
  /(^|[/._-])sd3([/._-]|$)/i,
  /image$/i,
]

export function isVideoModelName(name: string): boolean {
  return VIDEO_PATTERNS.some((pattern) => pattern.test(name))
}

export function isImageModelName(name: string): boolean {
  return IMAGE_PATTERNS.some((pattern) => pattern.test(name))
}

export function modelKind(name: string, endpointTypes: string[]): ModelKind {
  if (endpointTypes.includes('image-generation')) return 'image'
  if (endpointTypes.includes('openai-video')) return 'video'
  if (isVideoModelName(name)) return 'video'
  if (isImageModelName(name)) return 'image'
  return 'chat'
}
