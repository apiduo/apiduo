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
/** 与后端 pkg/apiduo/discount.Rule 一一对应 */
export type DiscountRule = {
  /** 逗号分隔的组名，或 "*" 表示全部 */
  groups: string
  /** 逗号分隔的模型名，或 "*" 表示全部 */
  models: string
  /** 最终组倍率，严格 0 < rate < 1；0.8 即八折 */
  rate: number
  /** 失效时刻（unix 秒），0 表示永久 */
  until: number
}

export type UserDiscountsPayload = {
  rules: DiscountRule[]
  /** 按被编辑的用户算出的可用分组，不是当前登录管理员的 */
  usable_groups: string[]
}

/** 单用户规则上限，与后端 discount.MaxRules 保持一致 */
export const MAX_DISCOUNT_RULES = 20
