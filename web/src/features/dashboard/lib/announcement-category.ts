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
