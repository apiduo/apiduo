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
import { postJson } from '../lib/request'
import type { ImageGenerationResultItem } from '../types'

const IMAGE_ENDPOINT = '/pg/images/generations'

export const IMAGE_SIZES = [
  '1024x1024',
  '1024x1792',
  '1792x1024',
  '512x512',
] as const

export interface ImageRequestOptions {
  model: string
  prompt: string
  n: number
  size: string
  quality?: string
}

interface ImageApiResponse {
  created?: number
  data?: Array<{
    url?: string
    b64_json?: string
    revised_prompt?: string
  }>
}

export interface ImageApiResult {
  items: ImageGenerationResultItem[]
  requestId: string
  error?: string
}

function toDataUrl(base64: string): string {
  return base64.startsWith('data:') ? base64 : `data:image/png;base64,${base64}`
}

export async function generateImages(
  options: ImageRequestOptions
): Promise<ImageApiResult> {
  const payload: Record<string, unknown> = {
    model: options.model,
    prompt: options.prompt,
    n: options.n,
    size: options.size,
    response_format: 'b64_json',
  }
  if (options.quality) payload.quality = options.quality

  const { data, requestId, error } = await postJson<ImageApiResponse>(
    IMAGE_ENDPOINT,
    payload
  )

  if (error || !data) {
    return { items: [], requestId, error: error ?? 'Request failed' }
  }

  const items: ImageGenerationResultItem[] = []
  for (const item of data.data ?? []) {
    // 优先 url：b64 在部分渠道会被截断，url 更省内存
    if (item.url) {
      items.push({ url: item.url, revisedPrompt: item.revised_prompt })
    } else if (item.b64_json) {
      items.push({
        url: toDataUrl(item.b64_json),
        revisedPrompt: item.revised_prompt,
      })
    }
  }

  return { items, requestId }
}
