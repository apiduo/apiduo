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

  it('读取启用态与路径，缺 upstream 时回落到默认布局', () => {
    const config = readImageProtocol(
      JSON.stringify({
        image_protocol: {
          enabled: true,
          primary_path: ' /v1/images/custom ',
        },
      })
    )
    expect(config).toEqual({
      enabled: true,
      upstream: 'seedream',
      primary_path: '/v1/images/custom',
      edit_path: '',
    })
  })
})

describe('writeImageProtocol', () => {
  it('启用时剪掉空路径并保留其它设置', () => {
    const next = JSON.parse(
      writeImageProtocol(
        JSON.stringify({ primary_endpoint: '/v1/images/generations' }),
        {
          enabled: true,
          upstream: 'gpt-image-2',
          primary_path: '  ',
          edit_path: '/v1/images/edits',
        }
      )
    )
    expect(next.primary_endpoint).toBe('/v1/images/generations')
    expect(next.image_protocol).toEqual({
      enabled: true,
      upstream: 'gpt-image-2',
      edit_path: '/v1/images/edits',
    })
  })

  it('关闭时删键而不是写 enabled:false', () => {
    const raw = JSON.stringify({
      image_protocol: { enabled: true, upstream: 'seedream' },
      primary_endpoint: '/v1/images/generations',
    })
    const next = JSON.parse(writeImageProtocol(raw, null))
    expect('image_protocol' in next).toBe(false)
    expect(next.primary_endpoint).toBe('/v1/images/generations')
  })

  it('空上游回落默认值', () => {
    const next = JSON.parse(writeImageProtocol('{}', { enabled: true }))
    expect(next.image_protocol.upstream).toBe('seedream')
  })
})
