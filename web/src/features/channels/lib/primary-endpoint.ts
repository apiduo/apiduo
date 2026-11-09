import { isAdvancedCustomManagementPath } from './advanced-custom'
import type {
  AdvancedCustomConverter,
  AdvancedCustomRoute,
} from '../types'

/**
 * 主端点 + 派生端点。
 *
 * 第一性原理：一个渠道对上游只讲一种「母语」（主端点），其余入口都是它的投影。
 * 现在存量的 `advanced_routes` 是「每条入口一行」，把结果当配置存了下来——于是同一件事
 * 要在每条路由上分别表达，改一次主端点就得逐条改，必然漂移。
 *
 * 这一层不改存储，只把「结果」反推回「主端点 + 派生」，让 UI 能按一个主端点去读、去写；
 * 派生行的上游路径默认跟随主端点，只有显式覆盖才保留自己的值。
 *
 * 三条硬规则（与 skill `openai-image-adapter` 的转换点一致）：
 *   1. 上游路径默认 = 主端点，不做逐条手填；
 *   2. 载体（文件流 / JSON）是入口属性，不是路由的自由度——edits 只有透传才保留
 *      multipart，否则会被重建成 JSON；
 *   3. 预览必须能说出「实际会发到哪、发什么形态」，否则用户只能靠生产环境验证。
 */

export type RouteDerivation = {
  incomingPath: string
  routeIndexes: number[]
  /** 实际生效的上游路径；空路由时等于主端点。 */
  upstreamPath: string
  /** 上游路径跟着主端点走（没有显式覆盖）。 */
  followsPrimary: boolean
  converter: AdvancedCustomConverter
  passThrough: boolean
  /** 该入口限定的模型；空数组表示所有模型。 */
  models: string[]
}

export type RoutePlanWarning =
  | 'no-route'
  | 'catch-all'
  | 'edits-passthrough-native'
  | 'edits-rebuilt-to-json'
  | 'edits-passthrough-to-json-upstream'
  | 'chat-to-image'
  | 'converter'

export type RoutePlan = {
  matched: boolean
  incomingPath: string
  upstreamPath: string
  converter: AdvancedCustomConverter
  passThrough: boolean
  carrier: 'multipart' | 'json'
  warnings: RoutePlanWarning[]
}

function incomingPathOf(route: AdvancedCustomRoute): string {
  return (route.incoming_path || '').trim()
}

function upstreamPathOf(route: AdvancedCustomRoute): string {
  return (route.upstream_path || '').trim()
}

function normalizeConverter(route: AdvancedCustomRoute): AdvancedCustomConverter {
  return ((route.converter || 'none').trim() || 'none') as AdvancedCustomConverter
}

function isEdits(path: string): boolean {
  return path.replace(/\/+$/, '').endsWith('/v1/images/edits')
}

function isGenerations(path: string): boolean {
  return path.replace(/\/+$/, '').endsWith('/v1/images/generations')
}

function isChat(path: string): boolean {
  return path.replace(/\/+$/, '').endsWith('/v1/chat/completions')
}

function isImageEndpoint(path: string): boolean {
  return isEdits(path) || isGenerations(path)
}

/** 只保留业务路由；模型列表 / 余额查询是管理面，不属于「入口 → 上游」的投影。 */
function businessRoutes(routes: readonly AdvancedCustomRoute[]): AdvancedCustomRoute[] {
  return routes.filter(
    (route) => !isAdvancedCustomManagementPath(incomingPathOf(route))
  )
}

/**
 * 主端点 = 出现次数最多的上游路径。
 *
 * 为什么不是「第一条」：预设与历史配置里，某条派生可能被单独改过，第一条恰好是例外。
 * 取众数更接近「这个渠道对上游到底讲什么」，同票时保留先出现的，避免顺序抖动。
 */
export function resolvePrimaryEndpoint(
  routes: readonly AdvancedCustomRoute[],
  declared?: string
): string {
  const explicit = (declared || '').trim()
  if (explicit) return explicit
  const counts = new Map<string, number>()
  for (const route of businessRoutes(routes)) {
    const upstream = upstreamPathOf(route)
    if (!upstream) continue
    counts.set(upstream, (counts.get(upstream) ?? 0) + 1)
  }
  let primary = ''
  let best = 0
  for (const [path, count] of counts) {
    if (count > best) {
      primary = path
      best = count
    }
  }
  return primary
}

/** 从渠道 settings JSON 里读主端点；坏 JSON 当没配。 */
export function readPrimaryEndpoint(settingsJson?: string): string {
  if (!settingsJson?.trim()) return ''
  try {
    const parsed: unknown = JSON.parse(settingsJson)
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return ''
    const value = (parsed as Record<string, unknown>).primary_endpoint
    return typeof value === 'string' ? value.trim() : ''
  } catch {
    return ''
  }
}

/** 把主端点写回 settings JSON；空串表示删除该字段（回到「从路由推导」）。 */
export function writePrimaryEndpoint(
  settingsJson: string | undefined,
  primary: string
): string {
  let settings: Record<string, unknown> = {}
  if (settingsJson?.trim()) {
    try {
      const parsed: unknown = JSON.parse(settingsJson)
      if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
        settings = parsed as Record<string, unknown>
      }
    } catch {
      settings = {}
    }
  }
  const value = primary.trim()
  if (value) {
    settings.primary_endpoint = value
  } else {
    delete settings.primary_endpoint
  }
  return JSON.stringify(settings)
}

/** 按入口归并成派生行；同一入口的多条路由（不同模型）算一行。 */
export function buildDerivations(
  routes: readonly AdvancedCustomRoute[],
  primary: string
): RouteDerivation[] {
  const rows = new Map<string, RouteDerivation>()
  routes.forEach((route, index) => {
    if (isAdvancedCustomManagementPath(incomingPathOf(route))) return
    const incomingPath = incomingPathOf(route)
    const upstreamPath = upstreamPathOf(route)
    const existing = rows.get(incomingPath)
    const routeModels = route.models?.filter(Boolean) ?? []
    if (!existing) {
      rows.set(incomingPath, {
        incomingPath,
        routeIndexes: [index],
        upstreamPath: upstreamPath || primary,
        followsPrimary: !upstreamPath || upstreamPath === primary,
        converter: normalizeConverter(route),
        passThrough: Boolean(route.pass_through_body_enabled),
        models: [...routeModels],
      })
      return
    }
    existing.routeIndexes.push(index)
    // 同一入口多行时，只要有一行跟随主端点就仍算「跟随」——它本来就是这个入口的默认行为。
    existing.followsPrimary =
      existing.followsPrimary || !upstreamPath || upstreamPath === primary
    if (!upstreamPath || upstreamPath === primary) existing.upstreamPath = primary
    existing.passThrough =
      existing.passThrough || Boolean(route.pass_through_body_enabled)
    existing.models = [...new Set([...existing.models, ...routeModels])]
  })
  return [...rows.values()]
}

/**
 * 换主端点：只改「跟随主端点」的派生，显式覆盖过的上游路径原样保留。
 *
 * 这是这套模型最重要的一条不变量——否则换个主端点就把用户手写的例外一起冲掉。
 */
export function applyPrimaryEndpoint(
  routes: readonly AdvancedCustomRoute[],
  nextPrimary: string,
  options?: { deriveToEmpty?: boolean }
): AdvancedCustomRoute[] {
  const primary = nextPrimary.trim()
  const previousPrimary = resolvePrimaryEndpoint(routes)
  return routes.map((route) => {
    if (isAdvancedCustomManagementPath(incomingPathOf(route))) return route
    const upstream = upstreamPathOf(route)
    if (upstream && upstream !== previousPrimary) return route
    // deriveToEmpty：存储形态只有「空 = 跟随主端点」一种，换主端点才真的只改一个字段。
    return { ...route, upstream_path: options?.deriveToEmpty ? '' : primary }
  })
}

/** 改一个派生行：入口是主键，组内所有路由一起改。 */
export function patchDerivation(
  routes: readonly AdvancedCustomRoute[],
  incomingPath: string,
  patch: {
    upstreamPath?: string
    converter?: string
    passThrough?: boolean
  }
): AdvancedCustomRoute[] {
  const target = incomingPath.trim()
  return routes.map((route) => {
    if (incomingPathOf(route) !== target) return route
    const next: AdvancedCustomRoute = { ...route }
    if (patch.upstreamPath !== undefined) {
      next.upstream_path = patch.upstreamPath.trim()
    }
    if (patch.converter !== undefined) {
      next.converter = patch.converter as AdvancedCustomRoute['converter']
    }
    if (patch.passThrough !== undefined) {
      next.pass_through_body_enabled = patch.passThrough
    }
    return next
  })
}

/** 新增派生：默认跟随主端点、原生转发，不预设透传。 */
export function addDerivation(
  routes: readonly AdvancedCustomRoute[],
  incomingPath: string,
  primary: string
): AdvancedCustomRoute[] {
  return [
    ...routes,
    {
      incoming_path: incomingPath,
      upstream_path: primary,
      converter: 'none',
    },
  ]
}

export function removeDerivation(
  routes: readonly AdvancedCustomRoute[],
  incomingPath: string
): AdvancedCustomRoute[] {
  const target = incomingPath.trim()
  return routes.filter((route) => incomingPathOf(route) !== target)
}

/**
 * 试算：这个入口 + 模型最终会发到哪、发什么形态。
 *
 * 判定顺序对齐后端 `MatchPathForModel`：先找同入口且模型命中的路由，再退回兜底路由。
 * 载体只看入口与透传：edits 透传才是 multipart，其余一律 JSON。
 */
export function previewRoutePlan(
  routes: readonly AdvancedCustomRoute[],
  options: { incomingPath: string; model?: string }
): RoutePlan {
  const incomingPath = options.incomingPath.trim()
  const model = (options.model || '').trim()
  const candidates = businessRoutes(routes)

  const matchesModel = (route: AdvancedCustomRoute) => {
    const models = route.models?.filter(Boolean) ?? []
    return models.length === 0 || (!!model && models.includes(model))
  }

  const exact = candidates.find(
    (route) => incomingPathOf(route) === incomingPath && matchesModel(route)
  )
  const catchAll = candidates.find((route) => incomingPathOf(route) === '')

  const route = exact ?? catchAll
  if (!route) {
    return {
      matched: false,
      incomingPath,
      upstreamPath: '',
      converter: 'none',
      passThrough: false,
      carrier: isEdits(incomingPath) ? 'multipart' : 'json',
      warnings: ['no-route'],
    }
  }

  const upstreamPath = upstreamPathOf(route) || resolvePrimaryEndpoint(routes)
  const converter = normalizeConverter(route)
  const passThrough = Boolean(route.pass_through_body_enabled)
  const carrier: RoutePlan['carrier'] =
    isEdits(incomingPath) && passThrough ? 'multipart' : 'json'
  const warnings: RoutePlanWarning[] = []
  if (exact === undefined && catchAll) warnings.push('catch-all')
  if (converter !== 'none') warnings.push('converter')
  if (isEdits(incomingPath)) {
    if (passThrough && isEdits(upstreamPath)) {
      warnings.push('edits-passthrough-native')
    } else if (passThrough && isGenerations(upstreamPath)) {
      // 最危险的一种：multipart 原样打给只收 JSON 的端点，上游回 4xx。
      warnings.push('edits-passthrough-to-json-upstream')
    } else if (!passThrough) {
      warnings.push('edits-rebuilt-to-json')
    }
  }
  if (isChat(incomingPath) && isImageEndpoint(upstreamPath)) {
    warnings.push('chat-to-image')
  }

  return {
    matched: true,
    incomingPath,
    upstreamPath,
    converter,
    passThrough,
    carrier,
    warnings,
  }
}

/**
 * 校验前补齐：把空 upstream_path 填成主端点，和后端 `WithPrimaryFallback` 一个语义。
 *
 * 校验器（`validateAdvancedCustomConfig`）只认「每条路由都有上游」。声明了主端点
 * 之后，空值不再是缺配置而是「跟随主端点」，所以校验必须看补齐后的副本，
 * 否则开着开关的合法配置会被判成 Incomplete。返回新数组，不改原对象。
 */
export function withPrimaryFallbackRoutes(
  routes: readonly AdvancedCustomRoute[],
  primary: string
): AdvancedCustomRoute[] {
  const center = primary.trim()
  if (!center) return [...routes]
  return routes.map((route) =>
    (route.upstream_path || '').trim() ? route : { ...route, upstream_path: center }
  )
}

/**
 * 统计口径的 JSON 版本：把 `advanced_custom` 字符串里空的 upstream_path 填成主端点。
 *
 * 抽屉上的路由徽标与「Incomplete」都用 `getAdvancedCustomStats`，它只看字符串。
 * 开关打开后空 upstream 是合法的，所以先把副本补齐再统计，否则页面一直显示不完整。
 */
export function fillPrimaryForStats(
  configJson: string | undefined,
  primary: string
): string | undefined {
  const center = primary.trim()
  const raw = (configJson || '').trim()
  if (!center || !raw) return configJson
  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
  } catch {
    return configJson
  }
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    return configJson
  }
  const config = parsed as { advanced_routes?: unknown }
  if (!Array.isArray(config.advanced_routes)) return configJson
  let changed = false
  const routes = config.advanced_routes.map((item) => {
    if (!item || typeof item !== 'object' || Array.isArray(item)) return item
    const route = item as Record<string, unknown>
    const upstream = typeof route.upstream_path === 'string' ? route.upstream_path.trim() : ''
    if (upstream) return item
    changed = true
    return { ...route, upstream_path: center }
  })
  if (!changed) return configJson
  return JSON.stringify({ ...config, advanced_routes: routes })
}

/** 主端点看着像图片端点（路径里含 /images/）。协议回退、页面文案都用这一条判据。 */
export function looksLikeImagePath(path: string): boolean {
  return /\/images(\/|$)/.test(path.trim())
}

/**
 * 打开主端点开关时的路由表兜底：一张业务路由都没有时，先种一条跟随主端点的派生。
 *
 * 高级自定义渠道至少需要一条路由才合法，否则打开开关后保存会被
 * 「requires at least one route」拦住，而错误指向的路由列表在页面上是空的。
 * 入站入口按主端点猜：看着像图片端点就种 generations，否则种 chat。
 */
export function seedPrimaryRoute(
  routes: readonly AdvancedCustomRoute[],
  primary: string
): AdvancedCustomRoute[] {
  const hasBusinessRoute = routes.some(
    (route) => !isAdvancedCustomManagementPath(incomingPathOf(route))
  )
  if (hasBusinessRoute) return [...routes]
  // 保留已有的管理面路由（模型列表 / 余额查询），只补业务入口：
  // 换主端点不该顺手把运维配好的管理面删掉。
  return [
    ...routes,
    {
      incoming_path: looksLikeImagePath(primary)
        ? '/v1/images/generations'
        : '/v1/chat/completions',
      upstream_path: '',
      converter: 'none',
    },
  ]
}
