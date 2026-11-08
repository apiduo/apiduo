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

export const VIDEO_DURATIONS = [5, 10] as const
export const VIDEO_RESOLUTIONS = ['720p', '1080p'] as const

export interface VideoRequestOptions {
  model: string
  prompt: string
  duration: number
  resolution: string
}

export interface VideoTaskSnapshot {
  taskId: string
  status: VideoTaskStatus
  progress: number
  url: string
  error: string
  requestId: string
}

function toNumber(value: unknown): number {
  const num = typeof value === 'number' ? value : Number(value)
  return Number.isFinite(num) ? num : 0
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
    Math.min(100, toNumber(root.progress) || toNumber(dataNode.progress))
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
  const { data, requestId, error } = await postJson<Record<string, unknown>>(
    VIDEO_ENDPOINT,
    {
      model: options.model,
      prompt: options.prompt,
      duration: options.duration,
      size: options.resolution,
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
