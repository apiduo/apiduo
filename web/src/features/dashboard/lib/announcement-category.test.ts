import { describe, expect, it } from 'vitest'

import { announcementCategory, isChangelog } from './announcement-category'

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
