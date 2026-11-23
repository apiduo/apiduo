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
// 账单特有的格式化。金额不读运行期汇率，也不做除法：
// 服务端下发的 amount 就是结算值，前端只负责排版。

/** 把 6 位小数的金额字符串排成带币种的显示值。 */
export function formatMoney(amount: string, currency: string): string {
  const numeric = Number(amount)
  if (!Number.isFinite(numeric)) return amount
  try {
    return new Intl.NumberFormat('zh-CN', {
      style: 'currency',
      currency: currency || 'CNY',
      minimumFractionDigits: 2,
      maximumFractionDigits: 4,
    }).format(numeric)
  } catch {
    // 币种代码不合法时退回数字，不要让整页渲染挂掉。
    return `${numeric.toFixed(2)} ${currency}`
  }
}

/** 文件大小。归档文件通常几 MB，用 KB/MB 比字节数好读。 */
export function formatFileSize(bytes: number): string {
  if (!bytes || bytes <= 0) return '-'
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(2)} MB`
}

/** 当前账期，按本地时区。服务端按东八区切，这里只用于默认选项。 */
export function currentBillingPeriod(): string {
  const now = new Date()
  return `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}`
}

/** 最近 count 个账期，从当月往前。 */
export function recentBillingPeriods(count: number): string[] {
  const now = new Date()
  const periods: string[] = []
  for (let offset = 0; offset < count; offset += 1) {
    const month = new Date(now.getFullYear(), now.getMonth() - offset, 1)
    periods.push(
      `${month.getFullYear()}-${String(month.getMonth() + 1).padStart(2, '0')}`
    )
  }
  return periods
}

/** 账期显示为「2026 年 10 月」。 */
export function formatBillingPeriod(period: string): string {
  const match = /^(\d{4})-(\d{2})$/.exec(period)
  if (!match) return period
  return `${match[1]} 年 ${Number(match[2])} 月`
}

/** 明细类型。与后端日志类型常量一致：2 消费 / 5 失败 / 6 退款。 */
export function billingLineTypeKey(type: number): string {
  switch (type) {
    case 2:
      return 'Consume'
    case 5:
      return 'Failed'
    case 6:
      return 'Refund'
  }
  return 'Other'
}
