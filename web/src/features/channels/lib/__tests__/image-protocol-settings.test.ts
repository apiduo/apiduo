import { describe, expect, it } from 'vitest'

import {
  readImageProtocol,
  writeImageProtocol,
} from '../image-protocol-settings'

describe('readImageProtocol', () => {
  it('没有配置时返回 null', () => {
    expect(readImageProtocol(undefined)).toBeNull()
    expect(readImageProtocol('{}')).toBeNull()
    expect(readImageProtocol('not json')).toBeNull()
  })

  // 0.0.74 之前存储值写的是型号名。后端在读取侧兼容了（否则请求 400），
  // 前端不归一的话下拉找不到匹配项，界面会直接显示一个裸的 `seedream`。
  it('旧型号名归一成协议名', () => {
    const legacy = (upstream: string) =>
      readImageProtocol(
        JSON.stringify({ image_protocol: { enabled: true, upstream } })
      )?.upstream
    expect(legacy('seedream')).toBe('ark')
    expect(legacy('gpt-image-2')).toBe('openai')
    expect(legacy(' ARK ')).toBe('ark')
    expect(legacy('openai')).toBe('openai')
  })

  it('读取启用态与从端点，缺 upstream 时回落到默认布局', () => {
    const config = readImageProtocol(
      JSON.stringify({
        image_protocol: {
          enabled: true,
          edit_path: ' /v1/images/custom-edits ',
        },
      })
    )
    expect(config).toEqual({
      enabled: true,
      upstream: 'ark',
      edit_path: '/v1/images/custom-edits',
    })
  })

  // primary_path 不再是协议字段：主端点只有 settings.primary_endpoint 一处。
  // 老配置里残留的值读的时候就丢掉，不能让它再影响界面。
  it('忽略历史 primary_path', () => {
    const config = readImageProtocol(
      JSON.stringify({
        image_protocol: { enabled: true, primary_path: '/v1/images/legacy' },
      })
    )
    expect(config).not.toBeNull()
    expect(config as unknown as Record<string, unknown>).not.toHaveProperty(
      'primary_path'
    )
  })
})

describe('writeImageProtocol', () => {
  it('启用时剪掉空路径并保留其它设置', () => {
    const next = JSON.parse(
      writeImageProtocol(
        JSON.stringify({ primary_endpoint: '/v1/images/generations' }),
        {
          enabled: true,
          upstream: 'openai',
          edit_path: '/v1/images/edits',
        }
      )
    )
    expect(next.primary_endpoint).toBe('/v1/images/generations')
    expect(next.image_protocol).toEqual({
      enabled: true,
      upstream: 'openai',
      edit_path: '/v1/images/edits',
    })
  })

  it('关闭时删键而不是写 enabled:false', () => {
    const raw = JSON.stringify({
      image_protocol: { enabled: true, upstream: 'ark' },
      primary_endpoint: '/v1/images/generations',
    })
    const next = JSON.parse(writeImageProtocol(raw, null))
    expect('image_protocol' in next).toBe(false)
    expect(next.primary_endpoint).toBe('/v1/images/generations')
  })

  it('空上游回落默认值', () => {
    const next = JSON.parse(writeImageProtocol('{}', { enabled: true }))
    expect(next.image_protocol.upstream).toBe('ark')
  })
})
