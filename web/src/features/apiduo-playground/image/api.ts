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
import { postForm, postJson } from '../lib/request'
import type { ImageGenerationResultItem } from '../types'

const IMAGE_ENDPOINT = '/pg/images/generations'
const IMAGE_EDIT_ENDPOINT = '/pg/images/edits'

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

export interface ImageEditRequestOptions extends ImageRequestOptions {
  /** 参考图文件；1 张走 `image`，多张走 `image[]`（与 OpenAI 一致） */
  files: File[]
}

/**
 * 参考图走编辑入口：multipart 与对外 /v1/images/edits 同构。响应形状与
 * generations 相同，所以解析复用同一段。
 */
export async function editImages(
  options: ImageEditRequestOptions
): Promise<ImageApiResult> {
  const form = new FormData()
  form.append('model', options.model)
  form.append('prompt', options.prompt)
  form.append('n', String(options.n))
  if (options.size) form.append('size', options.size)
  if (options.quality) form.append('quality', options.quality)
  const field = options.files.length > 1 ? 'image[]' : 'image'
  for (const file of options.files) form.append(field, file, file.name)

  const { data, requestId, error } = await postForm<ImageApiResponse>(
    IMAGE_EDIT_ENDPOINT,
    form
  )
  if (error || !data) {
    return { items: [], requestId, error: error ?? 'Request failed' }
  }
  return { items: parseImageItems(data), requestId }
}

function parseImageItems(data: ImageApiResponse): ImageGenerationResultItem[] {
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
  return items
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

  return { items: parseImageItems(data), requestId }
}
