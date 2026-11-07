import { describe, expect, it } from 'vitest'

import type { AdvancedCustomRoute } from '@/features/channels/types'

import { buildGrid, cellAt } from '../grid'
import {
  MATRIX_PRESETS,
  applyPreset,
  findPreset,
  presetToRoutes,
  routesToPresetEntries,
} from '../presets'

const MODELS = ['flash', 'pro']
const CHAT = '/v1/chat/completions'
const MESSAGES = '/v1/messages'

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

function emptyGrid() {
  return buildGrid([], MODELS, [CHAT])
}

describe('catalog', () => {
  it('exposes unique ids with at least one entry each', () => {
    const ids = MATRIX_PRESETS.map((preset) => preset.id)
    expect(new Set(ids).size).toBe(ids.length)
    for (const preset of MATRIX_PRESETS) {
      expect(preset.entries.length).toBeGreaterThan(0)
    }
    expect(findPreset('claude-upstream')?.id).toBe('claude-upstream')
    expect(findPreset('nope')).toBeUndefined()
  })
})

describe('applyPreset', () => {
  it('sets every model of the target endpoint to the converter', () => {
    const grid = applyPreset(emptyGrid(), findPreset('claude-upstream')!)
    expect(grid.cells.map((cell) => cell.mode)).toEqual([
      'openai_chat_completions_to_anthropic_messages',
      'openai_chat_completions_to_anthropic_messages',
    ])
  })

  it('adds an endpoint the table does not show yet', () => {
    const grid = applyPreset(emptyGrid(), findPreset('claude-client')!)
    expect(grid.endpoints).toContain(MESSAGES)
    expect(cellAt(grid, MESSAGES, 'flash')?.mode).toBe(
      'anthropic_messages_to_openai_chat_completions'
    )
    // 原有的行不受影响
    expect(cellAt(grid, CHAT, 'flash')?.mode).toBe('off')
  })

  it('leaves unrelated endpoints alone', () => {
    const previous = [native('/v1/images/edits', ['flash'])]
    const grid = buildGrid(previous, MODELS, ['/v1/images/edits', CHAT])
    const next = applyPreset(grid, findPreset('claude-upstream')!)
    expect(cellAt(next, '/v1/images/edits', 'flash')?.mode).toBe('none')
    expect(cellAt(next, CHAT, 'pro')?.mode).toBe(
      'openai_chat_completions_to_anthropic_messages'
    )
  })

  it('skips an endpoint the matrix cannot rewrite', () => {
    const grid = buildGrid(
      [native(CHAT, ['re:^flash$'])],
      MODELS,
      [CHAT]
    )
    const next = applyPreset(grid, findPreset('claude-upstream')!)
    expect(cellAt(next, CHAT, 'flash')?.mode).toBe('off')
  })

  it('is idempotent', () => {
    const preset = findPreset('gemini-upstream')!
    const once = applyPreset(emptyGrid(), preset)
    expect(applyPreset(once, preset).cells).toEqual(once.cells)
  })
})

describe('presetToRoutes', () => {
  it('emits one route with the converter defaults', () => {
    const preset = findPreset('claude-upstream')!
    expect(presetToRoutes(emptyGrid(), preset, [])).toEqual([
      {
        incoming_path: CHAT,
        upstream_path: MESSAGES,
        converter: 'openai_chat_completions_to_anthropic_messages',
        auth: { type: 'header', name: 'x-api-key', value: '{api_key}' },
      },
    ])
  })

  it('serves a client protocol the table did not have', () => {
    const preset = findPreset('claude-client')!
    expect(presetToRoutes(emptyGrid(), preset, [])).toEqual([
      {
        incoming_path: MESSAGES,
        upstream_path: CHAT,
        converter: 'anthropic_messages_to_openai_chat_completions',
        auth: {
          type: 'header',
          name: 'Authorization',
          value: 'Bearer {api_key}',
        },
      },
    ])
  })

  it('overrides the upstream path for the image pair', () => {
    const preset = findPreset('image-dual')!
    const routes = presetToRoutes(emptyGrid(), preset, [])
    expect(routes).toEqual([
      native('/v1/images/generations'),
      // edits 走 generations 时必须开请求体透传：否则 body 会按
      // dto.ImageRequest 重建，seed / sequential_image_generation 这类
      // 厂商私有字段被静默丢掉。
      {
        incoming_path: '/v1/images/edits',
        upstream_path: '/v1/images/generations',
        converter: 'none',
        pass_through_body_enabled: true,
      },
    ])
  })

  it('does not duplicate routes that already exist', () => {
    const previous = [native('/v1/images/edits')]
    const grid = buildGrid(previous, MODELS, ['/v1/images/edits'])
    const routes = presetToRoutes(grid, findPreset('image-dual')!, previous)
    expect(routes.filter((route) => route.incoming_path === '/v1/images/edits'))
      .toHaveLength(1)
    expect(routes[0].upstream_path).toBe('/v1/images/generations')
  })

  it('keeps management routes', () => {
    const management = native('/v1/models')
    const grid = buildGrid([management], MODELS, ['/v1/models'])
    const routes = presetToRoutes(grid, findPreset('claude-upstream')!, [
      management,
    ])
    expect(routes[0]).toEqual(management)
  })

  it('leaves a conflicting endpoint untouched', () => {
    const previous = [native(CHAT, ['re:^flash$'], '/custom')]
    const grid = buildGrid(previous, MODELS, [CHAT])
    expect(presetToRoutes(grid, findPreset('gemini-upstream')!, previous)).toEqual(
      previous
    )
  })
})

describe('routesToPresetEntries', () => {
  it('drops models and management routes but keeps the wiring', () => {
    const routes: AdvancedCustomRoute[] = [
      native('/v1/models', undefined, '/api/v3/models'),
      {
        incoming_path: '/v1/images/edits',
        upstream_path: '/v1/images/generations',
        converter: 'none',
        models: ['flash'],
      },
      {
        incoming_path: '/v1/messages',
        upstream_path: '/v1/chat/completions',
        converter: 'anthropic_messages_to_openai_chat_completions',
        auth: {
          type: 'header',
          name: 'Authorization',
          value: 'Bearer {api_key}',
        },
        pass_through_body_enabled: true,
      },
    ]

    expect(routesToPresetEntries(routes)).toEqual([
      {
        endpoint: '/v1/images/edits',
        mode: 'none',
        upstreamPath: '/v1/images/generations',
      },
      {
        endpoint: '/v1/messages',
        mode: 'anthropic_messages_to_openai_chat_completions',
        upstreamPath: '/v1/chat/completions',
        auth: {
          type: 'header',
          name: 'Authorization',
          value: 'Bearer {api_key}',
        },
        passThroughBody: true,
      },
    ])
  })

  it('keeps only the first route of an endpoint', () => {
    const routes = [
      {
        incoming_path: CHAT,
        converter: 'openai_chat_completions_to_anthropic_messages' as const,
        models: ['flash'],
      },
      {
        incoming_path: CHAT,
        converter: 'openai_chat_completions_to_gemini_generate_content' as const,
        models: ['pro'],
      },
    ]
    expect(routesToPresetEntries(routes)).toHaveLength(1)
    expect(routesToPresetEntries(routes)[0].mode).toBe(
      'openai_chat_completions_to_anthropic_messages'
    )
  })

  it('ignores an empty route list', () => {
    expect(routesToPresetEntries(undefined)).toEqual([])
    expect(routesToPresetEntries([])).toEqual([])
  })
})

describe('custom preset round trip', () => {
  it('reproduces the captured wiring on an empty channel', () => {
    const source: AdvancedCustomRoute[] = [
      {
        incoming_path: '/v1/images/edits',
        upstream_path: '/api/v3/images/generations',
        converter: 'none',
        models: ['seedream-4'],
      },
      {
        incoming_path: CHAT,
        upstream_path: '/v1/messages',
        converter: 'openai_chat_completions_to_anthropic_messages',
        auth: { type: 'header', name: 'x-api-key', value: '{api_key}' },
      },
    ]

    const preset = {
      id: 'custom:p1',
      label: 'My shape',
      builtIn: false,
      entries: routesToPresetEntries(source),
    }

    const routes = presetToRoutes(emptyGrid(), preset, [])
    // CHAT 本来就在表里，images/edits 是补出来的行，所以按表格顺序排在后面。
    expect(routes).toEqual([
      {
        incoming_path: CHAT,
        upstream_path: '/v1/messages',
        converter: 'openai_chat_completions_to_anthropic_messages',
        auth: { type: 'header', name: 'x-api-key', value: '{api_key}' },
      },
      {
        incoming_path: '/v1/images/edits',
        upstream_path: '/api/v3/images/generations',
        converter: 'none',
      },
    ])
  })

  it('applies a hand-written auth override', () => {
    const preset = {
      id: 'custom:p2',
      label: 'Custom auth',
      builtIn: false,
      entries: [
        {
          endpoint: CHAT,
          mode: 'openai_chat_completions_to_openai_responses' as const,
          auth: { type: 'header' as const, name: 'x-tenant', value: 'acme' },
        },
      ],
    }
    expect(presetToRoutes(emptyGrid(), preset, [])[0].auth).toEqual({
      type: 'header',
      name: 'x-tenant',
      value: 'acme',
    })
  })
})
