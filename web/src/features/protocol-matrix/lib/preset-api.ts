import { ADVANCED_CUSTOM_CONVERTER_OPTIONS } from '@/features/channels/lib/advanced-custom'
import type { AdvancedCustomRouteAuth } from '@/features/channels/types'
import { api } from '@/lib/api'

import type { GridCellMode } from './grid'
import type { Preset, PresetEntry } from './presets'

/**
 * 自定义模板的读写：一条模板 = `options` 表里的一行。
 *
 * 为什么不塞进一行 JSON：那样每次保存都会把整份列表覆盖掉，两个人同时改，
 * 后写的把先写的吃掉。一条一行，增删各自原子——而这正是 `options` 表本来的形状
 * （key → value），不需要新表、新接口。
 *
 * 为什么不建独立表：模板是管理员写的几十条配置，不是业务记录。`options` 已经是
 * 一张 key-value 配置表，能复用就别加表——加表要迁移、要控制器、要路由，换来的
 * 只是 `created_at` 这类字段。
 *
 * 删除没有 DELETE 接口，用**空值当墓碑**（`value: ""`），读的时候过滤掉。
 * 代价是库里留下空行；比起为删一条配置开一个能删任意 option 的接口，这个代价更小。
 */
export const ROUTE_PRESET_KEY_PREFIX = 'apiduo.route_preset.'

const KNOWN_CONVERTERS = new Set<string>(
  ADVANCED_CUSTOM_CONVERTER_OPTIONS.map((option) => option.value)
)

export type StoredPreset = {
  /** 就是 key 的后缀，随 key 一起持久化，不需要额外分配。 */
  id: string
  name: string
  entries: PresetEntry[]
}

function presetKey(id: string): string {
  return `${ROUTE_PRESET_KEY_PREFIX}${id}`
}

/** 时间戳打头，按 id 排序就是创建顺序。 */
export function newPresetId(): string {
  return `p${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}`
}

function isMode(value: unknown): value is GridCellMode {
  return (
    value === 'off' || (typeof value === 'string' && KNOWN_CONVERTERS.has(value))
  )
}

function parseAuth(value: unknown): AdvancedCustomRouteAuth | undefined {
  if (!value || typeof value !== 'object') return undefined
  const auth = value as Record<string, unknown>
  if (auth.type !== 'none' && auth.type !== 'header' && auth.type !== 'query') {
    return undefined
  }
  const name = typeof auth.name === 'string' ? auth.name.trim() : ''
  const secret = typeof auth.value === 'string' ? auth.value.trim() : ''
  return {
    type: auth.type,
    ...(name ? { name } : {}),
    ...(secret ? { value: secret } : {}),
  }
}

function parseEntry(value: unknown): PresetEntry | null {
  if (!value || typeof value !== 'object') return null
  const entry = value as Record<string, unknown>

  const endpoint = typeof entry.endpoint === 'string' ? entry.endpoint.trim() : ''
  if (!endpoint.startsWith('/')) return null
  if (!isMode(entry.mode)) return null

  const upstream =
    typeof entry.upstreamPath === 'string' ? entry.upstreamPath.trim() : ''
  const auth = parseAuth(entry.auth)

  return {
    endpoint,
    mode: entry.mode,
    ...(upstream ? { upstreamPath: upstream } : {}),
    ...(auth ? { auth } : {}),
    ...(entry.passThroughBody === true ? { passThroughBody: true } : {}),
  }
}

/** 按 id 排＝按创建时间排。缓存就地增删后也要调它，顺序才不会和重新拉取的不一致。 */
function sorted(presets: StoredPreset[]): StoredPreset[] {
  return [...presets].sort((left, right) => left.id.localeCompare(right.id))
}

/**
 * `/api/option/` 的响应 → 可用的模板列表。
 *
 * 只认前缀匹配的键；空值是墓碑（删除标记），直接跳过。坏行单独丢弃，
 * 不连累整条预设条——库里可能是旧版本或人工改过的值。
 */
export function parsePresets(rows: unknown): StoredPreset[] {
  if (!Array.isArray(rows)) return []

  const presets: StoredPreset[] = []
  const seen = new Set<string>()

  for (const item of rows) {
    if (!item || typeof item !== 'object') continue
    const { key, value } = item as { key?: unknown; value?: unknown }
    if (typeof key !== 'string' || !key.startsWith(ROUTE_PRESET_KEY_PREFIX)) {
      continue
    }

    const id = key.slice(ROUTE_PRESET_KEY_PREFIX.length).trim()
    if (!id || seen.has(id)) continue
    if (typeof value !== 'string' || !value.trim()) continue

    let parsed: unknown
    try {
      parsed = JSON.parse(value)
    } catch {
      continue
    }
    if (!parsed || typeof parsed !== 'object') continue

    const raw = parsed as Record<string, unknown>
    const name = typeof raw.name === 'string' ? raw.name.trim() : ''
    if (!name) continue

    const entries = (Array.isArray(raw.entries) ? raw.entries : [])
      .map(parseEntry)
      .filter((entry): entry is PresetEntry => entry !== null)
    if (entries.length === 0) continue

    seen.add(id)
    presets.push({ id, name, entries })
  }

  return sorted(presets)
}

/** 存储形态 → 可应用的预设。id 加前缀，避免和内置的撞。 */
export function toPreset(stored: StoredPreset): Preset {
  return {
    id: `custom:${stored.id}`,
    label: stored.name,
    entries: stored.entries,
    builtIn: false,
  }
}

/**
 * 缓存。
 *
 * `/api/option/` 返回的是**整份**配置——本机实测 270 行 / 45 KB，其中 81% 的字节
 * 是 `CompletionRatioMeta`、`ModelRatio` 这些模板一个字都用不上的行。而矩阵每切进来
 * 一次就要读一次：Tabs 的非激活面板会被卸载，来回切三次就是三次整包（实测 4 次
 * 切换 = 4 个请求 / 183 KB），只为读出 1 行模板。
 *
 * 只缓存解析后的结果，不缓存原始响应：反正每次都要靠 `parsePresets` 过滤，
 * 留一份原文只是多占内存。
 *
 * TTL 只是兜住「别人改了、我没改」这一种情况。自己增删会就地更新缓存，
 * 所以操作的人永远看不到自己的写还没生效。
 */
const CACHE_TTL_MS = 60_000

let cache: { at: number; presets: StoredPreset[] } | null = null
// 并发去重：两个组件同时挂载时只发一次请求。
let inflight: Promise<StoredPreset[]> | null = null

/** 丢掉缓存。正常增删由下面两个写函数自己维护，这里给「强制刷新」和测试用。 */
export function invalidatePresetCache(): void {
  cache = null
}

export async function listPresets(
  options: { force?: boolean } = {}
): Promise<StoredPreset[]> {
  if (!options.force && cache && Date.now() - cache.at < CACHE_TTL_MS) {
    return cache.presets
  }
  if (inflight) return inflight

  inflight = (async () => {
    try {
      const res = await api.get<{ data?: unknown }>('/api/option/')
      const presets = parsePresets(res.data?.data)
      cache = { at: Date.now(), presets }
      return presets
    } finally {
      // 失败也要清掉，否则一次网络抖动会让后续调用一直拿到同一个 rejected promise。
      inflight = null
    }
  })()
  return inflight
}

export async function createPreset(
  name: string,
  entries: PresetEntry[]
): Promise<StoredPreset> {
  const id = newPresetId()
  await api.put('/api/option/', {
    key: presetKey(id),
    value: JSON.stringify({ name, entries }),
  })
  const created = { id, name, entries }
  // 就地插进缓存：下一次 listPresets 不必再拉整包，也就不会出现「刚存的模板要等一拍」。
  if (cache) cache = { at: cache.at, presets: sorted([...cache.presets, created]) }
  return created
}

export async function deletePreset(id: string): Promise<void> {
  await api.put('/api/option/', { key: presetKey(id), value: '' })
  // 墓碑写完之后，缓存里那条也得跟着消失，否则界面会把它继续画出来。
  if (cache) {
    cache = { at: cache.at, presets: cache.presets.filter((preset) => preset.id !== id) }
  }
}
