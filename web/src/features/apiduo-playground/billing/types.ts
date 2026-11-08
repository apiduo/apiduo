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

/**
 * 计费口径的唯一事实来源。
 *
 * 这里的字段名与 `/api/pricing`、日志 `other` 保持一一对应，目的是让用户拿
 * 对账单上的数字去复算日志时，四项倍率能对得上、公式能复原。
 */
export interface PricingModel {
  modelName: string
  /** 0 = 按量（model_ratio），1 = 按次（model_price） */
  quotaType: number
  modelRatio: number
  modelPrice: number
  completionRatio: number
  cacheRatio: number
  enableGroups: string[]
  /** 该模型支持的端点类型，用来把生图/生视频模型筛到对应工作区 */
  endpointTypes: string[]
}

export interface PricingSnapshot {
  models: Record<string, PricingModel>
  groupRatio: Record<string, number>
  /** group → model → 折后倍率；命中即覆盖组倍率，不与组倍率相乘 */
  userDiscount: Record<string, Record<string, number>>
  fetchedAt: number
}

/** 与 `service/text_quota.go` 的 OpenAI 语义分支对齐 */
export interface ChatUsageTokens {
  promptTokens: number
  completionTokens: number
  /** prompt_tokens_details.cached_tokens：已含在 promptTokens 内 */
  cacheTokens: number
}

export interface QuotaBreakdown {
  quota: number
  usd: number
  /** 实际参与计算的组倍率：命中折扣时是折后值 */
  effectiveGroupRatio: number
  /** 命中个人折扣时的折扣值，未命中为 null */
  discountRate: number | null
  /** 未打折的原始组倍率，用于展示「被覆盖」 */
  baseGroupRatio: number
  modelRatio: number
  completionRatio: number
  cacheRatio: number
  /** 未命中缓存、按原价计的输入 token */
  baseTokens: number
  cacheTokens: number
  /** 计算式文本，供对账单直接展示 */
  formula: string
}

export interface RequestLogRecord {
  quota: number
  promptTokens: number
  completionTokens: number
  useTime: number
  channelId: number
  channelName: string
  requestId: string
  /** 日志 other 字段里的倍率快照，可能为空（日志未落库或旧记录） */
  other: Record<string, unknown> | null
}
