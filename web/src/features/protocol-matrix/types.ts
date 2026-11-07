import type {
  AdvancedCustomConverter,
  AdvancedCustomRoute,
  AdvancedCustomRouteAuth,
} from '@/features/channels/types'

/**
 * 「协议矩阵」是 `advanced_custom.advanced_routes` 的另一种视图，不是新数据源。
 *
 * 保存时仍然只写 `advanced_custom`（见 lib/matrix.ts 的 encodeMatrix），
 * 所以数据库、后端校验、运行时路由匹配都不需要知道矩阵的存在。
 */
export type MatrixMode = 'passthrough' | 'transform' | 'reject'

/**
 * 一行 = 一个端点 + 一组模型。
 *
 * `models` 为空表示该端点的兜底行：任何模型都落在这一行。上游要求同一端点下
 * 兜底行必须排在最后，`encodeMatrix` 负责排序，UI 不需要关心。
 *
 * 同一个端点可以有多行，用模型区分：seedream-4 走转换、seedream-3 走透传，
 * 是两行。同一个端点下模型不允许重叠（上游 `validateAdvancedCustomRouteModels` 会拒）。
 *
 * `upstreamPath` / `auth` / `passThroughBody` 是「矩阵不解释、但要原样带回」的字段——
 * 用户可能手改过它们，丢一次就是一次静默改写。
 */
export type MatrixItem =
  | {
      path: string
      models: string[]
      mode: 'passthrough'
      /** 缺省时 `encodeMatrix` 补成 `path`。 */
      upstreamPath?: string
      /** 上游的 per-route 透传开关；缺省不写。 */
      passThroughBody?: boolean
      /** 显式 `{type:'none'}` 要原样带回，它和「没有 auth」在界面上是两个选项。 */
      auth?: AdvancedCustomRouteAuth
    }
  | {
      path: string
      models: string[]
      mode: 'transform'
      converter: AdvancedCustomConverter
      /** 缺省时 `encodeMatrix` 用该转换器的默认上游路径。 */
      upstreamPath?: string
      /** 缺省 auth 由转换器决定；用户删掉 auth 时要能带回「没有」。 */
      auth?: AdvancedCustomRouteAuth
    }
  | { path: string; models: string[]; mode: 'reject' }

export type Matrix = {
  items: MatrixItem[]
  /**
   * 管理路由（模型列表、余额查询）不参与矩阵编辑，但必须原样带回。
   * 放在 Matrix 里而不是单独返回，是为了让 `encodeMatrix` 不可能忘记它们——
   * 漏一次就是一次静默删配置。
   */
  preserved?: AdvancedCustomRoute[]
}

/**
 * 一个 (端点, 模型) 组合在**运行时**会发生什么。
 *
 * 由 `buildCoverage` 从 routes 推导，不落库、不新增字段。
 * 它回答的是现有编辑器回答不了的那句话：
 *
 *     seedream-3 调 /v1/images/edits → 没有路由 → 404
 *
 * 判据照抄后端 `AdvancedCustomConfig.MatchPathForModel`：顺序匹配、先命中先赢，
 * `models` 为空的兜底行接住剩下的所有模型。
 */
export type MatrixOutcome =
  | {
      kind: 'forwarded'
      /** 命中的那条 route 在 `advanced_routes` 里的下标。 */
      routeIndex: number
      /** 非原生转发时，实际走的转换器。 */
      converter?: AdvancedCustomConverter
    }
  | { kind: 'rejected' }

export type CoverageCell = {
  endpoint: string
  model: string
  outcome: MatrixOutcome
}

/** 端点 × 模型 的完整覆盖情况；行优先，`cells[i * models.length + j]`。 */
export type Coverage = {
  endpoints: string[]
  models: string[]
  cells: CoverageCell[]
  forwarded: number
  rejected: number
}

/** 反解失败的原因。矩阵只接受「能被无损往返」的路由，
 * 遇到路径模板、自定义鉴权这类存量配置时拒绝进入矩阵模式，
 * 由调用方退回传统列表编辑器，保证存量配置不会被矩阵吃掉。
 */
export type MatrixDecodeFailure = {
  ok: false
  reason: string
  routeIndex?: number
}

export type MatrixDecodeResult =
  | { ok: true; matrix: Matrix }
  | MatrixDecodeFailure

/**
 * 矩阵默认展示的端点。
 *
 * 实测下来的渠道大多只配 3-5 个端点，所以这里给 5 个覆盖文本 + 图像的常见入口，
 * 不是全集。渠道真要用别的端点，按 `advanced_routes` 里实际出现的路径自动补进来。
 */
export const MATRIX_DEFAULT_PATHS = [
  '/v1/chat/completions',
  '/v1/responses',
  '/v1/embeddings',
  '/v1/images/generations',
  '/v1/images/edits',
] as const
