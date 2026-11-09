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
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { AlertTriangle, Database, Pin } from 'lucide-react'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { apiduoBlockMark } from '@/features/apiduo-marks/marks'
import { formatNumber, formatPercent } from '@/lib/format'
import { getMissingModels, getModels } from '@/features/models/api'
import type { Model } from '@/features/models/types'

import type { QuotaDataItem } from '../../types'
import { buildCatalogHealth } from '../../lib/catalog-health'
import { PanelWrapper } from '../ui/panel-wrapper'
import { StatCard } from '../ui/stat-card'

/**
 * 目录健康：元数据缺口，按调用量加权。
 *
 * 数据不新增接口 —— 调用量来自父级已经加载好的 `data`，
 * 元数据来自现成的 /api/models 与 /api/models/missing。
 *
 * 口径说明：覆盖率与时间范围无关（元数据是静态属性）；
 * 受时间范围影响的只有"缺口表按调用量排序"和两张调用量占比卡片。
 */

const PAGE_SIZE = 500

/** 元数据接口是分页的，但面板要的是全集：先取第一页，再补剩余页。 */
async function loadAllModels(): Promise<Model[]> {
  const first = await getModels({ p: 1, page_size: PAGE_SIZE })
  const items = first.data?.items ?? []
  const total = first.data?.total ?? items.length
  if (items.length >= total) return items

  const pages = Math.ceil(total / PAGE_SIZE)
  const rest = await Promise.all(
    Array.from({ length: pages - 1 }, (_, index) =>
      getModels({ p: index + 2, page_size: PAGE_SIZE }).then(
        (res) => res.data?.items ?? []
      )
    )
  )
  return items.concat(...rest)
}

function CoverageBar(props: { value: number; tone: 'calls' | 'count' }) {
  return (
    <div
      className={
        props.tone === 'calls'
          ? 'bg-muted h-2 w-full overflow-hidden rounded-full'
          : 'bg-muted/60 h-1 w-full overflow-hidden rounded-full'
      }
    >
      <div
        className={
          props.tone === 'calls'
            ? 'bg-primary h-full rounded-full transition-[width] duration-500'
            : 'bg-muted-foreground/50 h-full rounded-full transition-[width] duration-500'
        }
        style={{ width: `${Math.max(0, Math.min(100, props.value))}%` }}
      />
    </div>
  )
}

export function CatalogHealthPanel(props: {
  data: QuotaDataItem[]
  loading?: boolean
}) {
  const { t } = useTranslation()

  const modelsQuery = useQuery({
    queryKey: ['catalog-health', 'models'],
    queryFn: loadAllModels,
    staleTime: 60_000,
  })
  const missingQuery = useQuery({
    queryKey: ['catalog-health', 'missing'],
    queryFn: async () => {
      const res = await getMissingModels()
      return res.data ?? []
    },
    staleTime: 60_000,
  })

  const stats = useMemo(() => {
    if (!modelsQuery.data) return null
    return buildCatalogHealth(
      modelsQuery.data,
      props.data,
      missingQuery.data ?? []
    )
  }, [modelsQuery.data, missingQuery.data, props.data])

  const loading = props.loading || modelsQuery.isLoading
  const failed = modelsQuery.isError

  return (
    <PanelWrapper
      title={t('Catalog health')}
      description={t(
        'Metadata gaps weighted by call volume. Coverage does not depend on the time range.'
      )}
      loading={loading}
      empty={!loading && !failed && (stats?.totalModels ?? 0) === 0}
      emptyMessage={t('No models configured yet')}
      height='h-40'
      contentClassName='sm:p-5'
    >
      {failed || !stats ? (
        <p className='text-muted-foreground text-sm'>
          {t('Failed to load model metadata')}
        </p>
      ) : (
        <div className='flex flex-col gap-5'>
          <div className='grid gap-3 sm:grid-cols-3'>
            <StatCard
              title={t('Complete metadata (by calls)')}
              value={formatPercent(stats.completeShare)}
              description={t('{{count}} models configured', {
                count: stats.totalModels,
              })}
              icon={Database}
              tone='accent-1'
            />
            <StatCard
              title={t('Missing models')}
              value={formatNumber(stats.missingCount)}
              description={t('{{share}} of calls', {
                share: formatPercent(stats.missingShare),
              })}
              icon={AlertTriangle}
              tone='accent-2'
            />
            <StatCard
              title={t('Manually managed')}
              value={formatNumber(stats.pinnedCount)}
              description={t('Excluded from upstream sync')}
              icon={Pin}
              tone='accent-3'
            />
          </div>

          <div className='flex flex-col gap-3'>
            <div className='text-muted-foreground grid grid-cols-[7rem_1fr_6rem] items-center gap-3 text-[11px]'>
              <span>{t('Field')}</span>
              <span>{t('By calls / by model count')}</span>
              <span className='text-right'>{t('Coverage')}</span>
            </div>
            {stats.fields.map((field) => (
              <div
                key={field.key}
                className='grid grid-cols-[7rem_1fr_6rem] items-center gap-3'
              >
                <span className='truncate text-xs'>{t(field.label)}</span>
                <span className='flex flex-col gap-1'>
                  <CoverageBar value={field.byCalls} tone='calls' />
                  <CoverageBar value={field.byCount} tone='count' />
                </span>
                <span className='flex flex-col items-end gap-0.5'>
                  <span className='text-xs font-medium tabular-nums'>
                    {formatPercent(field.byCalls)}
                  </span>
                  <span className='text-muted-foreground text-[10px] tabular-nums'>
                    {formatPercent(field.byCount)} · {field.missing}
                  </span>
                </span>
              </div>
            ))}
          </div>

          <div
            data-apiduo={apiduoBlockMark('dashboard:catalog-health')}
            className='flex flex-col gap-2'
          >
            <h4 className='text-xs font-semibold'>{t('Gap list')}</h4>
            <div className='overflow-x-auto'>
            <table className='w-full text-left text-xs'>
              <thead className='text-muted-foreground border-b text-[11px]'>
                <tr>
                  <th className='py-2 pr-3 font-normal'>{t('Model')}</th>
                  <th className='py-2 pr-3 font-normal'>{t('Missing fields')}</th>
                  <th className='py-2 pr-3 text-right font-normal'>
                    {t('Calls')}
                  </th>
                  <th className='py-2 pr-3 text-right font-normal'>
                    {t('Share')}
                  </th>
                  <th className='py-2 text-right font-normal'>
                    {t('Action')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {stats.gaps.map((row) => (
                  <tr key={row.name} className='border-b last:border-0'>
                    <td className='max-w-[16rem] truncate py-2 pr-3 font-mono'>
                      {row.name}
                    </td>
                    <td className='py-2 pr-3'>
                      <span className='text-muted-foreground'>
                        {row.missing.map((item) => t(item)).join(' · ')}
                      </span>
                    </td>
                    <td className='py-2 pr-3 text-right tabular-nums'>
                      {formatNumber(row.calls)}
                    </td>
                    <td className='text-muted-foreground py-2 pr-3 text-right tabular-nums'>
                      {formatPercent(row.share)}
                    </td>
                    <td className='py-2 text-right'>
                      <Link
                        to='/models/$section'
                        params={{ section: 'metadata' }}
                        search={{ filter: row.name }}
                        className='text-primary text-[11px] font-medium hover:underline'
                      >
                        {t('Fix')}
                      </Link>
                    </td>
                  </tr>
                ))}
                {stats.gaps.length === 0 && (
                  <tr>
                    <td
                      colSpan={5}
                      className='text-muted-foreground py-6 text-center'
                    >
                      {t('Every model has complete metadata')}
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
            </div>
          </div>

          <p className='text-muted-foreground text-[11px]'>
            {t(
              '{{gaps}} models with gaps. Context length, modalities and release date are not shown until those fields land.',
              { gaps: formatNumber(stats.gapCount) }
            )}
          </p>
        </div>
      )}
    </PanelWrapper>
  )
}
