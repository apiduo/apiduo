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

import { parseTask } from '../api'

/*
 * 任务状态是「已成功」却看不到播放器，是这一层最容易回归的缺陷：
 * 网关的任务 DTO 用 `result_url`，上游（方舟）用 `content.video_url`，
 * 两套形状都得认出来，否则地址丢在解析这一步，UI 只会显示一行状态。
 */
describe('parseTask', () => {
  it('reads result_url from the gateway task DTO', () => {
    const snapshot = parseTask({
      code: 'success',
      data: {
        task_id: 'task_abc',
        status: 'SUCCESS',
        result_url: 'https://cdn.example.com/a.mp4',
      },
    })

    expect(snapshot.status).toBe('succeeded')
    expect(snapshot.url).toBe('https://cdn.example.com/a.mp4')
    expect(snapshot.taskId).toBe('task_abc')
  })

  it('reads video_url from a raw ark task object', () => {
    const snapshot = parseTask({
      id: 'cgt-1',
      status: 'succeeded',
      content: { video_url: 'https://cdn.example.com/b.mp4' },
    })

    expect(snapshot.url).toBe('https://cdn.example.com/b.mp4')
    expect(snapshot.taskId).toBe('cgt-1')
  })

  it('reads content.video_url nested inside data', () => {
    const snapshot = parseTask({
      data: {
        id: 'cgt-2',
        status: 'running',
        content: { video_url: 'https://cdn.example.com/c.mp4' },
      },
    })

    expect(snapshot.url).toBe('https://cdn.example.com/c.mp4')
    expect(snapshot.status).toBe('succeeded')
  })

  it('reads the first entry of a data array', () => {
    const snapshot = parseTask({
      data: [{ url: 'https://cdn.example.com/d.png' }],
    })

    expect(snapshot.url).toBe('https://cdn.example.com/d.png')
  })

  it('keeps a queued task queued without a url', () => {
    const snapshot = parseTask({
      data: { task_id: 'task_queued', status: 'queued' },
    })

    expect(snapshot.status).toBe('queued')
    expect(snapshot.url).toBe('')
    expect(snapshot.taskId).toBe('task_queued')
  })

  it('falls back to the id passed by the caller', () => {
    const snapshot = parseTask({ status: 'queued' }, 'local-1')

    expect(snapshot.taskId).toBe('local-1')
  })
})
