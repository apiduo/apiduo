import { readFileSync } from 'node:fs'
import { join } from 'node:path'

import { describe, expect, test } from 'vitest'

/**
 * The apiduo overlay only stays cheap if the upstream locale files stay clean.
 *
 * `config.ts` merges `apiduo/<lang>.json` into `locales/<lang>.json` at load
 * time, and the overlay wins. That split stops paying the moment a phrase is
 * added to an upstream file again: the next rebase conflicts on that line, which
 * is exactly what the split exists to prevent. Both failure modes below are
 * silent at runtime, so they are pinned here.
 */
const ROOT = process.cwd()
const LANGS = ['en', 'zh', 'zh-TW', 'fr', 'ru', 'ja', 'vi']

function translation(rel: string): Record<string, string> {
  return JSON.parse(readFileSync(join(ROOT, rel), 'utf8')).translation
}

describe('apiduo locale overlays', () => {
  test('the english overlay is the source string, never a translation', () => {
    // A translated value in en.json renders Chinese on the English console.
    // That already happened once while this overlay was being assembled from
    // the working tree, so it is now a hard rule rather than a review note.
    const en = translation('src/i18n/apiduo/en.json')
    const translated = Object.entries(en).filter(([key, value]) => value !== key)
    expect(translated).toEqual([])
  })

  test('an overlay repeats an upstream key only to change its wording', () => {
    // Same value as upstream means the overlay entry does nothing but create a
    // second place to edit the same phrase. Changing the wording is legitimate:
    // it covers upstream gaps such as an untranslated string.
    for (const lang of LANGS) {
      const overlay = translation(`src/i18n/apiduo/${lang}.json`)
      const upstream = translation(`src/i18n/locales/${lang}.json`)
      const redundant = Object.keys(overlay).filter(
        (key) => key in upstream && overlay[key] === upstream[key]
      )
      expect({ lang, redundant }).toEqual({ lang, redundant: [] })
    }
  })

  test('config merges the overlay into the runtime resources', async () => {
    // Guards the merge itself: a refactor of config.ts could drop the overlay
    // and every phrase would silently fall back to its English source string.
    const { resources } = await import('../config')
    const zh = resources.zhCN.translation as Record<string, string>
    expect(zh['Token Sticky']).toBe('令牌粘性')
  })
})
