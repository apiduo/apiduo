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

/**
 * 复制给用户的是**对外正式 API**，不是 Playground 自己的会话接口。
 * `/pg` 依赖浏览器会话，粘到脚本里跑不通；`/v1` 用 Bearer 令牌，
 * 分组由令牌决定，所以这里不传 group。
 */
export function buildOfficialCurl(
  mode: PlaygroundMode,
  modelName: string,
  origin: string
): { path: string; body: string; curl: string } {
  if (mode === 'image') {
    const path = '/v1/images/generations'
    const payload = { model: modelName, prompt: 'a cat', n: 1, size: '1024x1024' }
    return {
      path,
      body: JSON.stringify(payload, null, 2),
      curl: `curl ${origin}${path} \\\n  -H "Authorization: Bearer sk-your-token" \\\n  -H "Content-Type: application/json" \\\n  -d '${JSON.stringify(payload)}'`,
    }
  }

  if (mode === 'video') {
    const path = '/v1/video/generations'
    const payload = { model: modelName, prompt: 'a cat walking', duration: 5 }
    return {
      path,
      body: JSON.stringify(payload, null, 2),
      curl: `curl ${origin}${path} \\\n  -H "Authorization: Bearer sk-your-token" \\\n  -H "Content-Type: application/json" \\\n  -d '${JSON.stringify(payload)}'`,
    }
  }

  const path = '/v1/chat/completions'
  const payload = {
    model: modelName,
    messages: [{ role: 'user', content: 'hi' }],
    stream: true,
    stream_options: { include_usage: true },
  }
  return {
    path,
    body: JSON.stringify(payload, null, 2),
    curl: `curl ${origin}${path} \\\n  -H "Authorization: Bearer sk-your-token" \\\n  -H "Content-Type: application/json" \\\n  -d '${JSON.stringify(payload)}'`,
  }
}
