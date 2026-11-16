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
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { FormLabel } from '@/components/ui/form'

type PrimaryEndpointSummaryProps = {
  /** 一行文字：几条路由、中心在哪。 */
  summary: string
  /** 入口清单，已被上游截断到预览条数。 */
  labels: string[]
  hiddenCount: number
  hiddenTitle?: string
  invalid: boolean
}

// 高级自定义渠道的「路由摘要」。
//
// 从 drawer 里整块搬出来：那 41 行压在 advanced_custom 这个 FormItem 内部，
// 正是上游近两次改动覆盖的位置（4559 / 4573）。搬出来以后 drawer 里只剩一个
// 调用，冲突时能一眼看出这段属于谁。
//
// 只接收已经算好的值，不自己取 form：摘要的输入（主端点、镜像协议、路由统计）
// 同时被上游的 FormItem 消费，放在组件里算会造成两处读取同一份 formValues。
export function PrimaryEndpointSummary({
  summary,
  labels,
  hiddenCount,
  hiddenTitle,
  invalid,
}: PrimaryEndpointSummaryProps) {
  const { t } = useTranslation()

  return (
    <div className='min-w-0 space-y-2'>
      {/* 标题行右侧是状态（几条 + 中心），下一行才是入口清单。
          状态用文字、入口用 badge：两者不是同类项，不该同一种视觉。 */}
      <div className='flex min-w-0 items-baseline gap-x-2 gap-y-1'>
        <FormLabel className='shrink-0'>
          {t('Advanced Custom Routes')}
        </FormLabel>
        <span
          className='text-muted-foreground min-w-0 text-xs break-all'
          title={summary}
        >
          {summary}
        </span>
      </div>
      <div className='flex flex-wrap gap-1.5'>
        {labels.map((label) => (
          <Badge
            key={label}
            variant='outline'
            className='max-w-[12rem] font-normal'
            title={label}
          >
            <span className='truncate'>{label}</span>
          </Badge>
        ))}
        {hiddenCount > 0 && (
          <Badge variant='outline' title={hiddenTitle}>
            +{hiddenCount}
          </Badge>
        )}
        {invalid && <Badge variant='destructive'>{t('Incomplete')}</Badge>}
      </div>
    </div>
  )
}
