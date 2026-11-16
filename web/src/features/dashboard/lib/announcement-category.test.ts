import { describe, expect, it } from 'vitest'

import {
  announcementCategory,
  changelogKind,
  isChangelog,
  isFreshUpdate,
} from './announcement-category'

describe('announcementCategory', () => {
  it('缺省视为公告', () => {
    expect(announcementCategory({})).toBe('announcement')
    expect(announcementCategory({ category: undefined })).toBe('announcement')
  })

  it('空字符串视为公告', () => {
    expect(announcementCategory({ category: '' })).toBe('announcement')
  })

  it('认得出 changelog', () => {
    expect(announcementCategory({ category: 'changelog' })).toBe('changelog')
    expect(isChangelog({ category: 'changelog' })).toBe(true)
  })

  it('显式 announcement 保持公告', () => {
    expect(announcementCategory({ category: 'announcement' })).toBe('announcement')
    expect(isChangelog({ category: 'announcement' })).toBe(false)
  })

  // 后端会拒绝非法值，但存量数据或外部写入可能有脏值。
  // 前端按公告处理：宁可多显示在公告栏，也不要让一条消息凭空消失。
  it('未知取值回落到公告而不是丢弃', () => {
    expect(announcementCategory({ category: 'news' })).toBe('announcement')
    expect(announcementCategory({ category: 1 })).toBe('announcement')
    expect(announcementCategory({ category: null })).toBe('announcement')
  })
})

describe('changelogKind', () => {
  it('认得出三种变更类型', () => {
    expect(changelogKind({ kind: 'feature' })).toBe('feature')
    expect(changelogKind({ kind: 'improvement' })).toBe('improvement')
    expect(changelogKind({ kind: 'fix' })).toBe('fix')
  })

  // 没填就是没分类，不给标签 —— 比默认成「新增」诚实
  it('缺省与脏值返回 undefined', () => {
    expect(changelogKind({})).toBeUndefined()
    expect(changelogKind({ kind: '' })).toBeUndefined()
    expect(changelogKind({ kind: 'breaking' })).toBeUndefined()
    expect(changelogKind({ kind: 3 })).toBeUndefined()
  })
})

describe('isFreshUpdate', () => {
  const now = Date.parse('2026-11-17T12:00:00+08:00')
  const at = (days: number) =>
    new Date(now - days * 86400_000).toISOString()

  it('7 天内算新', () => {
    expect(isFreshUpdate({ publishDate: at(0) }, now)).toBe(true)
    expect(isFreshUpdate({ publishDate: at(6.9) }, now)).toBe(true)
  })

  it('超过 7 天不算新', () => {
    expect(isFreshUpdate({ publishDate: at(7) }, now)).toBe(false)
    expect(isFreshUpdate({ publishDate: at(30) }, now)).toBe(false)
  })

  // 未来的发布时间还没生效，不该打「新」标记
  it('未来时间不算新', () => {
    expect(isFreshUpdate({ publishDate: at(-1) }, now)).toBe(false)
  })

  it('缺时间或解析不了不算新', () => {
    expect(isFreshUpdate({}, now)).toBe(false)
    expect(isFreshUpdate({ publishDate: 'not-a-date' }, now)).toBe(false)
  })
})
