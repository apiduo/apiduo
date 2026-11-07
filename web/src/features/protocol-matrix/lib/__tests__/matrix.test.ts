import { describe, expect, it } from 'vitest'

import { getAdvancedCustomConverterDefaults } from '@/features/channels/lib/advanced-custom'
import type {
  AdvancedCustomConverter,
  AdvancedCustomRoute,
} from '@/features/channels/types'

import {
  converterOptionsForPath,
  decodeMatrix,
  encodeMatrix,
  validateMatrix,
} from '../matrix'
import type { Matrix } from '../../types'

function nativeRoute(
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

function convertedRoute(
  path: string,
  converter: AdvancedCustomConverter,
  models?: string[]
): AdvancedCustomRoute {
  const defaults = getAdvancedCustomConverterDefaults(converter, path)
  return {
    incoming_path: path,
    upstream_path: defaults.upstream_path,
    converter,
    auth: defaults.auth,
    ...(models ? { models } : {}),
  }
}

const passthroughRoute = nativeRoute('/v1/embeddings')
const transformRoute = convertedRoute(
  '/v1/messages',
  'anthropic_messages_to_openai_chat_completions'
)

describe('decodeMatrix', () => {
  it('treats an empty config as an empty matrix', () => {
    const result = decodeMatrix(undefined)
    expect(result.ok).toBe(true)
    if (!result.ok) return
    expect(result.matrix.items).toEqual([])
    expect(result.matrix.preserved).toEqual([])
  })

  it('reads a native route as passthrough', () => {
    const result = decodeMatrix([passthroughRoute])
    expect(result.ok).toBe(true)
    if (!result.ok) return
    expect(result.matrix.items).toEqual([
      {
        path: '/v1/embeddings',
        models: [],
        mode: 'passthrough',
        upstreamPath: '/v1/embeddings',
      },
    ])
  })

  it('reads a converted route as transform', () => {
    const result = decodeMatrix([transformRoute])
    expect(result.ok).toBe(true)
    if (!result.ok) return
    expect(result.matrix.items).toEqual([
      {
        path: '/v1/messages',
        models: [],
        mode: 'transform',
        converter: 'anthropic_messages_to_openai_chat_completions',
        upstreamPath: '/v1/chat/completions',
        auth: {
          type: 'header',
          name: 'Authorization',
          value: 'Bearer {api_key}',
        },
      },
    ])
  })

  it('keeps several models on one row', () => {
    const result = decodeMatrix([
      nativeRoute('/v1/images/generations', ['seedream-3', 'seedream-4']),
    ])
    expect(result.ok).toBe(true)
    if (!result.ok) return
    expect(result.matrix.items[0].models).toEqual(['seedream-3', 'seedream-4'])
  })

  it('keeps two model rules on the same endpoint as two rows', () => {
    const routes = [
      nativeRoute('/v1/chat/completions', ['seedream-4']),
      convertedRoute(
        '/v1/chat/completions',
        'openai_chat_completions_to_anthropic_messages',
        ['claude-3']
      ),
    ]
    const result = decodeMatrix(routes)
    expect(result.ok).toBe(true)
    if (!result.ok) return
    expect(result.matrix.items).toHaveLength(2)
    expect(result.matrix.items[1]).toEqual({
      path: '/v1/chat/completions',
      models: ['claude-3'],
      mode: 'transform',
      converter: 'openai_chat_completions_to_anthropic_messages',
      upstreamPath: '/v1/messages',
      auth: { type: 'header', name: 'x-api-key', value: '{api_key}' },
    })
  })

  it('keeps management routes aside instead of folding them into the matrix', () => {
    const managementRoute: AdvancedCustomRoute = nativeRoute('/v1/models', [])
    const result = decodeMatrix([managementRoute, passthroughRoute])
    expect(result.ok).toBe(true)
    if (!result.ok) return
    expect(result.matrix.preserved).toEqual([managementRoute])
    expect(result.matrix.items).toHaveLength(1)
  })

  it('keeps a {model} template upstream verbatim', () => {
    // upstream_path 里的 {model} 由运行时替换，矩阵原样带回即可，
    // 早先在这里拒绝是过度保守——它把整个 Gemini 渠道挡在门外。
    const routes = [
      nativeRoute('/v1/embeddings', undefined, '/v1/models/{model}'),
    ]
    const decoded = decodeMatrix(routes)
    expect(decoded.ok).toBe(true)
    if (!decoded.ok) return
    expect(encodeMatrix(decoded.matrix)).toEqual(routes)
  })

  it('refuses routes that override the default auth', () => {
    const result = decodeMatrix([
      {
        ...passthroughRoute,
        auth: { type: 'header', name: 'X-Token', value: 'custom' },
      },
    ])
    expect(result.ok).toBe(false)
    if (result.ok) return
    expect(result.reason).toContain('auth')
  })

  it('refuses the same model claimed twice on one endpoint', () => {
    const result = decodeMatrix([
      nativeRoute('/v1/chat/completions', ['seedream-3']),
      nativeRoute('/v1/chat/completions', ['seedream-3']),
    ])
    expect(result.ok).toBe(false)
    if (result.ok) return
    expect(result.reason).toContain('claimed twice')
  })

  it('refuses two fallback rows on one endpoint', () => {
    const result = decodeMatrix([
      nativeRoute('/v1/chat/completions'),
      nativeRoute('/v1/chat/completions'),
    ])
    expect(result.ok).toBe(false)
    if (result.ok) return
    expect(result.reason).toContain('fallback')
  })
})

describe('encodeMatrix', () => {
  it('drops reject rows because an absent route is already a rejection', () => {
    const matrix: Matrix = {
      items: [{ path: '/v1/images/edits', models: [], mode: 'reject' }],
    }
    expect(encodeMatrix(matrix)).toEqual([])
  })

  it('rebuilds the upstream path and auth for a transform row', () => {
    const matrix: Matrix = {
      items: [
        {
          path: '/v1/messages',
          models: [],
          mode: 'transform',
          converter: 'anthropic_messages_to_openai_chat_completions',
          upstreamPath: '/v1/chat/completions',
          auth: {
            type: 'header',
            name: 'Authorization',
            value: 'Bearer {api_key}',
          },
        },
      ],
    }
    expect(encodeMatrix(matrix)).toEqual([transformRoute])
  })

  it('writes the model list back onto the route', () => {
    const matrix: Matrix = {
      items: [
        {
          path: '/v1/images/generations',
          models: ['seedream-3', 'seedream-4'],
          mode: 'passthrough',
          upstreamPath: '/v1/images/generations',
        },
      ],
    }
    expect(encodeMatrix(matrix)).toEqual([
      nativeRoute('/v1/images/generations', ['seedream-3', 'seedream-4']),
    ])
  })

  it('moves fallback rows behind the model-specific rows', () => {
    const matrix: Matrix = {
      items: [
        {
          path: '/v1/chat/completions',
          models: [],
          mode: 'passthrough',
          upstreamPath: '/v1/chat/completions',
        },
        {
          path: '/v1/chat/completions',
          models: ['gpt-4o'],
          mode: 'passthrough',
          upstreamPath: '/v1/chat/completions',
        },
      ],
    }
    const routes = encodeMatrix(matrix)
    expect(routes[0].models).toEqual(['gpt-4o'])
    expect(routes[1].models).toBeUndefined()
  })

  it('keeps management routes in front', () => {
    const managementRoute: AdvancedCustomRoute = nativeRoute('/v1/models', [])
    const matrix: Matrix = {
      items: [
        {
          path: '/v1/embeddings',
          models: [],
          mode: 'passthrough',
          upstreamPath: '/v1/embeddings',
        },
      ],
    }
    expect(encodeMatrix({ ...matrix, preserved: [managementRoute] })).toEqual([
      managementRoute,
      passthroughRoute,
    ])
  })
})

describe('round trip', () => {
  it('survives decode then encode unchanged', () => {
    const routes = [passthroughRoute, transformRoute]
    const decoded = decodeMatrix(routes)
    expect(decoded.ok).toBe(true)
    if (!decoded.ok) return
    expect(encodeMatrix(decoded.matrix)).toEqual(routes)
  })

  it('carries management routes through decode and encode without a second argument', () => {
    const managementRoute = nativeRoute('/v1/models', [])
    const decoded = decodeMatrix([managementRoute, passthroughRoute])
    expect(decoded.ok).toBe(true)
    if (!decoded.ok) return
    expect(encodeMatrix(decoded.matrix)).toContainEqual(managementRoute)
  })

  it('survives encode then decode unchanged', () => {
    // 顺序按 encode 的产物写：具体模型行在前，兜底行在后。
    const matrix: Matrix = {
      items: [
        {
          path: '/v1/chat/completions',
          models: ['seedream-4'],
          mode: 'transform',
          converter: 'openai_chat_completions_to_anthropic_messages',
          upstreamPath: '/v1/messages',
          auth: { type: 'header', name: 'x-api-key', value: '{api_key}' },
        },
        {
          path: '/v1/chat/completions',
          models: ['seedream-3'],
          mode: 'passthrough',
          upstreamPath: '/v1/chat/completions',
        },
        {
          path: '/v1/embeddings',
          models: [],
          mode: 'passthrough',
          upstreamPath: '/v1/embeddings',
        },
      ],
    }
    const routes = encodeMatrix(matrix)
    const decoded = decodeMatrix(routes)
    expect(decoded.ok).toBe(true)
    if (!decoded.ok) return
    expect(decoded.matrix.items).toEqual(matrix.items)
  })

  it('survives a mixed model matrix with a fallback row', () => {
    const routes = [
      nativeRoute('/v1/images/generations', ['seedream-4']),
      nativeRoute('/v1/images/generations'),
    ]
    const decoded = decodeMatrix(routes)
    expect(decoded.ok).toBe(true)
    if (!decoded.ok) return
    expect(encodeMatrix(decoded.matrix)).toEqual(routes)
  })
})

describe('adversarial round trip', () => {
  it('accepts a route whose auth is explicitly none', () => {
    const routes: AdvancedCustomRoute[] = [
      {
        incoming_path: '/v1/embeddings',
        upstream_path: '/v1/embeddings',
        converter: 'none',
        auth: { type: 'none' },
      },
    ]
    const decoded = decodeMatrix(routes)
    expect(decoded.ok).toBe(true)
    if (!decoded.ok) return
    expect(encodeMatrix(decoded.matrix)).toEqual(routes)
  })

  it('accepts an explicit none auth on a transform row', () => {
    const routes: AdvancedCustomRoute[] = [
      {
        incoming_path: '/v1/messages',
        upstream_path: '/v1/chat/completions',
        converter: 'anthropic_messages_to_openai_chat_completions',
        auth: { type: 'none' },
      },
    ]
    const decoded = decodeMatrix(routes)
    expect(decoded.ok).toBe(true)
    if (!decoded.ok) return
    expect(encodeMatrix(decoded.matrix)).toEqual(routes)
  })

  it('treats an empty converter string as native forwarding', () => {
    const routes: AdvancedCustomRoute[] = [
      {
        incoming_path: '/v1/embeddings',
        upstream_path: '/v1/embeddings',
        converter: '' as AdvancedCustomConverter,
      },
    ]
    const decoded = decodeMatrix(routes)
    expect(decoded.ok).toBe(true)
    if (!decoded.ok) return
    expect(decoded.matrix.items[0].mode).toBe('passthrough')
  })

  it('accepts a templated incoming path and keeps it verbatim', () => {
    const routes: AdvancedCustomRoute[] = [
      {
        incoming_path: '/v1beta/models/{model}:generateContent',
        upstream_path: '/v1beta/models/{model}:generateContent',
        converter: 'none',
      },
    ]
    const decoded = decodeMatrix(routes)
    // incoming 里的 {model} 是端点模板，不是上游路径模板，允许进入矩阵。
    expect(decoded.ok).toBe(true)
    if (!decoded.ok) return
    expect(decoded.matrix.items[0].path).toBe(
      '/v1beta/models/{model}:generateContent'
    )
  })

  it('keeps a hand-edited upstream path on a transform row', () => {
    const routes: AdvancedCustomRoute[] = [
      {
        incoming_path: '/v1/messages',
        upstream_path: '/v1/custom/chat-endpoint',
        converter: 'anthropic_messages_to_openai_chat_completions',
        auth: { type: 'header', name: 'Authorization', value: 'Bearer {api_key}' },
      },
    ]
    const decoded = decodeMatrix(routes)
    expect(decoded.ok).toBe(true)
    if (!decoded.ok) return
    expect(encodeMatrix(decoded.matrix)).toEqual(routes)
  })

  it('keeps pass_through_body_enabled on a native row', () => {
    const routes: AdvancedCustomRoute[] = [
      {
        incoming_path: '/v1/embeddings',
        upstream_path: '/v1/embeddings',
        converter: 'none',
        pass_through_body_enabled: true,
      },
    ]
    const decoded = decodeMatrix(routes)
    expect(decoded.ok).toBe(true)
    if (!decoded.ok) return
    expect(encodeMatrix(decoded.matrix)).toEqual(routes)
  })

  it('does not invent an auth block on a transform row that had none', () => {
    const routes: AdvancedCustomRoute[] = [
      {
        incoming_path: '/v1/messages',
        upstream_path: '/v1/chat/completions',
        converter: 'anthropic_messages_to_openai_chat_completions',
      },
    ]
    const decoded = decodeMatrix(routes)
    expect(decoded.ok).toBe(true)
    if (!decoded.ok) return
    expect(encodeMatrix(decoded.matrix)).toEqual(routes)
  })

  it('keeps a custom upstream path on a native row', () => {
    const routes: AdvancedCustomRoute[] = [
      {
        incoming_path: '/v1/chat/completions',
        upstream_path: 'https://example.com/v1/openai/chat',
        converter: 'none',
      },
    ]
    const decoded = decodeMatrix(routes)
    expect(decoded.ok).toBe(true)
    if (!decoded.ok) return
    expect(encodeMatrix(decoded.matrix)).toEqual(routes)
  })
})

describe('validateMatrix', () => {
  it('reports the same model claimed twice on one endpoint', () => {
    const matrix: Matrix = {
      items: [
        { path: '/v1/chat/completions', models: ['a'], mode: 'reject' },
        { path: '/v1/chat/completions', models: ['a'], mode: 'reject' },
      ],
    }
    expect(validateMatrix(matrix).length).toBeGreaterThan(0)
  })

  it('allows two fallback rows on different endpoints', () => {
    const matrix: Matrix = {
      items: [
        { path: '/v1/chat/completions', models: [], mode: 'reject' },
        { path: '/v1/embeddings', models: [], mode: 'reject' },
      ],
    }
    expect(validateMatrix(matrix)).toEqual([])
  })

  it('reports a converter that cannot serve the endpoint', () => {
    const matrix: Matrix = {
      items: [
        {
          path: '/v1/embeddings',
          models: [],
          mode: 'transform',
          converter: 'anthropic_messages_to_openai_chat_completions',
        },
      ],
    }
    expect(validateMatrix(matrix)[0]).toContain('cannot be produced')
  })
})

describe('converterOptionsForPath', () => {
  it('lists the converters that can produce chat completions', () => {
    expect(converterOptionsForPath('/v1/chat/completions')).toEqual([
      'openai_chat_completions_to_anthropic_messages',
      'openai_chat_completions_to_openai_responses',
      'openai_chat_completions_to_gemini_generate_content',
    ])
  })

  it('returns nothing for a plain passthrough endpoint', () => {
    expect(converterOptionsForPath('/v1/embeddings')).toEqual([])
  })
})
