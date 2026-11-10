import {
  type LucideIcon,
  Activity,
  Database,
  LayoutTemplate,
  PanelsTopLeft,
  Plug,
  Sparkles,
} from 'lucide-react'

/**
 * Dashboard section tabs that apiduo added on top of upstream new-api.
 *
 * Only the origin dimension lives here. Permission is read from the section
 * registry (`ADMIN_ONLY_DASHBOARD_SECTIONS`) instead of being duplicated in a
 * second hand-maintained list, because two lists drift the moment a section
 * gains or loses its guard.
 *
 * A row pointing at an id nobody renders is dead weight; a new apiduo tab
 * missing from this list is invisible to the operator.
 */
const APIDUO_DASHBOARD_SECTIONS: ReadonlySet<string> = new Set([
  // Added in 5853a2ea together with the capacity and health views.
  'sk',
  'capacity',
  'retention',
])

/**
 * Value for the `data-apiduo` attribute on a dashboard tab, or `undefined` for
 * an upstream one. `undefined` keeps the attribute off the DOM entirely, so an
 * unmarked tab renders exactly as upstream does.
 */
export function apiduoTabMark(section: string): string | undefined {
  return APIDUO_DASHBOARD_SECTIONS.has(section) ? 'source' : undefined
}

/**
 * Blocks inside a page that apiduo added: a card in a drawer, a field in a
 * token form, a section in site settings.
 *
 * These are registered separately from the tab list because they carry a
 * different mark. A tab sits in a row of near-identical labels, so it needs a
 * dot to be found at all; a block already has a heading of its own, and a dot
 * beside it would read as a notification.
 *
 * Ids are `<area>:<thing>` so a rename in one area cannot silently re-mark an
 * unrelated block in another.
 */
const APIDUO_BLOCKS: ReadonlySet<string> = new Set([
  'dashboard:catalog-health',
  'channel:file-relay',
  'channel:artifact-storage',
  // 主端点 + 派生：把「每条入口一行」的路由收束成一个中心，apiduo 的结构性改造。
  'channel:primary-endpoint',
  // A third multi-key strategy: apiduo routes one token to one key.
  'channel:multi-key-sticky',
  // Per-entry coverage summary: which protocols a channel actually serves.
  'channel:route-coverage',
  // Pool width for multi-key channels: how many keys stay in rotation.
  'channel:min-active-keys',
  'security:agent-keys',
  'token:ip-allowlist',
  // 用户编辑抽屉里的「模型折扣」表格：按分组 × 模型配个人价，
  // 是 apiduo 加的计费能力，不标会被当成上游字段。
  'user:model-discounts',
  // Playground 的计费对账：金额 chip、对账单、请求详情都是 apiduo 加的，
  // 上游 Playground 只有耗时。不标会被当成上游行为。
  'playground:billing',
  'playground:pricing',
  // 三模式（对话 / 生图 / 生视频）切换是结构性改造，不是上游的分支。
  'playground:modes',
  'playground:image',
  'playground:video',
])

/**
 * Dialogs whose title carries the mark.
 *
 * `Dialog` exposes only `titleClassName`, not an attribute passthrough, so these
 * cannot go through `apiduoBlockMark`. They are listed here anyway so the full
 * set of marked surfaces is readable in one file rather than discovered by
 * grepping for a class name.
 */
export const APIDUO_DIALOG_TITLES: readonly string[] = [
  'channel:advanced-async',
  'channel:advanced-custom',
]

/** Exported so the test can prove the table and the call sites agree. */
export const APIDUO_BLOCK_IDS: readonly string[] = [...APIDUO_BLOCKS]

export function apiduoBlockMark(block: string): string | undefined {
  return APIDUO_BLOCKS.has(block) ? 'text' : undefined
}

/**
 * Settings sections apiduo added.
 *
 * One list serves both entry points: the sidebar sub-item (which only knows the
 * route) and the page title (which only knows the section id). Keeping two
 * lists would let them drift into marking a page without marking its link.
 */
const APIDUO_SETTINGS_SECTIONS: ReadonlySet<string> = new Set([
  'nav-apps',
  'artifact-storage',
  'appearance',
  'login-theme',
  'observability',
  'mcp',
])

/**
 * Icons for those sections' sidebar sub-items.
 *
 * Upstream section sub-items render as bare text — no sub-item anywhere in
 * settings carries an icon. Adding one is therefore a structural claim only
 * apiduo makes: the reader does not have to compare hues against the selected
 * state (which is also blue) to see which entries are not upstream. Each icon
 * is functional first — it says what the section does — and its gradient
 * stroke carries the origin mark, so the mark costs no extra geometry.
 */
const APIDUO_SETTINGS_SECTION_ICONS: Record<string, LucideIcon> = {
  'nav-apps': PanelsTopLeft,
  'artifact-storage': Database,
  appearance: Sparkles,
  'login-theme': LayoutTemplate,
  observability: Activity,
  mcp: Plug,
}

export function apiduoSettingsSectionIcon(
  section: string
): LucideIcon | undefined {
  return APIDUO_SETTINGS_SECTION_ICONS[section]
}

/**
 * Returns `'self'`, not `'text'`.
 *
 * These surfaces own an icon stroked with `currentColor`. The `text` variant
 * paints the container, which makes `color` transparent and erases that icon;
 * `self` colours only the label and leaves the icon alone.
 */
export function apiduoSettingsSectionMark(
  section: string
): string | undefined {
  return APIDUO_SETTINGS_SECTIONS.has(section) ? 'self' : undefined
}

/** Settings routes end in the section id (`.../site/nav-apps`). */
export function apiduoSettingsNavMark(
  url: string | undefined
): string | undefined {
  if (!url) return undefined
  return apiduoSettingsSectionMark(url.slice(url.lastIndexOf('/') + 1))
}

/** Same route rule as {@link apiduoSettingsNavMark}, for the entry's icon. */
export function apiduoSettingsNavIcon(url: string | undefined) {
  if (!url) return undefined
  return apiduoSettingsSectionIcon(url.slice(url.lastIndexOf('/') + 1))
}
