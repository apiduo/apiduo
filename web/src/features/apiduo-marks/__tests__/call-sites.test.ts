import { globSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, test } from 'vitest'

import { APIDUO_BLOCK_IDS, apiduoBlockMark } from '../marks'

/**
 * The mark only exists if two halves agree: an id in the table and the same id
 * at the call site. Neither half fails loudly on its own — a call site with an
 * unregistered id renders `undefined` and silently loses its mark, and a
 * registered id nobody uses is dead weight that survives review.
 *
 * Both directions are cheap to check by reading the source, and both have
 * already bitten during development, so they are pinned here.
 */
let cachedSources: string[] | null = null

function readSources(): string[] {
  if (cachedSources) return cachedSources
  const root = process.cwd()
  cachedSources = globSync('src/**/*.{ts,tsx}', { cwd: root })
    .filter((file) => !file.includes('__tests__'))
    .map((file) => readFileSync(join(root, file), 'utf8'))
  return cachedSources
}

describe('apiduo block marks', () => {
  test('every id used at a call site is registered', () => {
    const used = new Set<string>()
    for (const source of readSources()) {
      for (const match of source.matchAll(/apiduoBlockMark\('([^']+)'\)/g)) {
        used.add(match[1])
      }
    }

    expect(used.size).toBeGreaterThan(0)
    for (const id of used) {
      expect(apiduoBlockMark(id)).toBe('text')
    }
  })

  test('every registered id is used at least once', () => {
    const sources = readSources()
    for (const id of APIDUO_BLOCK_IDS) {
      const referenced = sources.some((source) =>
        source.includes(`apiduoBlockMark('${id}')`)
      )
      expect(referenced).toBe(true)
    }
  })
})
