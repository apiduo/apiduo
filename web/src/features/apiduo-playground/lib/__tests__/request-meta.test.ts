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

import { buildRequestCurl } from '../request-meta'

/*
 * 复制出来的 cURL 必须是「粘到脚本里能跑」的那一条。
 * 视频改走方舟之后，这一层最容易滞后：形状还是 OpenAI video，
 * 端点却只认 content[]，用户复制出去只会拿到「未知参数」。
 */
describe('buildRequestCurl', () => {
  it('prints the ark task shape for video', () => {
    const built = buildRequestCurl(
      {
        prompt: 'slow drift',
        model: 'hailuo-2.3',
        params: { seconds: 6, size: '1280x720', firstFrame: 1, lastFrame: 0 },
      },
      'video',
      'hailuo-2.3',
      'https://apiduo.cn'
    )

    expect(built.path).toBe('/v1/contents/generations/tasks')
    expect(built.curl).toContain('/v1/contents/generations/tasks')

    const body = JSON.parse(built.body) as Record<string, unknown>
    expect(body.duration).toBe(6)
    expect(body.ratio).toBe('16:9')
    expect(body.resolution).toBe('768p')
    expect(body.prompt).toBeUndefined()

    const content = body.content as Array<Record<string, unknown>>
    expect(content[0]).toMatchObject({ type: 'text', text: 'slow drift' })
    expect(content[1]).toMatchObject({ type: 'image_url', role: 'first_frame' })
    expect(content).toHaveLength(2)
  })

  it('maps a portrait preset to 9:16 and adds both frame roles', () => {
    const built = buildRequestCurl(
      {
        prompt: 'turn',
        params: { seconds: 10, size: '720x1280', firstFrame: 1, lastFrame: 1 },
      },
      'video',
      'hailuo-2.3',
      'https://apiduo.cn'
    )

    const body = JSON.parse(built.body) as Record<string, any>
    expect(body.ratio).toBe('9:16')
    expect(body.content.map((item: any) => item.role)).toEqual([
      undefined,
      'first_frame',
      'last_frame',
    ])
  })

  it('keeps image edits on multipart with the reference count', () => {
    const built = buildRequestCurl(
      { prompt: 'add border', params: { size: '1024x1024', n: 1 }, refs: 2 },
      'image',
      'flux-3-image',
      'https://apiduo.cn'
    )

    expect(built.path).toBe('/v1/images/edits')
    expect(built.curl).toContain('-F "image[]=@ref-1.png"')
    expect(built.curl).toContain('-F "image[]=@ref-2.png"')
  })
})
