import { describe, expect, it } from 'vitest'

import { ADMIN_ONLY_DASHBOARD_SECTIONS } from '../section-meta'
import { DASHBOARD_SECTION_IDS } from '../section-registry'

// 与 index.tsx 里的过滤表达式保持一致。复制一份是为了让判据能独立运行，
// 不必挂载整个 dashboard 组件。
const visibleFor = (isAdmin: boolean) =>
  DASHBOARD_SECTION_IDS.filter(
    (id) => isAdmin || !ADMIN_ONLY_DASHBOARD_SECTIONS.includes(id)
  )

// sk 分析下放给普通用户：它读 /api/data/sk/self，组件内部按 isAdmin 切换端点。
describe('dashboard 区块可见性', () => {
  it('sk 不在管理员专属列表里', () => {
    expect(ADMIN_ONLY_DASHBOARD_SECTIONS).not.toContain('sk')
  })

  it('普通用户能看到 sk', () => {
    expect(visibleFor(false)).toContain('sk')
  })

  it('普通用户看不到 users', () => {
    expect(visibleFor(false)).not.toContain('users')
  })

  // capacity 与 retention 一样是 apiduo 新增的（对照 upstream/main 确认），
  // 已迁到 /service-health：容量预测本来就属于「快满了」这一维。
  it('capacity 已从 dashboard 移除', () => {
    expect(DASHBOARD_SECTION_IDS).not.toContain('capacity')
    expect(visibleFor(true)).not.toContain('capacity')
  })

  // retention（客户余额健康 + 充值）是 apiduo 新增的，不是 new-api 原生，
  // 已整体迁到 /growth。留在两处会让余额口径打架：
  // dashboard 用 7 天均值，growth 用 3/7 天较快速率。
  it('retention 已从 dashboard 移除', () => {
    expect(DASHBOARD_SECTION_IDS).not.toContain('retention')
    expect(visibleFor(true)).not.toContain('retention')
  })

  it('管理员能看到全部区块', () => {
    expect(visibleFor(true)).toEqual([...DASHBOARD_SECTION_IDS])
  })
})
