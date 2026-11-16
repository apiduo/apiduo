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
export type SemanticColor =
  | 'blue'
  | 'green'
  | 'cyan'
  | 'purple'
  | 'pink'
  | 'red'
  | 'orange'
  | 'amber'
  | 'yellow'
  | 'lime'
  | 'light-green'
  | 'teal'
  | 'light-blue'
  | 'indigo'
  | 'violet'
  | 'grey'
  | 'slate'

export const colorToBgClass: Record<SemanticColor, string> = {
  blue: 'bg-blue-500',
  green: 'bg-green-500',
  cyan: 'bg-cyan-500',
  purple: 'bg-purple-500',
  pink: 'bg-pink-500',
  red: 'bg-red-500',
  orange: 'bg-orange-500',
  amber: 'bg-amber-500',
  yellow: 'bg-yellow-500',
  lime: 'bg-lime-500',
  'light-green': 'bg-green-400',
  teal: 'bg-teal-500',
  'light-blue': 'bg-sky-400',
  indigo: 'bg-indigo-500',
  violet: 'bg-violet-500',
  grey: 'bg-gray-400',
  slate: 'bg-slate-500',
}

export const avatarColorMap: Record<SemanticColor, string> = {
  blue: 'bg-chart-1/10 text-chart-1',
  green: 'bg-success/10 text-success',
  cyan: 'bg-chart-2/10 text-chart-2',
  purple: 'bg-chart-4/10 text-chart-4',
  pink: 'bg-chart-5/10 text-chart-5',
  red: 'bg-destructive/10 text-destructive',
  orange: 'bg-warning/10 text-warning',
  amber: 'bg-warning/10 text-warning',
  yellow: 'bg-warning/10 text-warning',
  lime: 'bg-chart-3/10 text-chart-3',
  'light-green': 'bg-success/10 text-success',
  teal: 'bg-chart-2/10 text-chart-2',
  'light-blue': 'bg-info/10 text-info',
  indigo: 'bg-chart-1/10 text-chart-1',
  violet: 'bg-chart-4/10 text-chart-4',
  grey: 'bg-muted text-muted-foreground',
  slate: 'bg-muted text-muted-foreground',
}

export function getAvatarColorClass(name: string): string {
  return avatarColorMap[stringToColor(name)]
}

export function getBgColorClass(color?: string): string {
  if (!color) return colorToBgClass.blue
  return (
    (colorToBgClass as Record<string, string>)[color] || colorToBgClass.blue
  )
}

/**
 * Chart color palette - Modern gradient colors compatible with light/dark themes
 * Uses HSL format for better theme adaptation
 */
export const CHART_COLORS = [
  'hsl(217, 91%, 60%)', // blue
  'hsl(142, 76%, 36%)', // green
  'hsl(38, 92%, 50%)', // amber
  'hsl(258, 90%, 66%)', // violet
  'hsl(330, 81%, 60%)', // pink
  'hsl(189, 94%, 43%)', // cyan
  'hsl(25, 95%, 53%)', // orange
  'hsl(239, 84%, 67%)', // indigo
  'hsl(173, 80%, 40%)', // teal
  'hsl(271, 91%, 65%)', // purple
  'hsl(199, 89%, 48%)', // sky
  'hsl(280, 65%, 60%)', // fuchsia
] as const

/**
 * Get a chart color by index (cycles through the palette)
 */
export function getChartColor(index: number): string {
  return CHART_COLORS[index % CHART_COLORS.length]
}

/**
 * Announcement status types
 */
export type AnnouncementType =
  | 'default'
  | 'ongoing'
  | 'success'
  | 'warning'
  | 'error'

/**
 * Announcement status color mapping
 */
export const ANNOUNCEMENT_TYPE_COLORS: Record<AnnouncementType, string> = {
  default: 'bg-neutral',
  ongoing: 'bg-info',
  success: 'bg-success',
  warning: 'bg-warning',
  error: 'bg-destructive',
}

/**
 * Get announcement status color class
 */
export function getAnnouncementColorClass(type?: string): string {
  const validType = (type || 'default') as AnnouncementType
  return ANNOUNCEMENT_TYPE_COLORS[validType] || ANNOUNCEMENT_TYPE_COLORS.default
}

/**
 * 铃铛圆点专用色。
 *
 * 6px 实心色块比文字更吃明度：语义 token 为了让白底文字达标，明度压到 0.58 上下，
 * 画成小圆点会发闷。这里统一提亮 0.04–0.06。**只用于实心色块**，
 * 级别文字走 ANNOUNCEMENT_TEXT_COLORS。
 */
export const ANNOUNCEMENT_DOT_COLORS: Record<AnnouncementType, string> = {
  default: 'oklch(0.62 0 0)',
  ongoing: 'oklch(0.67 0.15 243)',
  success: 'oklch(0.66 0.15 163)',
  warning: 'oklch(0.72 0.165 76)',
  error: 'oklch(0.64 0.22 27)',
}

/**
 * 公告级别 → 徽标文案与语义变体。
 *
 * 详情弹窗用 `variant` 渲染 StatusBadge，列表只取 `label` 作为 i18n key。
 * 一处定义，避免两处写出不同措辞。
 */
/**
 * 功能更新的变更类型配色。
 *
 * 与公告的 ANNOUNCEMENT_DOT_COLORS 是两套：那套表达严重程度（警告/错误），
 * 这套表达变更性质（新增/优化/修复）。同一个颜色在两个维度上可以复用 ——
 * 蓝色既表示「进行中」也表示「新增」，因为读者看到的是不同位置的圆点。
 */
export const CHANGELOG_KIND_COLORS: Record<
  'feature' | 'improvement' | 'fix',
  string
> = {
  feature: 'oklch(0.67 0.15 243)',
  improvement: 'oklch(0.66 0.15 163)',
  fix: 'oklch(0.62 0 0)',
}

export const ANNOUNCEMENT_LEVEL_BADGES: Record<
  string,
  { label: string; variant: 'neutral' | 'info' | 'success' | 'warning' | 'danger' }
> = {
  default: { label: 'Default', variant: 'neutral' },
  ongoing: { label: 'Ongoing', variant: 'info' },
  success: { label: 'Success', variant: 'success' },
  warning: { label: 'Warning', variant: 'warning' },
  error: { label: 'Error', variant: 'danger' },
}

/**
 * 级别文字色。
 *
 * 统一朝向 foreground 混色：亮色主题下 foreground 是深色 ⇒ 变深，暗色主题下是浅色
 * ⇒ 变亮。一份表达式同时满足两套主题，不需要 dark: 变体。
 *
 * 混色比例按各级别原本的对比度反推 —— warning 琥珀天生最弱，混到 74%；
 * error 红本身够深，只混 10% 以保留警示强度。
 * 实测（截图像素采样，11.5px 小字）：亮色最低 5.15:1，暗色最低 5.25:1，均过 AA。
 */
export const ANNOUNCEMENT_TEXT_COLORS: Record<AnnouncementType, string> = {
  default: 'var(--muted-foreground)',
  ongoing: 'color-mix(in oklab, var(--info) 78%, var(--foreground))',
  success: 'color-mix(in oklab, var(--success) 80%, var(--foreground))',
  warning: 'color-mix(in oklab, var(--warning) 74%, var(--foreground))',
  error: 'color-mix(in oklab, var(--destructive) 90%, var(--foreground))',
}

/**
 * Semantic colors for tags and badges
 */
const TAG_COLORS = [
  'amber',
  'blue',
  'cyan',
  'green',
  'grey',
  'indigo',
  'light-blue',
  'lime',
  'orange',
  'pink',
  'purple',
  'red',
  'teal',
  'violet',
  'yellow',
] as const

/**
 * Convert string to a stable semantic color
 * Used for model tags, group badges, user avatars, etc.
 * Same string always returns the same color
 *
 * @param str - Input string (model name, group name, username, etc.)
 * @returns Semantic color name from TAG_COLORS
 *
 * @example
 * stringToColor('gpt-4') // 'blue'
 * stringToColor('claude-3') // 'purple'
 * stringToColor('default') // 'green'
 */
export function stringToColor(str: string): SemanticColor {
  let sum = 0
  for (let i = 0; i < str.length; i++) {
    sum += str.charCodeAt(i)
  }
  const index = sum % TAG_COLORS.length
  return TAG_COLORS[index]
}
