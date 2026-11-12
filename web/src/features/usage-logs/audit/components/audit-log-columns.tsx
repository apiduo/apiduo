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
import type { ColumnDef } from '@tanstack/react-table'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { TruncatedCell } from '@/components/data-table'
import { StatusBadge } from '@/components/status-badge'
import dayjs from '@/lib/dayjs'

import type { AuditLog } from '../api'
import { buildAuditDetails } from '../lib/audit-details'
import { AuditLogDetailsDialog } from './audit-log-details-dialog'

export function useAuditLogColumns(
  accessOnly?: boolean
): ColumnDef<AuditLog>[] {
  const { t } = useTranslation()
  return useMemo(() => {
    const columns: ColumnDef<AuditLog>[] = [
      {
        accessorKey: 'created_at',
        header: t('Time'),
        size: 180,
        cell: ({ row }) => (
          <span className='font-mono tabular-nums'>
            {dayjs.unix(row.original.created_at).format('YYYY-MM-DD HH:mm:ss')}
          </span>
        ),
        meta: { label: t('Time'), mobileTitle: true },
      },
    ]
    if (!accessOnly) {
      columns.push(
        {
          accessorKey: 'username',
          header: t('Username'),
          size: 100,
          meta: { label: t('Username') },
        },
        {
          id: 'event',
          header: t('Event'),
          size: 360,
          accessorFn: (entry) => {
            const detail = buildAuditDetails(entry, t)
            return [detail.summary, detail.operation?.description]
              .filter(Boolean)
              .join(' · ')
          },
          cell: ({ row, getValue }) => {
            const operation = buildAuditDetails(row.original, t).operation
            if (!operation) {
              return (
                <TruncatedCell className='max-w-64'>
                  {getValue<string>()}
                </TruncatedCell>
              )
            }
            return (
              <div className='min-w-0 space-y-1'>
                <div className='flex min-w-0 items-baseline gap-1'>
                  <TruncatedCell
                    className='min-w-0 font-medium'
                    tooltipContent={operation.summary}
                  >
                    {operation.headline}
                  </TruncatedCell>
                  {operation.identifier && (
                    <span className='shrink-0 whitespace-nowrap'>
                      {operation.identifier}
                    </span>
                  )}
                </div>
                {operation.description && (
                  <TruncatedCell
                    className='text-muted-foreground'
                    contentClassName='line-clamp-2 whitespace-normal break-words'
                    tooltipContent={operation.description}
                  >
                    {operation.description}
                  </TruncatedCell>
                )}
              </div>
            )
          },
          meta: { label: t('Event') },
        }
      )
    }
    columns.push(
      {
        accessorKey: 'ip',
        header: 'IP',
        size: 120,
        cell: ({ row }) => (
          <span className='font-mono'>{row.original.ip || '—'}</span>
        ),
        meta: { label: 'IP' },
      },
      {
        // APIDUO:audit-token-column:BEGIN
        // 列里放令牌名而不是指纹：64 位十六进制在这个宽度读不出信息，
        // 追责要的是「哪把钥匙」。指纹留在 tooltip 与详情，需要精确定位时再取。
        //
        // 这一列占的是原来 Client（User-Agent）的位置 —— UA 几乎每条都一样，
        // 信息密度最低，而且详情里没丢。横向空间只够放一个。
        id: 'token',
        header: t('Token'),
        size: 160,
        accessorFn: (entry) =>
          [entry.token_agent_key, entry.token_label].filter(Boolean).join(' · ') ||
          entry.token_ref ||
          '',
        cell: ({ row }) => {
          const entry = row.original
          const label = [entry.token_agent_key, entry.token_label]
            .filter(Boolean)
            .join(' · ')
          if (!label) {
            // 令牌被删（或 session 请求本就没有令牌）。保留指纹前 8 位而不是留空：
            // 空白会让人读成「这条没有令牌」，而事实可能是「有，只是钥匙已回收」。
            return (
              <span className='text-muted-foreground font-mono'>
                {entry.token_ref ? `${entry.token_ref.slice(0, 8)}…` : '—'}
              </span>
            )
          }
          return (
            <TruncatedCell
              className='max-w-40'
              tooltipContent={entry.token_ref || undefined}
            >
              {label}
            </TruncatedCell>
          )
        },
        meta: { label: t('Token') },
        // APIDUO:audit-token-column:END
      },
      {
        accessorKey: 'method',
        header: t('Method'),
        size: 76,
        cell: ({ row }) => (
          <span className='text-muted-foreground font-mono'>
            {row.original.method || '—'}
          </span>
        ),
        meta: { label: t('Method') },
      },
      {
        accessorKey: 'route',
        header: t('Route'),
        size: 220,
        cell: ({ row }) => (
          <TruncatedCell className='max-w-60 font-mono'>
            {row.original.route || '—'}
          </TruncatedCell>
        ),
        meta: { label: t('Route') },
      },
      {
        accessorKey: 'status',
        header: 'HTTP',
        size: 60,
        cell: ({ row }) => (
          <span className='font-mono tabular-nums'>
            {row.original.status || '—'}
          </span>
        ),
        meta: { label: 'HTTP' },
      },
      {
        accessorKey: 'success',
        header: t('Result'),
        size: 82,
        cell: ({ row }) => (
          <StatusBadge
            label={row.original.success ? t('Success') : t('Failed')}
            variant={row.original.success ? 'success' : 'danger'}
            copyable={false}
          />
        ),
        meta: { label: t('Result'), mobileBadge: true },
      },
      {
        id: 'details',
        header: t('Details'),
        size: 70,
        enableHiding: false,
        cell: ({ row }) => <AuditLogDetailsDialog entry={row.original} />,
        meta: { label: t('Details') },
      }
    )
    return columns
  }, [accessOnly, t])
}
