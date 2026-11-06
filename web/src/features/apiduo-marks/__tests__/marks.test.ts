import { describe, expect, test } from 'vitest'

import {
  apiduoBlockMark,
  apiduoSettingsNavIcon,
  apiduoSettingsNavMark,
  apiduoSettingsSectionIcon,
  apiduoTabMark,
} from '../marks'

describe('apiduoTabMark', () => {
  test.each(['sk', 'capacity', 'retention'])(
    'marks the apiduo section %s',
    (section) => {
      expect(apiduoTabMark(section)).toBe('source')
    }
  )

  test.each(['overview', 'my-keys', 'models', 'flow', 'users'])(
    'leaves the upstream section %s unmarked',
    (section) => {
      expect(apiduoTabMark(section)).toBeUndefined()
    }
  )

  test('does not answer for a block id', () => {
    // A block id leaking into the tab list would draw a tab mark on a rail
    // that renders one tab per section, so the two tables must stay apart.
    expect(apiduoTabMark('channel:file-relay')).toBeUndefined()
  })
})

describe('apiduoBlockMark', () => {
  test.each([
    'channel:file-relay',
    'security:agent-keys',
    'token:ip-allowlist',
  ])('marks the apiduo block %s', (block) => {
    expect(apiduoBlockMark(block)).toBe('text')
  })

  test('leaves an unknown block unmarked', () => {
    expect(apiduoBlockMark('channel:not-a-thing')).toBeUndefined()
  })

  test('does not answer for a section id', () => {
    expect(apiduoBlockMark('sk')).toBeUndefined()
  })

  test('does not answer for a dialog title', () => {
    // Dialogs take a class name instead of an attribute, so their ids are
    // listed in APIDUO_DIALOG_TITLES and must not resolve here.
    expect(apiduoBlockMark('channel:advanced-async')).toBeUndefined()
  })
})

describe('apiduoSettingsNavMark', () => {
  test('marks the settings section apiduo added', () => {
    expect(apiduoSettingsNavMark('/system-settings/site/nav-apps')).toBe('self')
  })

  test('leaves upstream settings sections alone', () => {
    expect(apiduoSettingsNavMark('/system-settings/site/branding')).toBeUndefined()
    expect(apiduoSettingsNavMark('/system-settings/auth')).toBeUndefined()
  })

  test('survives a missing route', () => {
    expect(apiduoSettingsNavMark(undefined)).toBeUndefined()
  })
})

describe('apiduoSettingsSectionIcon', () => {
  test.each(['nav-apps', 'artifact-storage'])(
    'supplies an icon for the apiduo section %s',
    (section) => {
      expect(apiduoSettingsSectionIcon(section)).toBeDefined()
    }
  )

  test('leaves upstream sections without an icon', () => {
    // An icon on an upstream sub-item would claim apiduo built it — the whole
    // point of the icon channel is that upstream never uses it.
    expect(apiduoSettingsSectionIcon('system-info')).toBeUndefined()
    expect(apiduoSettingsSectionIcon('branding')).toBeUndefined()
  })

  test('answers the nav route with the same icon as the section id', () => {
    expect(apiduoSettingsNavIcon('/system-settings/site/nav-apps')).toBe(
      apiduoSettingsSectionIcon('nav-apps')
    )
    expect(apiduoSettingsNavIcon(undefined)).toBeUndefined()
  })
})
