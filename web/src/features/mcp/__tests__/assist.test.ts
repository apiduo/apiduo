/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { describe, expect, it } from 'vitest'

import {
  buildAssistMessages,
  parseAssistReply,
  ruleAssist,
  type AssistFacts,
} from '../assist'

const FACTS: AssistFacts = {
  modelId: 'doubao-seedream-5-0-flash',
  kind: 'image',
  protocol: 'openai_image',
  async: false,
  displayName: '',
}

function reply(payload: unknown): string {
  return JSON.stringify(payload)
}

// 用抛错代替 `!`：断言失败时给的是「解析失败 + 原始输入」，
// 比一句 "Cannot read properties of null" 有用得多。
function mustParse(text: string) {
  const out = parseAssistReply(text)
  if (!out) throw new Error(`解析失败：${text.slice(0, 120)}`)
  return out
}

describe('parseAssistReply', () => {
  it('解析正常回复', () => {
    const out = mustParse(
      reply({
        summary: '文生图 / 图生图，支持 4 张参考图',
        tags: ['文生图', '图生图', '多参考图'],
        async: false,
        max_ref_images: 4,
        max_ref_videos: 0,
        max_prompt_chars: 2000,
        parameters: { size: { enum: ['1K', '2K'], default: '2K' } },
        confidence: 'high',
        notes: '来自官方文档',
      })
    )
    expect(out.source).toBe('ai')
    expect(out.confidence).toBe('high')
    expect(out.patch.summary).toBe('文生图 / 图生图，支持 4 张参考图')
    expect(out.patch.tags).toBe('文生图,图生图,多参考图')
    expect(out.patch.max_ref_images).toBe(4)
    // 0 是「明确不支持」，必须原样保留，不能当空值
    expect(out.patch.max_ref_videos).toBe(0)
    expect(out.patch.max_prompt_chars).toBe(2000)
    expect(JSON.parse(out.patch.parameters ?? '{}')).toEqual({
      size: { enum: ['1K', '2K'], default: '2K' },
    })
  })

  it('剥掉 markdown 围栏与前后废话', () => {
    const fenced =
      '```json\n{"summary":"视频模型","tags":["文生视频"]}\n```'
    expect(parseAssistReply(fenced)?.patch.summary).toBe('视频模型')

    const chatty =
      '好的，结果如下：{"summary":"视频模型","tags":["文生视频"]} 以上。'
    expect(parseAssistReply(chatty)?.patch.summary).toBe('视频模型')
  })

  it('不是 JSON 就返回 null，交给调用方降级', () => {
    expect(parseAssistReply('抱歉，我不能确定这个模型的能力。')).toBeNull()
    expect(parseAssistReply('')).toBeNull()
    expect(parseAssistReply('[1,2,3]')).toBeNull()
  })

  it('null 表示「未声明」，原样保留', () => {
    const out = mustParse(
      reply({ summary: '图像模型', max_ref_images: null })
    )
    expect(out.patch.max_ref_images).toBeNull()
  })

  it('非法数值直接丢弃，不做「修正」', () => {
    const out = mustParse(
      reply({
        summary: '图像模型',
        max_ref_images: '4',
        max_ref_videos: -1,
        max_prompt_chars: 1.5,
      })
    )
    expect(out.patch.max_ref_images).toBeUndefined()
    expect(out.patch.max_ref_videos).toBeUndefined()
    expect(out.patch.max_prompt_chars).toBeUndefined()
  })

  it('只保留后端认识的参数键，并修掉自相矛盾的组合', () => {
    const out = mustParse(
      reply({
        summary: '图像模型',
        parameters: {
          size: {
            enum: ['1K', '2K'],
            default: '4K', // 不在枚举里 → 丢掉
            description: '分辨率', // 后端没有这个键 → 丢掉
          },
          duration: { min: 10, max: 2 }, // min > max → 丢掉 max
          empty: { description: 'x' }, // 没有任何有效键 → 整条丢掉
        },
      })
    )
    const specs = JSON.parse(out.patch.parameters ?? '{}')
    expect(specs.size).toEqual({ enum: ['1K', '2K'] })
    expect(specs.duration).toEqual({ min: 10 })
    expect(specs.empty).toBeUndefined()
  })

  it('标签去重并截断到 6 个，长摘要截断', () => {
    const out = mustParse(
      reply({
        summary: '一'.repeat(500),
        tags: ['a', 'a', 'b', 'c', 'd', 'e', 'f', 'g'],
      })
    )
    expect(out.patch.tags).toBe('a,b,c,d,e,f')
    expect(out.patch.summary?.length).toBe(120)
  })

  it('一个可用字段都没有时返回 null', () => {
    expect(parseAssistReply(reply({ foo: 'bar' }))).toBeNull()
    expect(
      parseAssistReply(reply({ summary: '   ', tags: [] }))
    ).toBeNull()
  })

  it('置信度只认 high / medium，其余按 low', () => {
    expect(mustParse(reply({ summary: 'x', confidence: 'high' })).confidence)
      .toBe('high')
    expect(mustParse(reply({ summary: 'x', confidence: 'low' })).confidence)
      .toBe('low')
    expect(mustParse(reply({ summary: 'x', confidence: 'bogus' })).confidence)
      .toBe('low')
  })
})

describe('ruleAssist', () => {
  it('图像模型给文生图标签，且不猜任何上限', () => {
    const out = ruleAssist(FACTS)
    expect(out.source).toBe('rule')
    expect(out.confidence).toBe('low')
    expect(out.patch.summary).toContain('图像')
    expect(out.patch.tags).toContain('文生图')
    // 上限只能来自事实，规则层一律不填
    expect(out.patch.max_ref_images).toBeUndefined()
    expect(out.patch.max_ref_videos).toBeUndefined()
    expect(out.patch.max_prompt_chars).toBeUndefined()
    expect(out.patch.parameters).toBeUndefined()
  })

  it('视频 + 异步：标签与执行方式都跟着变', () => {
    const out = ruleAssist({
      ...FACTS,
      modelId: 'doubao-seedance-1-0-pro',
      kind: 'video',
      protocol: 'openai_video',
      async: true,
    })
    expect(out.patch.summary).toContain('视频')
    expect(out.patch.tags).toContain('文生视频')
    expect(out.patch.tags).toContain('异步任务')
    expect(out.patch.async).toBe(true)
  })
})

describe('buildAssistMessages', () => {
  it('把已知事实都带上，并明确要求不确定时给 null', () => {
    const messages = buildAssistMessages({
      ...FACTS,
      channelType: 'Doubao',
      upstreamHost: 'ark.cn-beijing.volces.com',
    })
    expect(messages).toHaveLength(2)
    const user = messages[1].content
    expect(user).toContain('doubao-seedream-5-0-flash')
    expect(user).toContain('图像')
    expect(user).toContain('openai_image')
    expect(user).toContain('Doubao')
    expect(user).toContain('ark.cn-beijing.volces.com')
    expect(messages[0].content).toContain('严禁猜测具体数值')
  })

  it('编辑已有声明时把现值带上，避免重复推荐', () => {
    const messages = buildAssistMessages({
      ...FACTS,
      current: { summary: '旧摘要', tags: '文生图', parameters: '' },
    })
    const user = messages[1].content
    expect(user).toContain('旧摘要')
    expect(user).toContain('已有的一句话描述')
  })
})
