/**
 * 一条消息属于哪个展示位。
 *
 * `announcement` 进仪表盘的公告面板，`changelog` 进顶部的功能更新横幅。
 * 这个区分是渲染层面的，不改变存储形态：两者共用 announcements 数组。
 */
export type AnnouncementCategory = 'announcement' | 'changelog'

/**
 * 读出 category，缺省与脏值一律按公告处理。
 *
 * 后端会拒绝非法取值，但存量数据和外部写入不受它约束。
 * 回落到公告而不是丢弃：宁可让一条消息出现在公告栏，
 * 也不要因为一个字段拼错就让它从所有人的视野里消失。
 */
export function announcementCategory(item: {
  category?: unknown
}): AnnouncementCategory {
  return item.category === 'changelog' ? 'changelog' : 'announcement'
}

export function isChangelog(item: { category?: unknown }): boolean {
  return announcementCategory(item) === 'changelog'
}

/** 功能更新的变更类型。与公告的 type（严重程度）是两个维度。 */
export type ChangelogKind = 'feature' | 'improvement' | 'fix'

/**
 * 读出变更类型，没填或非法就返回 undefined。
 *
 * 不回落成 `feature`：把一条没分类的更新标成「新增」是在替作者说话，
 * 而它可能只是一次修复。没有标签比错标签好。
 */
export function changelogKind(item: { kind?: unknown }): ChangelogKind | undefined {
  const kind = item.kind
  return kind === 'feature' || kind === 'improvement' || kind === 'fix'
    ? kind
    : undefined
}

const FRESH_WINDOW_MS = 7 * 86400_000

/**
 * 这条更新是不是「最近发布的」，用于打 NEW 标记。
 *
 * 未来时间不算：还没生效的更新不该标成新。缺时间或解析失败也不算 ——
 * 一个解析不了的日期无法证明它新。
 */
export function isFreshUpdate(
  item: { publishDate?: unknown },
  now: number = Date.now()
): boolean {
  if (typeof item.publishDate !== 'string') return false
  const published = Date.parse(item.publishDate)
  if (Number.isNaN(published)) return false
  const age = now - published
  return age >= 0 && age < FRESH_WINDOW_MS
}
