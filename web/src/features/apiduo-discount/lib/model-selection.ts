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
import { splitList } from './match'

/**
 * 把多选控件的选择结果写回逗号串。
 *
 * "*" 和具体模型互斥：后端只看列表里有没有 "*"，混着写等于把具体名字全部作废，
 * 而界面上还留着几个 chip，运营会以为自己配了例外。所以按「这次新增了哪一侧」
 * 决定保留谁——新加 "*" 就清空名字，新加名字就丢掉 "*"。
 */
export function mergeModelSelection(previous: string, next: string[]): string {
  const before = splitList(previous)
  const added = next.filter((value) => !before.includes(value))

  if (added.includes('*')) return '*'
  if (before.includes('*') && added.length > 0) return added.join(',')
  return next.join(',')
}

/**
 * 单元格里的一行摘要：一个模型直接显示，多个只留首个 + 计数。
 *
 * 表格的模型列只有一百多像素，全部铺成 chip 会把行撑成两三行；完整清单在
 * 展开的下拉里，以及下方生效面板的分段说明里。
 */
export function selectionSummary(values: string[]): string {
  const [first, ...rest] = values
  if (!first) return ''
  const head = first === '*' ? '全部模型' : first
  return rest.length > 0 ? `${head} +${rest.length}` : head
}
