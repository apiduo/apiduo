import {
  ADVANCED_CUSTOM_MODEL_REGEX_PREFIX,
  isAdvancedCustomManagementPath,
} from '@/features/channels/lib/advanced-custom'
import type { AdvancedCustomRoute } from '@/features/channels/types'

import type { Coverage, CoverageCell, MatrixOutcome } from '../types'
import { MATRIX_DEFAULT_PATHS } from '../types'

function cleanList(values: readonly string[]): string[] {
  return [...new Set(values.map((value) => value.trim()).filter(Boolean))]
}

function modelMatchesRule(rule: string, model: string): boolean {
  if (!rule.startsWith(ADVANCED_CUSTOM_MODEL_REGEX_PREFIX)) return rule === model
  try {
    return new RegExp(
      rule.slice(ADVANCED_CUSTOM_MODEL_REGEX_PREFIX.length)
    ).test(model)
  } catch {
    // 坏正则不该让整张矩阵崩掉，后端 Validate 保存时就会拒。
    return false
  }
}

/**
 * 复刻后端 `AdvancedCustomConfig.MatchPathForModel`：
 * 按数组顺序取第一条「路径相同 且 模型规则命中」的路由，`models` 为空即兜底。
 *
 * 矩阵只处理精确路径；带 `{model}` 模板的路由在 `decodeMatrix` 阶段就被挡掉了，
 * 所以这里不需要实现模板匹配。
 */
export function findRouteForModel(
  routes: AdvancedCustomRoute[] | undefined,
  endpoint: string,
  model: string
): { route: AdvancedCustomRoute; index: number } | null {
  const target = endpoint.trim()

  for (const [index, route] of (routes ?? []).entries()) {
    if ((route.incoming_path ?? '').trim() !== target) continue

    const rules = (route.models ?? [])
      .map((rule) => rule.trim())
      .filter(Boolean)
    if (rules.length === 0) return { route, index }
    if (rules.some((rule) => modelMatchesRule(rule, model))) {
      return { route, index }
    }
  }

  return null
}

/**
 * 端点清单 = 默认展示的 5 个 + `advanced_routes` 里实际出现的路径。
 * 默认清单只是兜底，渠道真配了别的端点必须能看到。
 */
export function collectEndpoints(
  routes: AdvancedCustomRoute[] | undefined,
  defaults: readonly string[] = MATRIX_DEFAULT_PATHS
): string[] {
  const endpoints = cleanList(defaults)
  const seen = new Set(endpoints)

  for (const route of routes ?? []) {
    const path = (route.incoming_path ?? '').trim()
    // 管理路由（模型列表、余额查询）是控制面接口，不是客户端生成入口，
    // 放进覆盖矩阵只会让每一行都多出一个永远「已配置」的噪音行。
    if (!path || isAdvancedCustomManagementPath(path) || seen.has(path)) continue
    seen.add(path)
    endpoints.push(path)
  }

  return endpoints
}

/**
 * 端点 × 模型 的覆盖表。每一格回答「这个模型打这个端点会发生什么」。
 *
 * 这一层不读也不写配置，只做推导——所以它可以在编辑过程中随时重算，
 * 不参与保存，也不会和 `advanced_routes` 产生第二份真相。
 */
export function buildCoverage(
  routes: AdvancedCustomRoute[] | undefined,
  models: readonly string[],
  endpoints: readonly string[]
): Coverage {
  const cleanEndpoints = cleanList(endpoints)
  const cleanModels = cleanList(models)
  const cells: CoverageCell[] = []
  let forwarded = 0

  for (const endpoint of cleanEndpoints) {
    for (const model of cleanModels) {
      const hit = findRouteForModel(routes, endpoint, model)
      const outcome: MatrixOutcome = hit
        ? {
            kind: 'forwarded',
            routeIndex: hit.index,
            ...(hit.route.converter && hit.route.converter !== 'none'
              ? { converter: hit.route.converter }
              : {}),
          }
        : { kind: 'rejected' }

      if (outcome.kind === 'forwarded') forwarded += 1
      cells.push({ endpoint, model, outcome })
    }
  }

  return {
    endpoints: cleanEndpoints,
    models: cleanModels,
    cells,
    forwarded,
    rejected: cells.length - forwarded,
  }
}

/**
 * routes 里声明过的模型规则，含 `re:` 原文。
 * 兜底路由不产生模型——所以全兜底配置下这里返回空，覆盖视图退化成端点级。
 */
export function collectModels(
  routes: AdvancedCustomRoute[] | undefined
): string[] {
  const models: string[] = []
  const seen = new Set<string>()

  for (const route of routes ?? []) {
    for (const rule of route.models ?? []) {
      const trimmed = rule.trim()
      if (!trimmed || seen.has(trimmed)) continue
      // 正则规则没法展开成具体模型名。放进列头只会误导——拿它去匹配它自己
      // 永远为 false，整列会假报 404。
      if (trimmed.startsWith(ADVANCED_CUSTOM_MODEL_REGEX_PREFIX)) continue
      seen.add(trimmed)
      models.push(trimmed)
    }
  }

  return models
}

/** 已经被任何一条路由覆盖的端点。 */
export function coveredEndpoints(
  routes: AdvancedCustomRoute[] | undefined
): Set<string> {
  const paths = new Set<string>()
  for (const route of routes ?? []) {
    const path = (route.incoming_path ?? '').trim()
    if (path && !isAdvancedCustomManagementPath(path)) paths.add(path)
  }
  return paths
}

/** 找出会被拒绝的组合——UI 用它高亮「这一格会 404」。 */
export function rejectedCells(coverage: Coverage): CoverageCell[] {
  return coverage.cells.filter((cell) => cell.outcome.kind === 'rejected')
}
