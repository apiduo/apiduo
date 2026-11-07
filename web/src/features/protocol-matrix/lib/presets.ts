import { isAdvancedCustomManagementPath } from '@/features/channels/lib/advanced-custom'
import type {
  AdvancedCustomRoute,
  AdvancedCustomRouteAuth,
} from '@/features/channels/types'

import {
  type Grid,
  type GridCellMode,
  gridToRoutes,
  isCellEditable,
  normalizeConverter,
  setCellMode,
} from './grid'

/**
 * 常见组合的「一键铺开」。
 *
 * 和上游 `ADVANCED_CUSTOM_TEMPLATE_OPTIONS` 不是一回事：那套是**整份配置替换**
 * （all protocols / openai only / claude only / gemini only），只铺原生透传，
 * 也不含转换。这里补的恰好是它没有的两半：
 *
 *   1. 只动预设点名的端点，别的端点原样留着（可以叠加）
 *   2. 可以带转换器——「客户端说 Claude、上游其实说 OpenAI」这类接线
 *
 * 每条预设都是 endpoints 上的最小改动，不做整份覆盖，所以点错了再点一次别的
 * 预设也不会把之前的配置冲掉。
 */
export type PresetEntry = {
  endpoint: string
  mode: GridCellMode
  /** 覆盖上游路径；缺省时由转换器的默认值决定。 */
  upstreamPath?: string
  /** 覆盖鉴权；缺省时由转换器（或原路由的模板）决定。 */
  auth?: AdvancedCustomRouteAuth
  /** 覆盖 per-route 透传开关。 */
  passThroughBody?: boolean
}

export type Preset = {
  id: string
  /** 内置预设是 i18n key；自定义预设直接用名字，调用方按 `builtIn` 区分。 */
  label: string
  /** i18n key；自定义预设没有。 */
  hint?: string
  builtIn?: boolean
  entries: PresetEntry[]
}

const CHAT = '/v1/chat/completions'
const MESSAGES = '/v1/messages'
const IMAGES_GENERATIONS = '/v1/images/generations'
const IMAGES_EDITS = '/v1/images/edits'

export const MATRIX_PRESETS: Preset[] = [
  {
    id: 'image-dual',
    label: 'Image pair',
    hint: 'Forward both image endpoints, pointing edits at generations (Ark and similar merge them).',
    builtIn: true,
    entries: [
      { endpoint: IMAGES_GENERATIONS, mode: 'none' },
      {
        // 一定要开透传：edits 走 generations 时，请求体会按 dto.ImageRequest
        // 重建，`seed`、`sequential_image_generation` 这些厂商私有字段会被
        // 静默丢掉——返回 200、图不对，最难查的一类错。实测同一请求开/关
        // 透传，上游收到 163 字节 vs 147 字节，差的正是这些字段。
        endpoint: IMAGES_EDITS,
        mode: 'none',
        upstreamPath: IMAGES_GENERATIONS,
        passThroughBody: true,
      },
    ],
  },
  {
    id: 'claude-client',
    label: 'Claude → OpenAI',
    hint: 'Clients call /v1/messages, the upstream speaks OpenAI Chat.',
    builtIn: true,
    entries: [
      {
        endpoint: MESSAGES,
        mode: 'anthropic_messages_to_openai_chat_completions',
      },
    ],
  },
  {
    id: 'claude-upstream',
    label: 'OpenAI → Claude',
    hint: 'Clients call /v1/chat/completions, the upstream speaks Anthropic Messages.',
    builtIn: true,
    entries: [
      {
        endpoint: CHAT,
        mode: 'openai_chat_completions_to_anthropic_messages',
      },
    ],
  },
  {
    id: 'gemini-upstream',
    label: 'OpenAI → Gemini',
    hint: 'Clients call /v1/chat/completions, the upstream speaks Gemini generateContent.',
    builtIn: true,
    entries: [
      {
        endpoint: CHAT,
        mode: 'openai_chat_completions_to_gemini_generate_content',
      },
    ],
  },
  {
    id: 'responses-upstream',
    label: 'OpenAI → Responses',
    hint: 'Clients call /v1/chat/completions, the upstream speaks OpenAI Responses.',
    builtIn: true,
    entries: [
      {
        endpoint: CHAT,
        mode: 'openai_chat_completions_to_openai_responses',
      },
    ],
  },
]

export function findPreset(id: string): Preset | undefined {
  return MATRIX_PRESETS.find((preset) => preset.id === id)
}

/** 表格里没有这个端点时补一行（全 off），否则预设没法落在它上面。 */
function ensureEndpoint(grid: Grid, endpoint: string): Grid {
  if (grid.endpoints.includes(endpoint)) return grid

  const cells = [...grid.cells]
  for (const model of grid.models) {
    cells.push({ endpoint, model, mode: 'off' })
  }
  // cells 是行优先的二维摊平，追加整行不会破坏 `row * models.length` 的索引。
  return { ...grid, endpoints: [...grid.endpoints, endpoint], cells }
}

/**
 * 只改预设点名的端点，其余端点一格不动。
 *
 * 冲突端点（正则规则 / 转换器服务不了）跳过：它的格子改不动路由，
 * 改了只会让界面和保存结果对不上。
 */
export function applyPreset(grid: Grid, preset: Preset): Grid {
  let next = grid

  for (const entry of preset.entries) {
    if (!isCellEditable(next, entry.endpoint)) continue
    next = ensureEndpoint(next, entry.endpoint)
    for (const model of next.models) {
      next = setCellMode(next, entry.endpoint, model, entry.mode)
    }
  }

  return next
}

/**
 * 预设 → 路由。
 *
 * 上游路径 / 鉴权 / 透传开关的覆盖在 `gridToRoutes` 之后打补丁，因为这几个字段
 * 不属于格子的状态（格子只记模式）。打完之后它就是普通路由，后续任何编辑都会
 * 把它当模板原样沿用——所以补丁只需要打一次。
 *
 * 同一端点出现多条 entry 时只认第一条：模板是「这个端点怎么接」的形状，
 * 一个端点两种转换器属于渠道个例，不是模板能表达的东西。
 */
export function presetToRoutes(
  grid: Grid,
  preset: Preset,
  previous: AdvancedCustomRoute[] | undefined
): AdvancedCustomRoute[] {
  const routes = gridToRoutes(applyPreset(grid, preset), previous)

  const overrides = new Map<string, PresetEntry>()
  for (const entry of preset.entries) {
    if (!overrides.has(entry.endpoint)) overrides.set(entry.endpoint, entry)
  }

  return routes.map((route) => {
    const entry = overrides.get((route.incoming_path ?? '').trim())
    if (!entry) return route
    return {
      ...route,
      ...(entry.upstreamPath ? { upstream_path: entry.upstreamPath } : {}),
      ...(entry.auth ? { auth: entry.auth } : {}),
      ...(entry.passThroughBody ? { pass_through_body_enabled: true } : {}),
    }
  })
}

/**
 * 把当前路由收成一份模板。
 *
 * 丢 `models`：模板是形状，不是模型清单——套到新渠道时模型名几乎一定不同，
 * 留着只会生成一堆指向不存在模型的路由。
 *
 * 同一端点只取第一条（理由同上）。管理路由（模型列表、余额查询）不进模板。
 */
export function routesToPresetEntries(
  routes: AdvancedCustomRoute[] | undefined
): PresetEntry[] {
  const seen = new Set<string>()
  const entries: PresetEntry[] = []

  for (const route of routes ?? []) {
    const endpoint = (route.incoming_path ?? '').trim()
    if (!endpoint || isAdvancedCustomManagementPath(endpoint)) continue
    if (seen.has(endpoint)) continue
    seen.add(endpoint)

    const upstream = (route.upstream_path ?? '').trim()
    entries.push({
      endpoint,
      mode: normalizeConverter(route),
      ...(upstream ? { upstreamPath: upstream } : {}),
      ...(route.auth ? { auth: route.auth } : {}),
      ...(route.pass_through_body_enabled ? { passThroughBody: true } : {}),
    })
  }

  return entries
}
