import { beforeEach, describe, expect, it, vi } from 'vitest'

import {
  ROUTE_PRESET_KEY_PREFIX,
  type StoredPreset,
  createPreset,
  deletePreset,
  invalidatePresetCache,
  listPresets,
  newPresetId,
  parsePresets,
  toPreset,
} from '../preset-api'

const { get, put } = vi.hoisted(() => ({ get: vi.fn(), put: vi.fn() }))

vi.mock('@/lib/api', () => ({ api: { get, put } }))

/** 一次成功的 `/api/option/` 读：把模板行包成接口形状。 */
function optionResponse(presets: StoredPreset[]) {
  return {
    data: {
      data: presets.map((preset) =>
        row(preset.id, { name: preset.name, entries: preset.entries })
      ),
    },
  }
}

const entries = [
  { endpoint: '/v1/images/generations', mode: 'none' as const },
  {
    endpoint: '/v1/images/edits',
    mode: 'none' as const,
    upstreamPath: '/v1/images/generations',
  },
]

function row(id: string, value: unknown) {
  return {
    key: `${ROUTE_PRESET_KEY_PREFIX}${id}`,
    value: typeof value === 'string' ? value : JSON.stringify(value),
  }
}

const valid: StoredPreset = { id: 'p1', name: 'Ark images', entries }

describe('parsePresets', () => {
  it('returns nothing for shapes that are not a list', () => {
    expect(parsePresets(undefined)).toEqual([])
    expect(parsePresets(null)).toEqual([])
    expect(parsePresets({})).toEqual([])
    expect(parsePresets('nope')).toEqual([])
  })

  it('ignores option rows that are not presets', () => {
    const rows = [
      { key: 'RetryTimes', value: '3' },
      { key: 'apiduo.appearance', value: '{"enabled":true}' },
    ]
    expect(parsePresets(rows)).toEqual([])
  })

  it('keeps a well formed row', () => {
    expect(parsePresets([row('p1', { name: 'Ark images', entries })])).toEqual([
      valid,
    ])
  })

  it('treats an empty value as a tombstone', () => {
    expect(parsePresets([row('p1', '')])).toEqual([])
    expect(parsePresets([row('p1', '   ')])).toEqual([])
  })

  it('drops rows with broken JSON or no usable entry', () => {
    const rows = [
      row('bad-json', '{not json'),
      row('no-entries', { name: 'x', entries: [] }),
      row('bad-mode', {
        name: 'x',
        entries: [{ endpoint: '/v1/chat/completions', mode: 'nope' }],
      }),
      row('no-name', { name: '  ', entries }),
      row('ok', { name: 'ok', entries }),
    ]
    expect(parsePresets(rows).map((preset) => preset.id)).toEqual(['ok'])
  })

  it('drops entries whose endpoint does not start with a slash', () => {
    const rows = [
      row('mixed', {
        name: 'mixed',
        entries: [
          { endpoint: 'v1/chat/completions', mode: 'none' },
          { endpoint: '/v1/chat/completions', mode: 'none' },
        ],
      }),
    ]
    expect(parsePresets(rows)[0].entries).toEqual([
      { endpoint: '/v1/chat/completions', mode: 'none' },
    ])
  })

  it('orders by id, which is creation order', () => {
    const rows = [
      row('pb', { name: 'second', entries }),
      row('pa', { name: 'first', entries }),
    ]
    expect(parsePresets(rows).map((preset) => preset.name)).toEqual([
      'first',
      'second',
    ])
  })

  it('keeps auth and pass-through when stored', () => {
    const rows = [
      row('wired', {
        name: 'wired',
        entries: [
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
        ],
      }),
    ]
    expect(parsePresets(rows)[0].entries[0]).toEqual({
      endpoint: '/v1/messages',
      mode: 'anthropic_messages_to_openai_chat_completions',
      upstreamPath: '/v1/chat/completions',
      auth: {
        type: 'header',
        name: 'Authorization',
        value: 'Bearer {api_key}',
      },
      passThroughBody: true,
    })
  })
})

describe('newPresetId', () => {
  it('produces distinct ids prefixed with p', () => {
    const first = newPresetId()
    const second = newPresetId()
    expect(first).not.toBe(second)
    expect(first.startsWith('p')).toBe(true)
  })
})

describe('toPreset', () => {
  it('namespaces the id so a custom preset cannot shadow a built-in one', () => {
    const preset = toPreset(valid)
    expect(preset.id).toBe('custom:p1')
    expect(preset.builtIn).toBe(false)
    expect(preset.label).toBe('Ark images')
    expect(preset.entries).toEqual(entries)
  })
})

describe('listPresets cache', () => {
  beforeEach(() => {
    get.mockReset()
    put.mockReset()
    invalidatePresetCache()
  })

  it('reads the whole option map once, then serves from memory', async () => {
    get.mockResolvedValue(optionResponse([valid]))

    expect(await listPresets()).toEqual([valid])
    expect(await listPresets()).toEqual([valid])
    expect(get).toHaveBeenCalledTimes(1)
  })

  it('shares one request between concurrent callers', async () => {
    get.mockResolvedValue(optionResponse([valid]))

    const [first, second] = await Promise.all([listPresets(), listPresets()])
    expect(first).toEqual([valid])
    expect(second).toEqual([valid])
    expect(get).toHaveBeenCalledTimes(1)
  })

  it('refetches after the cache is invalidated or forced', async () => {
    get.mockResolvedValue(optionResponse([valid]))

    await listPresets()
    invalidatePresetCache()
    await listPresets()
    await listPresets({ force: true })
    expect(get).toHaveBeenCalledTimes(3)
  })

  it('expires on its own so another admin\'s edit shows up', async () => {
    const clock = vi.spyOn(Date, 'now').mockReturnValue(1_000_000)
    get.mockResolvedValue(optionResponse([valid]))

    await listPresets()
    clock.mockReturnValue(1_000_000 + 59_000)
    await listPresets()
    expect(get).toHaveBeenCalledTimes(1)

    clock.mockReturnValue(1_000_000 + 61_000)
    await listPresets()
    expect(get).toHaveBeenCalledTimes(2)
    clock.mockRestore()
  })

  it('retries after a failed read instead of caching the rejection', async () => {
    get.mockRejectedValueOnce(new Error('offline')).mockResolvedValue(optionResponse([valid]))

    await expect(listPresets()).rejects.toThrow('offline')
    expect(await listPresets()).toEqual([valid])
    expect(get).toHaveBeenCalledTimes(2)
  })

  it('keeps a freshly created preset without another read', async () => {
    get.mockResolvedValue(optionResponse([valid]))
    put.mockResolvedValue({})

    await listPresets()
    const created = await createPreset('New one', entries)
    const all = await listPresets()

    expect(all.map((preset) => preset.name)).toEqual(['Ark images', 'New one'])
    expect(all.at(-1)?.id).toBe(created.id)
    expect(get).toHaveBeenCalledTimes(1)
  })

  it('drops a deleted preset from the cache and writes a tombstone', async () => {
    get.mockResolvedValue(optionResponse([valid]))
    put.mockResolvedValue({})

    await listPresets()
    await deletePreset(valid.id)

    expect(put).toHaveBeenCalledWith('/api/option/', {
      key: `${ROUTE_PRESET_KEY_PREFIX}${valid.id}`,
      value: '',
    })
    expect(await listPresets()).toEqual([])
    expect(get).toHaveBeenCalledTimes(1)
  })
})
