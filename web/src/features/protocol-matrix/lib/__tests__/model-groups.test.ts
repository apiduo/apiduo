import { describe, expect, it } from 'vitest'

import { classifyModel, groupModels } from '../model-groups'

describe('classifyModel', () => {
  it('recognises image families', () => {
    for (const model of [
      'doubao-seedream-5-0-flash-260915',
      'doubao-seededit-3-0-i2i-250628',
      'gpt-image-1',
      'dall-e-3',
      'flux-pro-1.1',
      'imagen-4.0-generate-001',
      'qwen-image-edit',
      'wanx2.1-t2i-turbo',
      'nano-banana',
    ]) {
      expect(classifyModel(model)).toBe('image')
    }
  })

  it('recognises video families', () => {
    for (const model of [
      'doubao-seedance-1-0-pro-250528',
      'veo-3.0-generate-001',
      'sora-2',
      'kling-v2-master',
      'wan2.1-t2v-14b',
      'hailuo-02',
      'pika-2.2',
    ]) {
      expect(classifyModel(model)).toBe('video')
    }
  })

  it('keeps text models out of the image bucket', () => {
    for (const model of [
      'gpt-4o',
      'claude-sonnet-4',
      'deepseek-v3-250324',
      'qwen-vl-max',
      'doubao-1-5-vision-pro-32k-250115',
      'gemini-2.5-pro',
      'glm-4-plus',
      'kimi-k2-250711',
    ]) {
      expect(classifyModel(model)).toBe('text')
    }
  })

  it('separates embeddings, rerank and audio', () => {
    for (const model of [
      'text-embedding-3-large',
      'bge-m3',
      'gte-Qwen2-7B-instruct',
      'bge-reranker-v2-m3',
      'whisper-1',
      'gpt-4o-audio-preview',
      'tts-1-hd',
    ]) {
      expect(classifyModel(model)).toBe('other')
    }
  })

  it('falls back to text for unknown or blank names', () => {
    expect(classifyModel('some-company-model-v1')).toBe('text')
    expect(classifyModel('')).toBe('text')
    expect(classifyModel('   ')).toBe('text')
  })
})

describe('groupModels', () => {
  it('orders text before image before video before other', () => {
    const groups = groupModels([
      'veo-3.0-generate-001',
      'gpt-4o',
      'doubao-seedream-5-0-260128',
      'text-embedding-3-large',
    ])

    expect(groups.map((group) => group.key)).toEqual([
      'text',
      'image',
      'video',
      'other',
    ])
    expect(groups.flatMap((group) => group.models)).toEqual([
      'gpt-4o',
      'doubao-seedream-5-0-260128',
      'veo-3.0-generate-001',
      'text-embedding-3-large',
    ])
  })

  it('keeps the channel order inside a group', () => {
    const groups = groupModels([
      'claude-sonnet-4',
      'gpt-4o',
      'deepseek-v3',
      'gemini-2.5-pro',
    ])
    expect(groups).toHaveLength(1)
    expect(groups[0].models).toEqual([
      'claude-sonnet-4',
      'gpt-4o',
      'deepseek-v3',
      'gemini-2.5-pro',
    ])
  })

  it('omits empty groups so a text-only channel gets one header', () => {
    const groups = groupModels(['gpt-4o', 'claude-sonnet-4'])
    expect(groups).toHaveLength(1)
    expect(groups[0].key).toBe('text')
  })

  it('drops blanks and duplicates without losing order', () => {
    const groups = groupModels(['gpt-4o', '  ', 'gpt-4o', 'seedream-4'])
    expect(groups.flatMap((group) => group.models)).toEqual([
      'gpt-4o',
      'seedream-4',
    ])
  })

  it('returns nothing for an empty list', () => {
    expect(groupModels([])).toEqual([])
  })

  it('never loses or invents models', () => {
    const models = [
      'gpt-4o',
      'doubao-seedream-5-0-flash-260915',
      'kling-v2',
      'bge-m3',
      'claude-sonnet-4',
    ]
    const flattened = groupModels(models).flatMap((group) => group.models)
    expect(flattened).toHaveLength(models.length)
    expect(new Set(flattened)).toEqual(new Set(models))
  })
})
