import { describe, expect, it } from 'vitest'

import {
  NAV_APP_ICONS,
  NAV_APP_ICON_GROUPS,
  NAV_APP_ICON_OPTIONS,
  navAppIcon,
  navAppIconLabel,
} from '../icons'

describe('icon choices', () => {
  it('resolves brand icons from the closed map', () => {
    expect(navAppIcon('github')).toBe(NAV_APP_ICONS.github)
    expect(navAppIcon('claude')).toBe(NAV_APP_ICONS.claude)
    expect(navAppIcon('clickhouse')).toBe(NAV_APP_ICONS.clickhouse)
  })

  it('falls back to the globe for a name that is not in the map', () => {
    // Simple Icons 5.7 dropped the OpenAI and Slack trademarks; those names
    // must degrade to the neutral icon, never render nothing.
    expect(navAppIcon('openai')).toBe(navAppIcon('definitely-not-an-icon'))
    expect(navAppIcon('slack')).toBe(navAppIcon('definitely-not-an-icon'))
  })

  it('groups cover every option exactly once', () => {
    const names = NAV_APP_ICON_GROUPS.flatMap((group) => [...group.names])
    expect(new Set(names).size).toBe(names.length)
    expect([...names].sort()).toEqual([...NAV_APP_ICON_OPTIONS].sort())
  })

  it('shows readable labels instead of raw slugs', () => {
    expect(navAppIconLabel('openrouter')).toBe('OpenRouter')
    expect(navAppIconLabel('book-open')).toBe('Book Open')
  })
})
