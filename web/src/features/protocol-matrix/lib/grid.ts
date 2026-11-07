import {
  ADVANCED_CUSTOM_MODEL_REGEX_PREFIX,
  getAdvancedCustomConverterDefaults,
  getAdvancedCustomConverterOptions,
  getAdvancedCustomIncomingPathOptions,
  isAdvancedCustomManagementPath,
} from '@/features/channels/lib/advanced-custom'
import type {
  AdvancedCustomConverter,
  AdvancedCustomRoute,
} from '@/features/channels/types'

/**
 * 「哪些模型支持哪些端点」的二维表，每格三态。
 *
 * 和 `coverage.ts` 的区别：覆盖表是**读**（路由推导出后果），这里是**写**
 * （格子的状态反推路由）。两者共用同一组端点与模型，但方向相反。
 */
export type GridCellMode = 'off' | AdvancedCustomConverter

/**
 * `'off'` 和 `'none'` 是两个不同的东西，别混：
 *  - `'off'`：不生成路由，后端 `MatchPathForModel` 匹配不到，请求被拒
 *  - `'none'`：原生透传，写成 `converter: 'none'`
 *
 * 之所以不给 `'off'` 单独造一个枚举，是因为路由的 converter 恰好是一个
 * 字符串，分组时把 mode 直接当 key 用，不用再折一次。
 */
export type GridCell = {
  endpoint: string
  model: string
  mode: GridCellMode
}

/** 端点为什么不能用矩阵编辑。UI 把它翻成人话。 */
export type GridConflictCode =
  | 'regex-models'
  | 'unsupported-converter'
  | 'overlapping-routes'

export type GridOrphan = {
  endpoint: string
  model: string
  mode: GridCellMode
}

export type Grid = {
  endpoints: string[]
  models: string[]
  /** 行优先：`cells[row * models.length + column]`。 */
  cells: GridCell[]
  /** 端点 → 无法用矩阵表达的原因；可编辑的端点不出现在这里。 */
  conflicts: Record<string, GridConflictCode>
  /**
   * routes 里点名了、但渠道模型清单里没有的模型。
   * 表格不显示它们（表格只按渠道清单展开），但生成时必须原样带回，
   * 否则改一次矩阵就静默删掉一条既有路由。
   */
  orphans: GridOrphan[]
}

function cleanList(values: readonly string[]): string[] {
  return [...new Set(values.map((value) => value.trim()).filter(Boolean))]
}

/** 空串和缺省都归一成 `none`——上游 `Validate` 也是这么做的。 */
export function normalizeConverter(
  route: AdvancedCustomRoute
): AdvancedCustomConverter {
  return ((route.converter ?? '').trim() ||
    'none') as AdvancedCustomConverter
}

function converterServesPath(
  converter: AdvancedCustomConverter,
  path: string
): boolean {
  if (converter === 'none') return true
  return getAdvancedCustomIncomingPathOptions(converter).some(
    (option) => option.value === path
  )
}

/** 把一个端点下的所有路由收成一组。 */
function routesByEndpoint(
  routes: AdvancedCustomRoute[] | undefined
): Map<string, AdvancedCustomRoute[]> {
  const grouped = new Map<string, AdvancedCustomRoute[]>()
  for (const route of routes ?? []) {
    const path = (route.incoming_path ?? '').trim()
    if (!path || isAdvancedCustomManagementPath(path)) continue
    const list = grouped.get(path) ?? []
    list.push(route)
    grouped.set(path, list)
  }
  return grouped
}

/**
 * 从 routes 反解出每个格子的状态。
 *
 * 多条路由现在也能反解了：一格一个 converter，分组本来就是按 converter 做的。
 * 只有三种情况仍然判成冲突：
 *  1. 路由用 `re:` 正则圈模型——展开不成具体的格子
 *  2. 路由的 converter 服务不了这个端点——写回去上游会拒
 *  3. 同一个模型被两条路由同时命中——格子的三态表达不了二选一
 */
export function buildGrid(
  routes: AdvancedCustomRoute[] | undefined,
  models: readonly string[],
  endpoints: readonly string[]
): Grid {
  const cleanEndpoints = cleanList(endpoints)
  const cleanModels = cleanList(models)
  const grouped = routesByEndpoint(routes)
  const modelSet = new Set(cleanModels)
  const cells: GridCell[] = []
  const conflicts: Record<string, GridConflictCode> = {}
  const orphans: GridOrphan[] = []

  for (const endpoint of cleanEndpoints) {
    const list = grouped.get(endpoint) ?? []
    const modes = new Map<string, GridCellMode>()
    let conflict: GridConflictCode | undefined

    for (const route of list) {
      const converter = normalizeConverter(route)
      if (!converterServesPath(converter, endpoint)) {
        conflict = conflict ?? 'unsupported-converter'
        continue
      }

      const rules = (route.models ?? [])
        .map((rule) => rule.trim())
        .filter(Boolean)
      if (
        rules.some((rule) => rule.startsWith(ADVANCED_CUSTOM_MODEL_REGEX_PREFIX))
      ) {
        conflict = conflict ?? 'regex-models'
        continue
      }

      // 兜底路由（models 为空）接住全部模型，不会产生孤儿。
      const targets = rules.length === 0 ? cleanModels : rules
      for (const model of targets) {
        if (!modelSet.has(model)) {
          if (rules.length > 0) {
            orphans.push({ endpoint, model, mode: converter })
          }
          continue
        }
        const previous = modes.get(model)
        if (previous !== undefined && previous !== 'off') {
          conflict = conflict ?? 'overlapping-routes'
          continue
        }
        modes.set(model, converter)
      }
    }

    for (const model of cleanModels) {
      cells.push({ endpoint, model, mode: modes.get(model) ?? 'off' })
    }
    if (conflict) conflicts[endpoint] = conflict
  }

  return { endpoints: cleanEndpoints, models: cleanModels, cells, conflicts, orphans }
}

export function isCellEditable(grid: Grid, endpoint: string): boolean {
  return !grid.conflicts[endpoint]
}

export function cellAt(
  grid: Grid,
  endpoint: string,
  model: string
): GridCell | undefined {
  return grid.cells.find(
    (cell) => cell.endpoint === endpoint && cell.model === model
  )
}

export function setCellMode(
  grid: Grid,
  endpoint: string,
  model: string,
  mode: GridCellMode
): Grid {
  return {
    ...grid,
    cells: grid.cells.map((cell) =>
      cell.endpoint === endpoint && cell.model === model
        ? { ...cell, mode }
        : cell
    ),
  }
}

/** 点格子：`off` ↔ `none`。选转换器走 `setCellMode`，不在这里循环。 */
export function toggleCell(
  grid: Grid,
  endpoint: string,
  model: string
): Grid {
  const cell = cellAt(grid, endpoint, model)
  return setCellMode(grid, endpoint, model, cell?.mode === 'off' ? 'none' : 'off')
}

/**
 * 整行填充。
 *
 * `'none'` 只把 `off` 提升成透传，**不动**已经是转换器的格子——「全选」不该
 * 顺手把用户挑好的转换器降级。`'off'` 则清空整行，语义就是清空。
 */
export function fillEndpoint(
  grid: Grid,
  endpoint: string,
  mode: 'none' | 'off'
): Grid {
  return {
    ...grid,
    cells: grid.cells.map((cell) => {
      if (cell.endpoint !== endpoint) return cell
      if (mode === 'off') return { ...cell, mode }
      return cell.mode === 'off' ? { ...cell, mode } : cell
    }),
  }
}

export function fillAll(grid: Grid, mode: 'none' | 'off'): Grid {
  return {
    ...grid,
    cells: grid.cells.map((cell) => {
      if (!isCellEditable(grid, cell.endpoint)) return cell
      if (mode === 'off') return { ...cell, mode }
      return cell.mode === 'off' ? { ...cell, mode } : cell
    }),
  }
}

/** 某个端点被点亮的模型，按 `grid.models` 的顺序。 */
export function supportedModels(grid: Grid, endpoint: string): string[] {
  return grid.cells
    .filter((cell) => cell.endpoint === endpoint && cell.mode !== 'off')
    .map((cell) => cell.model)
}

/** 某个端点按 converter 分组后的模型清单，顺序沿用 `grid.models`。 */
export function modelsByMode(
  grid: Grid,
  endpoint: string
): Map<GridCellMode, string[]> {
  const groups = new Map<GridCellMode, string[]>()
  for (const cell of grid.cells) {
    if (cell.endpoint !== endpoint || cell.mode === 'off') continue
    const list = groups.get(cell.mode) ?? []
    list.push(cell.model)
    groups.set(cell.mode, list)
  }
  return groups
}

/**
 * 某个端点能选哪些转换器——直接问上游，不另建表。
 * 上游加一个转换器，矩阵自动多一个选项。
 */
export function converterOptionsForEndpoint(
  endpoint: string
): AdvancedCustomConverter[] {
  return getAdvancedCustomConverterOptions(endpoint)
    .map((option) => option.value)
    .filter((converter) => converter !== 'none')
}

function buildRoute(
  endpoint: string,
  mode: AdvancedCustomConverter,
  models: string[],
  template: AdvancedCustomRoute | undefined,
  catchAll: boolean
): AdvancedCustomRoute {
  const defaults = getAdvancedCustomConverterDefaults(mode, endpoint)
  // 模板按 converter 取——同一端点下不同转换器各有各的上游路径和鉴权，
  // 交叉沿用会把 A 的上游路径写到 B 上。
  const auth = template ? template.auth : mode === 'none' ? undefined : defaults.auth

  return {
    incoming_path: endpoint,
    upstream_path:
      (template?.upstream_path ?? '').trim() || defaults.upstream_path,
    converter: mode,
    ...(auth ? { auth } : {}),
    ...(template?.pass_through_body_enabled
      ? { pass_through_body_enabled: true }
      : {}),
    ...(catchAll ? {} : { models }),
  }
}

/**
 * 把格子写回 `advanced_routes`。
 *
 * 同一端点可以产出多条路由（不同 converter 各一条），每条带自己的模型清单。
 * 只有「该端点只有一个 converter、且它覆盖了全部模型」时才收敛成兜底路由
 * （`models` 缺省）——兜底比把模型名抄一遍更稳，渠道以后加模型不用回来改。
 *
 * 顺序：带 `models` 的在前、兜底在后；上游要求同一端点的兜底排在最后。
 * 其它端点保持原有相对顺序，避免每次改一个格子都把整个数组重排。
 */
export function gridToRoutes(
  grid: Grid,
  previous: AdvancedCustomRoute[] | undefined
): AdvancedCustomRoute[] {
  const grouped = routesByEndpoint(previous)
  const generated = new Map<string, AdvancedCustomRoute[]>()

  for (const endpoint of grid.endpoints) {
    if (!isCellEditable(grid, endpoint)) continue
    // 管理路由只参与展示，永远不重写。
    if (isAdvancedCustomManagementPath(endpoint)) continue

    const template = grouped.get(endpoint) ?? []
    const groups = modelsByMode(grid, endpoint)
    const orphans = grid.orphans.filter((item) => item.endpoint === endpoint)
    for (const orphan of orphans) {
      const list = groups.get(orphan.mode) ?? []
      if (!list.includes(orphan.model)) list.push(orphan.model)
      groups.set(orphan.mode, list)
    }

    if (groups.size === 0) {
      generated.set(endpoint, [])
      continue
    }

    const entries = [...groups.entries()]
    // 收敛成兜底的条件：只有一个 converter，且它确实覆盖了表格里的每一个模型。
    // 不能用「分组里模型数 ≥ 表格模型数」判断——孤儿模型会把这个数量凑够，
    // 于是一个原本只勾了 flash 的端点会被写成兜底，pro 也跟着放行。
    const coversAll =
      entries.length === 1 &&
      grid.models.every((model) => entries[0][1].includes(model))

    const routes = entries
      .filter(([, models]) => models.length > 0)
      .map(([mode, models]) =>
        buildRoute(
          endpoint,
          mode as AdvancedCustomConverter,
          models,
          template.find((route) => normalizeConverter(route) === mode),
          coversAll
        )
      )

    generated.set(endpoint, routes)
  }

  // 只有「表格里有这一行、且这一行可编辑」的端点才允许被重写。
  // 其余一律原样保留——包括管理路由，以及调用方没放进表格的路径。
  const rewritten = new Set(
    grid.endpoints.filter(
      (endpoint) =>
        !isAdvancedCustomManagementPath(endpoint) &&
        isCellEditable(grid, endpoint)
    )
  )
  const kept: AdvancedCustomRoute[] = []
  const emitted = new Set<string>()

  for (const route of previous ?? []) {
    const path = (route.incoming_path ?? '').trim()
    if (!path) continue
    if (!rewritten.has(path)) {
      kept.push(route)
      continue
    }
    // 同一端点的多条旧路由只触发一次重生成，位置取第一条。
    if (emitted.has(path)) continue
    emitted.add(path)
    kept.push(...(generated.get(path) ?? []))
  }

  // 新端点（之前一条路由都没有）按表格顺序追加。
  for (const endpoint of grid.endpoints) {
    if (emitted.has(endpoint)) continue
    const routes = generated.get(endpoint)
    if (routes && routes.length > 0) kept.push(...routes)
  }

  return kept
}

/** 表格里没显示、但生成时保留了的模型数量。UI 用它提示「别以为删掉了」。 */
export function orphanCount(grid: Grid): number {
  return grid.orphans.length
}
