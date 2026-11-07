import {
  ADVANCED_CUSTOM_CONVERTER_OPTIONS,
  getAdvancedCustomConverterDefaults,
  getAdvancedCustomIncomingPathOptions,
  isAdvancedCustomManagementPath,
} from '@/features/channels/lib/advanced-custom'
import type {
  AdvancedCustomConverter,
  AdvancedCustomRoute,
  AdvancedCustomRouteAuth,
} from '@/features/channels/types'

import type { Matrix, MatrixDecodeResult, MatrixItem } from '../types'

const CONVERTER_NONE: AdvancedCustomConverter = 'none'

function normalizeModels(models: string[] | undefined): string[] {
  if (!models?.length) return []
  return [...new Set(models.map((model) => model.trim()).filter(Boolean))]
}

function authEquals(
  actual: AdvancedCustomRouteAuth | undefined,
  expected: AdvancedCustomRouteAuth | undefined
): boolean {
  return (
    (actual?.type ?? '') === (expected?.type ?? '') &&
    (actual?.name ?? '') === (expected?.name ?? '') &&
    (actual?.value ?? '') === (expected?.value ?? '')
  )
}

type EmittableItem = Exclude<MatrixItem, { mode: 'reject' }>

function toRoute(item: EmittableItem): AdvancedCustomRoute {
  const models = item.models.length > 0 ? [...item.models] : undefined
  const modelField = models ? { models } : {}

  if (item.mode === 'passthrough') {
    const defaults = getAdvancedCustomConverterDefaults(
      CONVERTER_NONE,
      item.path
    )
    return {
      incoming_path: item.path.trim(),
      upstream_path: (item.upstreamPath ?? '').trim() || defaults.upstream_path,
      converter: CONVERTER_NONE,
      ...(item.auth ? { auth: item.auth } : {}),
      ...(item.passThroughBody ? { pass_through_body_enabled: true } : {}),
      ...modelField,
    }
  }

  const defaults = getAdvancedCustomConverterDefaults(item.converter, item.path)
  return {
    incoming_path: item.path.trim(),
    upstream_path: (item.upstreamPath ?? '').trim() || defaults.upstream_path,
    converter: item.converter,
    ...(item.auth ? { auth: item.auth } : {}),
    ...modelField,
  }
}

/**
 * 把 `advanced_custom.advanced_routes` 反解成矩阵视图。
 *
 * 只接受能被 `encodeMatrix` 无损写回的路由：路径模板、自定义鉴权这类存量配置
 * 一律返回失败，由调用方退回传统列表编辑器。宁可不让用，也不能让用户点一下
 * 「协议矩阵」就把已有的高级配置改坏。
 */
export function decodeMatrix(
  routes: AdvancedCustomRoute[] | undefined
): MatrixDecodeResult {
  const items: MatrixItem[] = []
  const preserved: AdvancedCustomRoute[] = []
  const modelsByPath = new Map<string, Set<string>>()
  const catchAllPaths = new Set<string>()

  for (const [index, route] of (routes ?? []).entries()) {
    const path = (route.incoming_path ?? '').trim()
    const upstreamPath = (route.upstream_path ?? '').trim()
    // 手工编辑过的配置可能留下空串，上游 `Validate` 会把它归一成 none，
    // 矩阵必须提前做同样的事，否则一条空 converter 会被当成未知转换器拒掉。
    const converter = ((route.converter ?? '').trim() ||
      CONVERTER_NONE) as AdvancedCustomConverter
    const models = normalizeModels(route.models)

    if (!path) {
      return { ok: false, reason: 'incoming path is empty', routeIndex: index }
    }
    if (isAdvancedCustomManagementPath(path)) {
      preserved.push(route)
      continue
    }
    if (models.length === 0) {
      if (catchAllPaths.has(path)) {
        return {
          ok: false,
          reason: `${path} declares more than one fallback route`,
          routeIndex: index,
        }
      }
      catchAllPaths.add(path)
    } else {
      const seenModels = modelsByPath.get(path) ?? new Set<string>()
      for (const model of models) {
        if (seenModels.has(model)) {
          return {
            ok: false,
            reason: `${model} is claimed twice for ${path}`,
            routeIndex: index,
          }
        }
        seenModels.add(model)
      }
      modelsByPath.set(path, seenModels)
    }

    // 没有 auth、或者显式 `{type:'none'}` 都表示「不额外加鉴权」，前者是原生透传
    // 最常见的形态，后者是用户在界面上主动选的「无」。两者都合法。
    // 只有写了非 none 的 auth 且和该转换器默认值不同，才是手工定制过的，矩阵表达不了。
    const defaults = getAdvancedCustomConverterDefaults(converter, path)
    const customAuth =
      route.auth !== undefined && (route.auth.type ?? 'none') !== 'none'
    if (customAuth && !authEquals(route.auth, defaults.auth)) {
      return {
        ok: false,
        reason: `${path} overrides the default auth`,
        routeIndex: index,
      }
    }

    if (converter === CONVERTER_NONE) {
      items.push({
        path,
        models,
        mode: 'passthrough',
        upstreamPath: upstreamPath || path,
        ...(route.auth ? { auth: route.auth } : {}),
        ...(route.pass_through_body_enabled ? { passThroughBody: true } : {}),
      })
      continue
    }

    const allowedPaths = getAdvancedCustomIncomingPathOptions(converter).map(
      (option) => option.value
    )
    if (!allowedPaths.includes(path)) {
      return {
        ok: false,
        reason: `converter ${converter} cannot serve ${path}`,
        routeIndex: index,
      }
    }

    items.push({
      path,
      models,
      mode: 'transform',
      converter,
      upstreamPath: upstreamPath || defaults.upstream_path,
      ...(route.auth ? { auth: route.auth } : {}),
    })
  }

  return { ok: true, matrix: { items, preserved } }
}

/**
 * 把矩阵写回 `advanced_routes`。
 *
 * reject 行不生成路由——后端 `resolve` 对未声明的路径本来就报错，
 * 所以「不写」就是「拒绝」。
 *
 * 兜底行（models 为空）统一排在具体模型行之后：上游要求同一端点下
 * catch-all 必须是最后一条，而 `MatchPathForModel` 顺序匹配、先命中先赢。
 */
export function encodeMatrix(matrix: Matrix): AdvancedCustomRoute[] {
  const routes: AdvancedCustomRoute[] = [...(matrix.preserved ?? [])]

  for (const item of matrix.items) {
    if (item.mode === 'reject' || item.models.length === 0) continue
    routes.push(toRoute(item))
  }
  for (const item of matrix.items) {
    if (item.mode === 'reject' || item.models.length > 0) continue
    routes.push(toRoute(item))
  }

  return routes
}

/** 矩阵自身的约束；转换器与端点的匹配由 `validateAdvancedCustomConfig` 兜底。 */
export function validateMatrix(matrix: Matrix): string[] {
  const issues: string[] = []
  const modelsByPath = new Map<string, Set<string>>()
  const catchAllPaths = new Set<string>()

  for (const item of matrix.items) {
    const path = item.path.trim()
    if (!path) {
      issues.push('every row needs an endpoint')
      continue
    }

    const models = normalizeModels(item.models)
    if (models.length === 0) {
      if (catchAllPaths.has(path)) {
        issues.push(`${path} allows only one fallback row`)
      }
      catchAllPaths.add(path)
    } else {
      const seenModels = modelsByPath.get(path) ?? new Set<string>()
      for (const model of models) {
        if (seenModels.has(model)) {
          issues.push(`${model} appears twice for ${path}`)
        }
        seenModels.add(model)
      }
      modelsByPath.set(path, seenModels)
    }

    if (item.mode === 'passthrough' && !(item.upstreamPath ?? '').trim()) {
      issues.push(`${path} needs an upstream path`)
    }
    if (item.mode === 'transform') {
      const allowedPaths = getAdvancedCustomIncomingPathOptions(
        item.converter
      ).map((option) => option.value)
      if (!allowedPaths.includes(path)) {
        issues.push(`${path} cannot be produced by ${item.converter}`)
      }
    }
  }

  return issues
}

/** 某个端点当前可用的转换器——直接复用上游的端点↔转换器映射，不另建表。 */
export function converterOptionsForPath(
  path: string
): AdvancedCustomConverter[] {
  return ADVANCED_CUSTOM_CONVERTER_OPTIONS.map((option) => option.value).filter(
    (converter) =>
      converter !== CONVERTER_NONE &&
      getAdvancedCustomIncomingPathOptions(converter).some(
        (option) => option.value === path
      )
  )
}
