import { describe, expect, it } from 'vitest'

import type { AdvancedCustomRoute } from '@/features/channels/types'

import {
  buildGrid,
  cellAt,
  converterOptionsForEndpoint,
  fillAll,
  fillEndpoint,
  gridToRoutes,
  isCellEditable,
  modelsByMode,
  normalizeConverter,
  orphanCount,
  setCellMode,
  supportedModels,
  toggleCell,
} from '../grid'

const MODELS = ['flash', 'pro']
const CHAT = '/v1/chat/completions'

function native(
  path: string,
  models?: string[],
  upstreamPath = path
): AdvancedCustomRoute {
  return {
    incoming_path: path,
    upstream_path: upstreamPath,
    converter: 'none',
    ...(models ? { models } : {}),
  }
}

describe('buildGrid', () => {
  it('marks every model as passthrough when the endpoint has a catch-all route', () => {
    const grid = buildGrid([native('/v1/images/generations')], MODELS, [
      '/v1/images/generations',
    ])
    expect(grid.cells.map((cell) => cell.mode)).toEqual(['none', 'none'])
    expect(grid.conflicts).toEqual({})
  })

  it('only marks the listed model when the route carries model rules', () => {
    const grid = buildGrid(
      [native('/v1/images/edits', ['flash'])],
      MODELS,
      ['/v1/images/edits']
    )
    expect(grid.cells.map((cell) => cell.mode)).toEqual(['none', 'off'])
  })

  it('leaves an unconfigured endpoint fully off', () => {
    const grid = buildGrid([], MODELS, ['/v1/embeddings'])
    expect(grid.cells.every((cell) => cell.mode === 'off')).toBe(true)
  })

  it('reads back two converters on the same endpoint', () => {
    // 之前这种端点直接判成冲突；三态之后它是可表达的，不该再锁编辑。
    const routes: AdvancedCustomRoute[] = [
      {
        incoming_path: CHAT,
        upstream_path: '/v1/messages',
        converter: 'openai_chat_completions_to_anthropic_messages',
        models: ['flash'],
      },
      native(CHAT, ['pro']),
    ]
    const grid = buildGrid(routes, MODELS, [CHAT])
    expect(grid.conflicts).toEqual({})
    expect(grid.cells.map((cell) => cell.mode)).toEqual([
      'openai_chat_completions_to_anthropic_messages',
      'none',
    ])
  })

  it('flags regex model rules as unrepresentable', () => {
    const grid = buildGrid(
      [native('/v1/images/edits', ['re:^flash.*$'])],
      MODELS,
      ['/v1/images/edits']
    )
    expect(grid.conflicts['/v1/images/edits']).toBe('regex-models')
    expect(isCellEditable(grid, '/v1/images/edits')).toBe(false)
  })

  it('flags a converter that cannot serve the endpoint', () => {
    const grid = buildGrid(
      [
        {
          incoming_path: '/v1/embeddings',
          converter: 'openai_chat_completions_to_anthropic_messages',
        },
      ],
      MODELS,
      ['/v1/embeddings']
    )
    expect(grid.conflicts['/v1/embeddings']).toBe('unsupported-converter')
  })

  it('flags two routes claiming the same model', () => {
    const grid = buildGrid(
      [native(CHAT, ['flash']), native(CHAT)],
      MODELS,
      [CHAT]
    )
    expect(grid.conflicts[CHAT]).toBe('overlapping-routes')
  })

  it('collects models the channel list no longer declares as orphans', () => {
    const grid = buildGrid(
      [native('/v1/images/edits', ['flash', 'ghost'])],
      MODELS,
      ['/v1/images/edits']
    )
    expect(grid.orphans).toEqual([
      { endpoint: '/v1/images/edits', model: 'ghost', mode: 'none' },
    ])
    expect(orphanCount(grid)).toBe(1)
  })

  it('ignores management routes', () => {
    const grid = buildGrid([native('/v1/models')], MODELS, ['/v1/models'])
    expect(grid.cells.every((cell) => cell.mode === 'off')).toBe(true)
  })

  it('normalizes an empty converter string to native forwarding', () => {
    // 手工编辑过的配置可能留下空串，上游 Validate 会归一成 none。
    const handEdited = { converter: '' } as unknown as AdvancedCustomRoute
    expect(normalizeConverter(handEdited)).toBe('none')
    expect(normalizeConverter({ converter: undefined })).toBe('none')
  })
})

describe('cell edits', () => {
  const base = buildGrid([native('/v1/images/generations')], MODELS, [
    '/v1/images/generations',
  ])

  it('toggles a single cell between off and passthrough', () => {
    const next = toggleCell(base, '/v1/images/generations', 'flash')
    expect(cellAt(next, '/v1/images/generations', 'flash')?.mode).toBe('off')
    expect(supportedModels(next, '/v1/images/generations')).toEqual(['pro'])
    expect(
      cellAt(
        toggleCell(next, '/v1/images/generations', 'flash'),
        '/v1/images/generations',
        'flash'
      )?.mode
    ).toBe('none')
  })

  it('sets a converter on a single cell only', () => {
    const next = setCellMode(
      buildGrid([], MODELS, [CHAT]),
      CHAT,
      'flash',
      'openai_chat_completions_to_anthropic_messages'
    )
    expect(cellAt(next, CHAT, 'flash')?.mode).toBe(
      'openai_chat_completions_to_anthropic_messages'
    )
    expect(cellAt(next, CHAT, 'pro')?.mode).toBe('off')
  })

  it('fills a row between off and passthrough', () => {
    const off = fillEndpoint(base, '/v1/images/generations', 'off')
    expect(supportedModels(off, '/v1/images/generations')).toEqual([])
    expect(
      supportedModels(
        fillEndpoint(off, '/v1/images/generations', 'none'),
        '/v1/images/generations'
      )
    ).toEqual(MODELS)
  })

  it('does not downgrade an existing converter when filling a row', () => {
    const grid = setCellMode(
      buildGrid([], MODELS, [CHAT]),
      CHAT,
      'flash',
      'openai_chat_completions_to_anthropic_messages'
    )
    expect(fillEndpoint(grid, CHAT, 'none').cells.map((cell) => cell.mode)).toEqual(
      ['openai_chat_completions_to_anthropic_messages', 'none']
    )
  })

  it('keeps converters when selecting all', () => {
    const grid = buildGrid([], MODELS, [CHAT])
    const mixed = setCellMode(
      setCellMode(grid, CHAT, 'flash', 'openai_chat_completions_to_anthropic_messages'),
      CHAT,
      'pro',
      'openai_chat_completions_to_openai_responses'
    )
    expect(fillAll(mixed, 'none').cells.map((cell) => cell.mode)).toEqual([
      'openai_chat_completions_to_anthropic_messages',
      'openai_chat_completions_to_openai_responses',
    ])
    expect(fillAll(mixed, 'off').cells.every((cell) => cell.mode === 'off')).toBe(
      true
    )
  })

  it('does not touch conflicting endpoints when filling all', () => {
    const grid = buildGrid([native(CHAT, ['re:.*'])], MODELS, [CHAT])
    expect(fillAll(grid, 'none').cells.every((cell) => cell.mode === 'off')).toBe(
      true
    )
  })

  it('groups models by mode in table order', () => {
    const grid = setCellMode(
      setCellMode(
        buildGrid([], MODELS, [CHAT]),
        CHAT,
        'flash',
        'openai_chat_completions_to_anthropic_messages'
      ),
      CHAT,
      'pro',
      'openai_chat_completions_to_openai_responses'
    )
    expect([...modelsByMode(grid, CHAT).entries()]).toEqual([
      ['openai_chat_completions_to_anthropic_messages', ['flash']],
      ['openai_chat_completions_to_openai_responses', ['pro']],
    ])
  })

  it('lists only converters that serve the endpoint', () => {
    expect(converterOptionsForEndpoint(CHAT)).toEqual([
      'openai_chat_completions_to_anthropic_messages',
      'openai_chat_completions_to_openai_responses',
      'openai_chat_completions_to_gemini_generate_content',
    ])
    expect(converterOptionsForEndpoint('/v1/images/generations')).toEqual([])
  })
})

describe('gridToRoutes', () => {
  it('collapses a fully selected endpoint into a catch-all route', () => {
    const grid = fillAll(buildGrid([], MODELS, ['/v1/images/generations']), 'none')
    expect(gridToRoutes(grid, [])).toEqual([native('/v1/images/generations')])
  })

  it('writes a model list when only part of the row is selected', () => {
    const grid = buildGrid([native('/v1/images/edits', ['flash'])], MODELS, [
      '/v1/images/edits',
    ])
    expect(gridToRoutes(grid, [])).toEqual([native('/v1/images/edits', ['flash'])])
  })

  it('emits one route per converter on the same endpoint', () => {
    let grid = buildGrid([], MODELS, [CHAT])
    grid = setCellMode(
      grid,
      CHAT,
      'flash',
      'openai_chat_completions_to_anthropic_messages'
    )
    grid = setCellMode(grid, CHAT, 'pro', 'none')

    const routes = gridToRoutes(grid, [])
    expect(routes).toEqual([
      {
        incoming_path: CHAT,
        upstream_path: '/v1/messages',
        converter: 'openai_chat_completions_to_anthropic_messages',
        auth: { type: 'header', name: 'x-api-key', value: '{api_key}' },
        models: ['flash'],
      },
      {
        incoming_path: CHAT,
        upstream_path: CHAT,
        converter: 'none',
        models: ['pro'],
      },
    ])
  })

  it('does not write a catch-all when a converter covers only part of the row', () => {
    const grid = setCellMode(
      buildGrid([], MODELS, [CHAT]),
      CHAT,
      'flash',
      'openai_chat_completions_to_anthropic_messages'
    )
    const routes = gridToRoutes(grid, [])
    expect(routes).toHaveLength(1)
    expect(routes[0].models).toEqual(['flash'])
  })

  it('drops an endpoint whose row is fully deselected', () => {
    const grid = buildGrid([native('/v1/images/edits')], MODELS, [
      '/v1/images/edits',
    ])
    expect(gridToRoutes(fillEndpoint(grid, '/v1/images/edits', 'off'), [])).toEqual(
      []
    )
  })

  it('keeps management routes untouched', () => {
    const management = native('/v1/models')
    const grid = buildGrid([management], MODELS, ['/v1/models'])
    expect(gridToRoutes(grid, [management])).toContainEqual(management)
  })

  it('keeps a conflicting endpoint exactly as it was', () => {
    const previous = [native(CHAT, ['flash']), native(CHAT, ['pro'])]
    const grid = buildGrid(
      [native(CHAT, ['re:^flash$']), native(CHAT, ['pro'])],
      MODELS,
      [CHAT]
    )
    expect(gridToRoutes(grid, previous)).toEqual(previous)
  })

  it('reuses the converter and upstream path already configured', () => {
    const previous: AdvancedCustomRoute[] = [
      {
        incoming_path: '/v1/messages',
        upstream_path: '/v1/chat/completions',
        converter: 'anthropic_messages_to_openai_chat_completions',
        auth: { type: 'header', name: 'Authorization', value: 'Bearer {api_key}' },
      },
    ]
    const grid = fillAll(buildGrid(previous, MODELS, ['/v1/messages']), 'none')
    expect(gridToRoutes(grid, previous)).toEqual(previous)
  })

  it('takes the template matching the converter, not the first route', () => {
    // 两个转换器各有自己的上游路径，不能交叉沿用。
    const previous: AdvancedCustomRoute[] = [
      {
        incoming_path: CHAT,
        upstream_path: '/custom/claude',
        converter: 'openai_chat_completions_to_anthropic_messages',
        models: ['flash'],
      },
      {
        incoming_path: CHAT,
        upstream_path: '/custom/gemini',
        converter: 'openai_chat_completions_to_gemini_generate_content',
        models: ['pro'],
      },
    ]
    const grid = buildGrid(previous, MODELS, [CHAT])
    expect(gridToRoutes(grid, previous)).toEqual(previous)
  })

  it('keeps orphan models when regenerating', () => {
    const previous = [native('/v1/images/edits', ['flash', 'ghost'])]
    const grid = buildGrid(previous, MODELS, ['/v1/images/edits'])
    expect(gridToRoutes(grid, previous)).toEqual(previous)
  })

  it('never turns a partially selected endpoint into a catch-all because of orphans', () => {
    const previous = [native('/v1/images/edits', ['flash', 'ghost'])]
    const grid = buildGrid(previous, MODELS, ['/v1/images/edits'])
    const routes = gridToRoutes(grid, previous)
    expect(routes[0].models).toEqual(['flash', 'ghost'])
  })

  it('keeps the relative order of untouched endpoints', () => {
    const previous = [
      native('/v1/images/generations', ['flash']),
      native('/v1/models'),
      native('/v1/images/edits', ['pro']),
    ]
    const grid = buildGrid(previous, MODELS, [
      '/v1/images/edits',
      '/v1/images/generations',
    ])
    const routes = gridToRoutes(grid, previous)
    expect(routes.map((route) => route.incoming_path)).toEqual([
      '/v1/images/generations',
      '/v1/models',
      '/v1/images/edits',
    ])
  })

  it('appends a brand new endpoint at the end', () => {
    const previous = [native('/v1/models')]
    const grid = buildGrid(previous, MODELS, ['/v1/embeddings'])
    const routes = gridToRoutes(fillAll(grid, 'none'), previous)
    expect(routes.map((route) => route.incoming_path)).toEqual([
      '/v1/models',
      '/v1/embeddings',
    ])
    expect(routes[1].models).toBeUndefined()
  })

  it('drops a route the user turned off, keeps the rest', () => {
    const previous = [native('/v1/images/edits', ['flash'])].concat([
      native('/v1/images/generations'),
    ])
    let grid = buildGrid(previous, MODELS, [
      '/v1/images/edits',
      '/v1/images/generations',
    ])
    grid = fillEndpoint(grid, '/v1/images/edits', 'off')
    expect(gridToRoutes(grid, previous)).toEqual([
      native('/v1/images/generations'),
    ])
  })
})
