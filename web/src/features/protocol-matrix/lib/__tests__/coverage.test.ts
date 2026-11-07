import { describe, expect, it } from 'vitest'

import type { AdvancedCustomRoute } from '@/features/channels/types'

import {
  buildCoverage,
  collectEndpoints,
  collectModels,
  coveredEndpoints,
  findRouteForModel,
  rejectedCells,
} from '../coverage'
import { MATRIX_DEFAULT_PATHS } from '../../types'

function nativeRoute(
  path: string,
  models?: string[]
): AdvancedCustomRoute {
  return {
    incoming_path: path,
    upstream_path: path,
    converter: 'none',
    ...(models ? { models } : {}),
  }
}

const seedreamRoutes: AdvancedCustomRoute[] = [
  nativeRoute('/v1/images/generations', ['seedream-3', 'seedream-4']),
  {
    incoming_path: '/v1/images/edits',
    upstream_path: '/v1/images/generations',
    converter: 'openai_chat_completions_to_anthropic_messages',
    models: ['seedream-4'],
  },
]

describe('findRouteForModel', () => {
  it('matches a route that lists the model', () => {
    const hit = findRouteForModel(seedreamRoutes, '/v1/images/edits', 'seedream-4')
    expect(hit?.index).toBe(1)
  })

  it('misses a model the route does not list', () => {
    expect(
      findRouteForModel(seedreamRoutes, '/v1/images/edits', 'seedream-3')
    ).toBeNull()
  })

  it('falls back to the catch-all route', () => {
    const routes = [nativeRoute('/v1/chat/completions')]
    expect(findRouteForModel(routes, '/v1/chat/completions', 'anything')?.index).toBe(
      0
    )
  })

  it('prefers the model rule when it comes before the catch-all', () => {
    const routes = [
      nativeRoute('/v1/chat/completions', ['gpt-4o']),
      nativeRoute('/v1/chat/completions'),
    ]
    expect(findRouteForModel(routes, '/v1/chat/completions', 'gpt-4o')?.index).toBe(0)
    expect(findRouteForModel(routes, '/v1/chat/completions', 'gpt-4o-mini')?.index).toBe(
      1
    )
  })

  it('supports regex model rules', () => {
    const routes = [nativeRoute('/v1/chat/completions', ['re:^seedream-.*$'])]
    expect(
      findRouteForModel(routes, '/v1/chat/completions', 'seedream-4')?.index
    ).toBe(0)
    expect(
      findRouteForModel(routes, '/v1/chat/completions', 'gpt-4o')
    ).toBeNull()
  })

  it('ignores a broken regex rule instead of throwing', () => {
    const routes = [nativeRoute('/v1/chat/completions', ['re:['])]
    expect(() =>
      findRouteForModel(routes, '/v1/chat/completions', 'x')
    ).not.toThrow()
    expect(findRouteForModel(routes, '/v1/chat/completions', 'x')).toBeNull()
  })

  it('only matches on the exact endpoint', () => {
    expect(
      findRouteForModel(seedreamRoutes, '/v1/images/variations', 'seedream-4')
    ).toBeNull()
  })
})

describe('buildCoverage', () => {
  it('reports which model would hit a 404', () => {
    const coverage = buildCoverage(
      seedreamRoutes,
      ['seedream-3', 'seedream-4'],
      ['/v1/images/generations', '/v1/images/edits']
    )

    expect(coverage.forwarded).toBe(3)
    expect(coverage.rejected).toBe(1)
    expect(rejectedCells(coverage).map((cell) => [cell.endpoint, cell.model])).toEqual([
      ['/v1/images/edits', 'seedream-3'],
    ])
  })

  it('carries the converter onto the forwarded cell', () => {
    const coverage = buildCoverage(
      seedreamRoutes,
      ['seedream-4'],
      ['/v1/images/edits']
    )
    const outcome = coverage.cells[0].outcome
    expect(outcome.kind).toBe('forwarded')
    if (outcome.kind !== 'forwarded') return
    expect(outcome.converter).toBe(
      'openai_chat_completions_to_anthropic_messages'
    )
  })

  it('leaves converter undefined for native forwarding', () => {
    const coverage = buildCoverage(
      [nativeRoute('/v1/embeddings')],
      ['text-embedding-3-small'],
      ['/v1/embeddings']
    )
    const outcome = coverage.cells[0].outcome
    expect(outcome.kind).toBe('forwarded')
    if (outcome.kind !== 'forwarded') return
    expect(outcome.converter).toBeUndefined()
  })

  it('treats an empty model list as nothing to check', () => {
    const coverage = buildCoverage(seedreamRoutes, [], ['/v1/images/edits'])
    expect(coverage.models).toEqual([])
    expect(coverage.cells).toEqual([])
    expect(coverage.rejected).toBe(0)
  })

  it('deduplicates and trims both axes', () => {
    const coverage = buildCoverage(
      [nativeRoute('/v1/embeddings')],
      [' a ', 'a', ''],
      ['/v1/embeddings', '/v1/embeddings']
    )
    expect(coverage.models).toEqual(['a'])
    expect(coverage.endpoints).toEqual(['/v1/embeddings'])
    expect(coverage.cells).toHaveLength(1)
  })
})

describe('collectModels', () => {
  it('lists concrete model names', () => {
    expect(
      collectModels([nativeRoute('/v1/chat/completions', ['gpt-4o', 'gpt-4o-mini'])])
    ).toEqual(['gpt-4o', 'gpt-4o-mini'])
  })

  it('skips regex rules because they cannot be expanded into concrete names', () => {
    const routes = [
      nativeRoute('/v1/chat/completions', ['re:^seedream-.*$', 'gpt-4o']),
    ]
    expect(collectModels(routes)).toEqual(['gpt-4o'])
  })

  it('returns nothing when every rule is a regex', () => {
    expect(
      collectModels([nativeRoute('/v1/chat/completions', ['re:^seedream-.*$'])])
    ).toEqual([])
  })
})

describe('management routes', () => {
  const management: AdvancedCustomRoute[] = [
    nativeRoute('/v1/models', []),
    nativeRoute('/v1/dashboard/billing/credit_grants', []),
  ]

  it('are not offered as coverage endpoints', () => {
    expect(collectEndpoints(management)).toEqual([...MATRIX_DEFAULT_PATHS])
  })

  it('are not counted as covered endpoints', () => {
    expect(coveredEndpoints(management).size).toBe(0)
  })
})

describe('collectEndpoints', () => {
  it('starts from the five default endpoints', () => {
    expect(collectEndpoints([])).toEqual([...MATRIX_DEFAULT_PATHS])
  })

  it('appends endpoints that only exist in the routes', () => {
    const endpoints = collectEndpoints([
      nativeRoute('/v1/images/variations'),
      nativeRoute('/v1/images/generations'),
    ])
    expect(endpoints).toContain('/v1/images/variations')
    expect(
      endpoints.filter((path) => path === '/v1/images/generations')
    ).toHaveLength(1)
  })
})
