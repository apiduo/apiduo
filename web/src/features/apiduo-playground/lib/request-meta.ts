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
import type { PlaygroundMode } from '../types'

/** What a single run actually asked for. Built by the workspace at call time. */
export interface RequestMeta {
  prompt: string
  /** 这一笔用的模型；缺省用当前选中的模型。 */
  model?: string
  /** How many reference pictures the request carried (image edits, chat). */
  refs?: number
  /** Extra knobs in the order they should be printed. */
  params?: Record<string, string | number>
}

export interface BuiltCurl {
  path: string
  body: string
  curl: string
}

const TOKEN = 'sk-your-token'

/**
 * 复制给用户的是**对外正式 API**，不是 Playground 自己的会话接口。
 * `/pg` 依赖浏览器会话，粘到脚本里跑不通；`/v1` 用 Bearer 令牌。
 *
 * `meta` 来自这一笔运行；没有（刷新后、历史消息）就回落到一个能跑的示例，
 * 按钮至少不会复制出一段空 curl。
 */
export function buildRequestCurl(
  meta: RequestMeta | null | undefined,
  mode: PlaygroundMode,
  modelName: string,
  origin: string
): BuiltCurl {
  if (mode === 'image') {
    const model = meta?.model || modelName
    const prompt = meta?.prompt || 'a cat'
    const size = String(meta?.params?.size ?? '1024x1024')
    const n = Number(meta?.params?.n ?? 1)
    const refs = meta?.refs ?? 0

    if (refs > 0) {
      const files = Array.from(
        { length: refs },
        (_, index) => `  -F "image[]=@ref-${index + 1}.png"`
      ).join(' \\\n')
      return {
        path: '/v1/images/edits',
        body: [
          `model=${model}`,
          `prompt=${prompt}`,
          `n=${n}`,
          `size=${size}`,
          `image[]=@ref-1.png … (${refs} files)`,
        ].join('\n'),
        curl: `curl ${origin}/v1/images/edits \\
  -H "Authorization: Bearer ${TOKEN}" \\
  -F "model=${model}" \\
  -F "prompt=${prompt}" \\
  -F "n=${n}" \\
  -F "size=${size}" \\
${files}`,
      }
    }

    const payload = { model, prompt, n, size }
    return {
      path: '/v1/images/generations',
      body: JSON.stringify(payload, null, 2),
      curl: `curl ${origin}/v1/images/generations \\
  -H "Authorization: Bearer ${TOKEN}" \\
  -H "Content-Type: application/json" \\
  -d '${JSON.stringify(payload)}'`,
    }
  }

  if (mode === 'video') {
    const payload: Record<string, unknown> = {
      model: meta?.model || modelName,
      prompt: meta?.prompt || 'a cat walking',
      seconds: String(meta?.params?.seconds ?? '5'),
      size: String(meta?.params?.size ?? '1280x720'),
    }
    const frames = Number(meta?.params?.frames ?? 0)
    if (frames > 0) {
      // 公开形状里首尾帧是 metadata 的两个字段；这里用占位 URL，
      // 因为 curl 不可能带着浏览器里的 data URL。
      payload.metadata = {
        ...(frames >= 1
          ? { first_frame_image: 'https://your-host/first-frame.png' }
          : {}),
        ...(frames >= 2
          ? { last_frame_image: 'https://your-host/last-frame.png' }
          : {}),
      }
    }
    return {
      path: '/v1/video/generations',
      body: JSON.stringify(payload, null, 2),
      curl: `curl ${origin}/v1/video/generations \\
  -H "Authorization: Bearer ${TOKEN}" \\
  -H "Content-Type: application/json" \\
  -d '${JSON.stringify(payload)}'`,
    }
  }

  const refs = meta?.refs ?? 0
  const text = meta?.prompt || 'hi'
  const content =
    refs > 0
      ? [
          { type: 'text', text },
          ...Array.from({ length: refs }, (_, index) => ({
            type: 'image_url',
            image_url: { url: `https://your-host/image-${index + 1}.png` },
          })),
        ]
      : text
  const payload: Record<string, unknown> = {
    model: meta?.model || modelName,
    messages: [{ role: 'user', content }],
    stream: true,
    stream_options: { include_usage: true },
  }
  const temperature = meta?.params?.temperature
  if (typeof temperature === 'number') payload.temperature = temperature

  return {
    path: '/v1/chat/completions',
    body: JSON.stringify(payload, null, 2),
    curl: `curl ${origin}/v1/chat/completions \\
  -H "Authorization: Bearer ${TOKEN}" \\
  -H "Content-Type: application/json" \\
  -d '${JSON.stringify(payload)}'`,
  }
}
