import { describe, expect, it } from 'vitest'

import type { AdvancedCustomRoute } from '@/features/channels/types'

import {
  addDerivation,
  applyPrimaryEndpoint,
  buildDerivations,
  patchDerivation,
  previewRoutePlan,
  readPrimaryEndpoint,
  removeDerivation,
  resolvePrimaryEndpoint,
  writePrimaryEndpoint,
} from '../primary-endpoint'

const routes = (): AdvancedCustomRoute[] => [
  {
    incoming_path: '/v1/images/generations',
    upstream_path: '/v1/images/generations',
    converter: 'none',
  },
  {
    incoming_path: '/v1/images/edits',
    upstream_path: '/v1/images/generations',
    converter: 'none',
    pass_through_body_enabled: true,
  },
  {
    incoming_path: '/v1/chat/completions',
    upstream_path: '/v1/images/generations',
    converter: 'openai_chat_completions_to_gemini_generate_content',
  },
]

describe('resolvePrimaryEndpoint', () => {
  it('取出现次数最多的上游路径', () => {
    expect(resolvePrimaryEndpoint(routes())).toBe('/v1/images/generations')
  })

  it('忽略管理面路由', () => {
    const withManagement: AdvancedCustomRoute[] = [
      ...routes(),
      { incoming_path: '/api/advanced-custom/models', upstream_path: '/v1/models' },
    ]
    expect(resolvePrimaryEndpoint(withManagement)).toBe('/v1/images/generations')
  })

  it('没有可用路由时返回空串', () => {
    expect(resolvePrimaryEndpoint([])).toBe('')
  })
})

describe('declared primary endpoint', () => {
  it('声明的主端点优先于路由众数', () => {
    expect(resolvePrimaryEndpoint(routes(), '/v1/images/edits')).toBe(
      '/v1/images/edits'
    )
  })

  it('空串仍然回落到众数（兼容旧配置）', () => {
    expect(resolvePrimaryEndpoint(routes(), '  ')).toBe('/v1/images/generations')
  })

  it('从 settings JSON 读主端点，坏 JSON 当没配', () => {
    expect(readPrimaryEndpoint('{"primary_endpoint":"/v1/images/edits"}')).toBe(
      '/v1/images/edits'
    )
    expect(readPrimaryEndpoint('not json')).toBe('')
    expect(readPrimaryEndpoint(undefined)).toBe('')
  })

  it('写回 settings 时保留其它字段，空值删除该键', () => {
    const withOther = JSON.stringify({
      image_protocol: { enabled: true },
      primary_endpoint: '/v1/chat/completions',
    })
    const next = JSON.parse(writePrimaryEndpoint(withOther, '/v1/images/edits'))
    expect(next.primary_endpoint).toBe('/v1/images/edits')
    expect(next.image_protocol).toEqual({ enabled: true })

    const cleared = JSON.parse(writePrimaryEndpoint(withOther, ''))
    expect('primary_endpoint' in cleared).toBe(false)
    expect(cleared.image_protocol).toEqual({ enabled: true })
  })
})

describe('buildDerivations', () => {
  it('同一入口的多条路由合并成一行，并保留模型范围', () => {
    const rows = buildDerivations(
      [
        {
          incoming_path: '/v1/chat/completions',
          upstream_path: '/v1/chat/completions',
          models: ['gpt-4o'],
        },
        {
          incoming_path: '/v1/chat/completions',
          upstream_path: '/v1/chat/completions',
          models: ['gpt-4o-mini'],
          pass_through_body_enabled: true,
        },
      ],
      '/v1/chat/completions'
    )
    expect(rows).toHaveLength(1)
    expect(rows[0].models).toEqual(['gpt-4o', 'gpt-4o-mini'])
    expect(rows[0].passThrough).toBe(true)
    expect(rows[0].followsPrimary).toBe(true)
  })

  it('上游路径与主端点不同时标记为自定义', () => {
    const rows = buildDerivations(
      [
        {
          incoming_path: '/v1/images/edits',
          upstream_path: '/v1/images/edits',
          converter: 'none',
        },
      ],
      '/v1/images/generations'
    )
    expect(rows[0].followsPrimary).toBe(false)
    expect(rows[0].upstreamPath).toBe('/v1/images/edits')
  })
})

describe('applyPrimaryEndpoint', () => {
  it('换主端点只改跟随主端点的行，显式覆盖保留', () => {
    const input: AdvancedCustomRoute[] = [
      {
        incoming_path: '/v1/images/generations',
        upstream_path: '/v1/images/generations',
      },
      {
        incoming_path: '/v1/images/edits',
        upstream_path: '/v1/images/generations',
      },
      {
        incoming_path: '/v1/chat/completions',
        upstream_path: '/v1/chat/completions',
      },
    ]
    const next = applyPrimaryEndpoint(input, '/v1/images/edits')
    expect(next[0].upstream_path).toBe('/v1/images/edits')
    expect(next[1].upstream_path).toBe('/v1/images/edits')
    // 原主端点是 generations，chat 自己指向 chat 属于显式覆盖，不能被冲掉。
    expect(next[2].upstream_path).toBe('/v1/chat/completions')
  })
})

describe('patchDerivation / addDerivation / removeDerivation', () => {
  it('按入口整组修改', () => {
    const next = patchDerivation(routes(), '/v1/images/edits', {
      passThrough: false,
      converter: 'openai_chat_completions_to_openai_responses',
    })
    const edited = next.filter(
      (route) => route.incoming_path === '/v1/images/edits'
    )
    expect(edited.every((route) => route.pass_through_body_enabled === false)).toBe(
      true
    )
    expect(
      edited.every(
        (route) =>
          route.converter === 'openai_chat_completions_to_openai_responses'
      )
    ).toBe(true)
  })

  it('新增派生跟随主端点且不带透传', () => {
    const next = addDerivation(routes(), '/v1/responses', '/v1/images/generations')
    const added = next.at(-1)
    expect(added).toBeDefined()
    expect(added?.upstream_path).toBe('/v1/images/generations')
    expect(added?.pass_through_body_enabled).toBeUndefined()
  })

  it('删除派生只删该入口', () => {
    const next = removeDerivation(routes(), '/v1/chat/completions')
    expect(next.map((route) => route.incoming_path)).toEqual([
      '/v1/images/generations',
      '/v1/images/edits',
    ])
  })
})

describe('previewRoutePlan', () => {
  it('edits 透传 + 主端点是 edits ⇒ multipart 原生转发', () => {
    const plan = previewRoutePlan(
      [
        {
          incoming_path: '/v1/images/edits',
          upstream_path: '/v1/images/edits',
          pass_through_body_enabled: true,
        },
      ],
      { incomingPath: '/v1/images/edits' }
    )
    expect(plan.matched).toBe(true)
    expect(plan.carrier).toBe('multipart')
    expect(plan.warnings).toContain('edits-passthrough-native')
  })

  it('edits 透传但主端点是 generations ⇒ 明确报警', () => {
    const plan = previewRoutePlan(routes(), { incomingPath: '/v1/images/edits' })
    expect(plan.upstreamPath).toBe('/v1/images/generations')
    expect(plan.warnings).toContain('edits-passthrough-to-json-upstream')
  })

  it('edits 不透传 ⇒ 表单会被重建为 JSON', () => {
    const plan = previewRoutePlan(
      [
        {
          incoming_path: '/v1/images/edits',
          upstream_path: '/v1/images/edits',
        },
      ],
      { incomingPath: '/v1/images/edits' }
    )
    expect(plan.carrier).toBe('json')
    expect(plan.warnings).toContain('edits-rebuilt-to-json')
  })

  it('chat 打到图片端点时提示需要图片协议', () => {
    const plan = previewRoutePlan(routes(), {
      incomingPath: '/v1/chat/completions',
      model: 'flux-3-image',
    })
    expect(plan.matched).toBe(true)
    expect(plan.warnings).toContain('chat-to-image')
  })

  it('没有匹配路由时回退兜底并标记', () => {
    const plan = previewRoutePlan(
      [{ incoming_path: '', upstream_path: '/v1/chat/completions' }],
      { incomingPath: '/v1/embeddings' }
    )
    expect(plan.matched).toBe(true)
    expect(plan.warnings).toContain('catch-all')
  })

  it('完全无路由时 matched=false', () => {
    const plan = previewRoutePlan([], { incomingPath: '/v1/images/edits' })
    expect(plan.matched).toBe(false)
    expect(plan.warnings).toEqual(['no-route'])
    expect(plan.carrier).toBe('multipart')
  })
})
