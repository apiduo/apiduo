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
import { getJson, postJson } from '../lib/request'
import type { VideoTaskStatus } from '../types'

const VIDEO_ENDPOINT = '/pg/video/generations'

/**
 * hailuo 2.3 只接受 6 秒和 10 秒（`plugins/tasks/hailuo/plugin.js` 的 usage
 * schema 与上游一致）。传 5 会被上游判 `code=2400050` 并让任务失败 ——
 * 而受理是成功的，所以前端只看到一条「upstream reported failure」。
 */
export const VIDEO_DURATIONS = [6, 10] as const

/**
 * 面板按横/竖屏给两个入口，但上游（方舟任务协议）要的是**档位名 + 比例**：
 * `resolution: "768p"` + `ratio: "16:9"`。换算放在下面的预设里，用户不需要
 * 知道上游怎么拼。
 *
 * 1080p 暂不放出来：hailuo 2.3 支持，但多一档会让参数行在窄屏上折行。
 */
export const VIDEO_RESOLUTIONS = ['1280x720', '720x1280'] as const

type ResolutionPreset = { ratio: string; resolution: string }

const RESOLUTION_PRESETS: Record<string, ResolutionPreset> = {
  '1280x720': { ratio: '16:9', resolution: '768p' },
  '720x1280': { ratio: '9:16', resolution: '768p' },
}

export interface VideoRequestOptions {
  model: string
  prompt: string
  duration: number
  resolution: string
  /** 首帧/尾帧：data URL（上游只认 URL 或 Base64，data URL 就是后者）。 */
  firstFrame?: string
  lastFrame?: string
}

/** 浏览器里把文件读成 data URL：链路接受 Base64，不需要先上传。 */
export function fileToDataUrl(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.addEventListener('load', () =>
      resolve(String(reader.result ?? ''))
    )
    reader.addEventListener('error', () =>
      reject(reader.error ?? new Error('read failed'))
    )
    reader.readAsDataURL(file)
  })
}

export interface VideoTaskSnapshot {
  taskId: string
  status: VideoTaskStatus
  progress: number
  url: string
  error: string
  requestId: string
}

/** 上游把进度写成 "10%"，直接 Number() 会得到 NaN，进度条就永远停在 0 */
function parseProgress(value: unknown): number {
  if (typeof value === 'number') return value
  if (typeof value === 'string') {
    const num = Number(value.replace('%', '').trim())
    return Number.isFinite(num) ? num : 0
  }
  return 0
}

function pickString(
  source: Record<string, unknown>,
  keys: string[]
): string {
  for (const key of keys) {
    const value = source[key]
    if (typeof value === 'string' && value) return value
  }
  return ''
}

function normalizeStatus(raw: string): VideoTaskStatus {
  const value = raw.toLowerCase()
  if (value.includes('fail') || value.includes('error') || value.includes('cancel')) {
    return 'failed'
  }
  if (value.includes('succeed') || value.includes('complet') || value === 'success') {
    return 'succeeded'
  }
  if (value.includes('run') || value.includes('process') || value.includes('progress')) {
    return 'running'
  }
  // NOT_START / QUEUED / PENDING 都还在排队
  return 'queued'
}

/**
 * 异步任务的响应格式在各家渠道之间没有统一，这里只认几个稳定字段：
 * id/task_id、status、progress、url。任一缺失都不影响其余字段的解析。
 */
function parseTask(raw: unknown, fallbackId = ''): VideoTaskSnapshot {
  const root = (raw && typeof raw === 'object' ? raw : {}) as Record<
    string,
    unknown
  >
  const dataNode =
    root.data && typeof root.data === 'object' && !Array.isArray(root.data)
      ? (root.data as Record<string, unknown>)
      : root

  const taskId = pickString(root, ['id', 'task_id']) || pickString(dataNode, ['id', 'task_id']) || fallbackId
  const status = normalizeStatus(
    pickString(root, ['status', 'state']) || pickString(dataNode, ['status', 'state'])
  )

  let url = pickString(root, ['video_url', 'url']) || pickString(dataNode, ['video_url', 'url'])
  if (!url) {
    const list = Array.isArray(root.data) ? root.data : []
    const first = list[0]
    if (first && typeof first === 'object') {
      url = pickString(first as Record<string, unknown>, ['url', 'video_url'])
    }
  }
  if (!url) {
    const content = root.content
    if (content && typeof content === 'object') {
      url = pickString(content as Record<string, unknown>, ['video_url', 'url'])
    }
  }

  const progress = Math.max(
    0,
    Math.min(100, parseProgress(root.progress) || parseProgress(dataNode.progress))
  )

  return {
    taskId,
    status: url ? 'succeeded' : status,
    progress: progress || (status === 'succeeded' ? 100 : 0),
    url,
    error:
      pickString(root, ['error', 'fail_reason', 'message']) ||
      pickString(dataNode, ['error', 'fail_reason', 'message']),
    requestId: '',
  }
}

export async function createVideoTask(
  options: VideoRequestOptions
): Promise<{ snapshot: VideoTaskSnapshot | null; error?: string }> {
  /* 视频走**方舟任务协议**（渠道 18 的 advanced_async.protocol = "ark"）：
     文本与首尾帧都在 `content` 数组里，帧靠 role 区分，参数是 duration /
     ratio / resolution 三个顶层标量。任务回显同样是方舟形状
     （`{id, status, content:{video_url}}`），parseTask 已经认。
     首帧/尾帧传 data URL：链路接受 Base64，不需要先上传。 */
  const content: Array<Record<string, unknown>> = [
    { type: 'text', text: options.prompt },
  ]
  if (options.firstFrame) {
    content.push({
      type: 'image_url',
      image_url: { url: options.firstFrame },
      role: 'first_frame',
    })
  }
  if (options.lastFrame) {
    content.push({
      type: 'image_url',
      image_url: { url: options.lastFrame },
      role: 'last_frame',
    })
  }

  const preset =
    RESOLUTION_PRESETS[options.resolution] ?? RESOLUTION_PRESETS['1280x720']
  const { data, requestId, error } = await postJson<Record<string, unknown>>(
    VIDEO_ENDPOINT,
    {
      model: options.model,
      content,
      duration: options.duration,
      ratio: preset.ratio,
      resolution: preset.resolution,
    }
  )

  if (error || !data) {
    return { snapshot: null, error: error ?? 'Request failed' }
  }

  const snapshot = parseTask(data)
  return { snapshot: { ...snapshot, requestId } }
}

export async function fetchVideoTask(
  taskId: string
): Promise<VideoTaskSnapshot | null> {
  if (!taskId) return null
  const { data, error } = await getJson<Record<string, unknown>>(
    `${VIDEO_ENDPOINT}/${encodeURIComponent(taskId)}`
  )
  if (error || !data) return null
  return parseTask(data, taskId)
}
