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

/**
 * What an image model can be asked for.
 *
 * The console does not ship a per-model parameter schema yet — the MCP
 * registry has one (`parameters.aspect_ratio` / `.resolution`) but it is not
 * exposed to the playground, so this table is a name-pattern stand-in. It is
 * deliberately conservative in one direction only: an unknown model is given
 * the full ratio matrix, because a DiT model that rejects a size answers with
 * a clear upstream error, while a picker that hides every ratio but 1:1 makes
 * the feature look broken for models that would have accepted them.
 *
 * Sizes are derived from an *area class* plus the ratio, not stored per
 * combination: 1K is 1024×1024 at 1:1 and 1360×768 at 16:9, which is how the
 * upstreams actually behave. Snapping to 16 px keeps the numbers legible and
 * off odd widths that some providers round anyway.
 */

export interface RatioSpec {
  id: string
  ar: number
  section: 'land' | 'sq' | 'port'
  /** `auto` means "let the model pick"; no size is sent. */
  auto?: boolean
}

export const RATIOS: readonly RatioSpec[] = [
  { id: 'auto', ar: 1, section: 'sq', auto: true },
  { id: '21:9', ar: 21 / 9, section: 'land' },
  { id: '2:1', ar: 2, section: 'land' },
  { id: '16:9', ar: 16 / 9, section: 'land' },
  { id: '3:2', ar: 1.5, section: 'land' },
  { id: '4:3', ar: 4 / 3, section: 'land' },
  { id: '5:4', ar: 1.25, section: 'land' },
  { id: '1:1', ar: 1, section: 'sq' },
  { id: '4:5', ar: 0.8, section: 'port' },
  { id: '3:4', ar: 0.75, section: 'port' },
  { id: '2:3', ar: 2 / 3, section: 'port' },
  { id: '9:16', ar: 9 / 16, section: 'port' },
  { id: '1:2', ar: 0.5, section: 'port' },
  { id: '9:21', ar: 9 / 21, section: 'port' },
] as const

export const RATIO_SECTIONS: ReadonlyArray<['land' | 'sq' | 'port', string]> = [
  ['land', 'Landscape'],
  ['sq', 'Square'],
  ['port', 'Portrait'],
]

export interface ResolutionSpec {
  id: string
  area: number
  label: string
  note: string
}

export const RESOLUTIONS: readonly ResolutionSpec[] = [
  { id: '768', area: 768 * 768, label: '768', note: 'Draft' },
  { id: '1024', area: 1024 * 1024, label: '1K', note: 'Standard' },
  { id: '1536', area: 1536 * 1536, label: '1.5K', note: 'High' },
  { id: '2048', area: 2048 * 2048, label: '2K', note: 'Upscaled' },
] as const

export function ratioOf(id: string): RatioSpec {
  return RATIOS.find((item) => item.id === id) ?? RATIOS[7]
}

export function resolutionOf(id: string): ResolutionSpec {
  return RESOLUTIONS.find((item) => item.id === id) ?? RESOLUTIONS[1]
}

const snap = (value: number) => Math.max(16, Math.round(value / 16) * 16)

/**
 * Pixel size for a (ratio, resolution) pair. Returns `undefined` for `auto`,
 * where sending a size at all would override the model's own choice.
 */
export function sizeFor(ratioId: string, resolutionId: string): string | undefined {
  const ratio = ratioOf(ratioId)
  if (ratio.auto) return undefined
  const { area } = resolutionOf(resolutionId)
  const width = snap(Math.sqrt(area * ratio.ar))
  const height = snap(Math.sqrt(area / ratio.ar))
  return `${width}x${height}`
}

export interface ModelImageCapability {
  ratios: string[]
  resolutions: string[]
  maxCount: number
  /**
   * 参考图容量，0 = 该模型没有已知的编辑入口。与 ratios 同一哲学：只声明
   * 有把握的家族，未知家族给 0（不显示入口），因为一个不收参考图的渠道
   * 对 multipart 的回答是上游报错，而一个被静默丢弃参考图的渠道会假装
   * 成功——后者才是不能给的。
   */
  maxRefs: number
}

const ALL_RATIOS = RATIOS.map((item) => item.id)
const ALL_RES = RESOLUTIONS.map((item) => item.id)

/**
 * Families whose size argument is an enum, not a free pair. Sending anything
 * outside it is rejected upstream, so the picker must not offer it.
 */
const FIXED: ReadonlyArray<{
  match: RegExp
  ratios: string[]
  resolutions?: string[]
  maxCount?: number
  /** 编辑入口的参考图容量；缺省 0（不显示参考图入口） */
  maxRefs?: number
}> = [
  // DALL·E 3 takes exactly three shapes and one size, and n is always 1.
  // It has no edits endpoint that takes a reference at all.
  {
    match: /dall-e-3/i,
    ratios: ['1:1', '3:2', '2:3'],
    resolutions: ['1024'],
    maxCount: 1,
  },
  {
    match: /gpt-image-1/i,
    ratios: ['1:1', '3:2', '2:3'],
    resolutions: ['1024'],
    maxRefs: 16,
  },
  // DALL·E 2 is the one model whose edits endpoint takes exactly one square
  // PNG — the original /v1/images/edits contract.
  {
    match: /dall-e-2/i,
    ratios: ['1:1'],
    resolutions: ['1024'],
    maxCount: 1,
    maxRefs: 1,
  },
  // SD 1.5/XL inherit the 1024-ish buckets their LoRAs were trained on.
  {
    match: /(^|\/)sd[-_.]?(1\.5|xl|3)/i,
    ratios: ['1:1', '3:2', '2:3', '16:9', '9:16'],
    resolutions: ['768', '1024'],
  },
]

/**
 * 参考图容量按家族给，不按渠道给：同一家族的编辑契约是模型定的，不是渠道
 * 定的。数字与各家文档对齐——gpt-image-1 十六张、FLUX 3 十张（与 MCP 注册表
 * 的 max_ref_images 一致）、Seedream 十张（Ark 图生图上限）。没写进表里的
 * 家族一律 0：宁缺入口，不给一个会被静默丢弃参考图的入口。
 */
const REF_LIMITS: ReadonlyArray<{ match: RegExp; max: number }> = [
  { match: /(?:^|[/._-])flux(?:[/._-]|$)/i, max: 10 },
  { match: /seedream/i, max: 10 },
]

export function capabilityFor(model: string): ModelImageCapability {
  const fixed = FIXED.find((entry) => entry.match.test(model))
  if (fixed) {
    return {
      ratios: fixed.ratios,
      resolutions: fixed.resolutions ?? ['1024'],
      maxCount: fixed.maxCount ?? 4,
      maxRefs: fixed.maxRefs ?? refLimitOf(model),
    }
  }
  return {
    ratios: ALL_RATIOS,
    resolutions: ALL_RES,
    maxCount: 4,
    maxRefs: refLimitOf(model),
  }
}

function refLimitOf(model: string): number {
  return REF_LIMITS.find((entry) => entry.match.test(model))?.max ?? 0
}
