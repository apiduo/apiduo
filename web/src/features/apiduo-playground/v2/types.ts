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

export interface RunImage {
  url: string
  revisedPrompt?: string
}

/**
 * One shot at the model, from the press to the settlement.
 *
 * The prompt is kept on the run, not only in the composer: "reuse" and
 * "retry" both need to reproduce the request that produced *this* picture,
 * and the composer has usually moved on by then.
 */
export interface StageRun {
  id: string
  createdAt: number
  prompt: string
  model: string
  ratio: string
  resolution: string
  size?: string
  count: number
  items: RunImage[]
  status: 'pending' | 'done' | 'error'
  error?: string
  /** 这次请求带的参考图字节。只在内存里，重试时按它重放同一个请求。 */
  refFiles?: File[]
}

/**
 * 一张参考图。`url` 是 object URL，归 composer 渲染、归持有方撤销：
 * 缩略图 46px 只是文件的预览，真正的字节始终在 `file` 里，提交时才进 multipart。
 */
export interface RefImage {
  id: string
  file: File
  url: string
}

export type AmbientFamily = 'g0' | 'g1' | 'g2' | 'g3'

/** Deterministic per run, so the same picture keeps the same light. */
export function runSeed(run: Pick<StageRun, 'id'>): number {
  const digits = run.id.replaceAll(/\D/g, '')
  return Number.parseInt(digits.slice(-6), 10) || 0
}

export function ambientOf(runs: StageRun[]): AmbientFamily {
  const done = runs.filter((run) => run.status === 'done')
  const last = done.at(-1)
  if (!last) return 'g3'
  return (['g0', 'g1', 'g2'] as const)[runSeed(last) % 3]
}

const GLOW = [
  'oklch(0.70 0.12 192)',
  'oklch(0.80 0.105 76)',
  'oklch(0.70 0.065 58)',
] as const

export function glowOf(run: StageRun): string {
  return GLOW[runSeed(run) % GLOW.length]
}
