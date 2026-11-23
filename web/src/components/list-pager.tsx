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
import { ChevronLeft, ChevronRight } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { NativeSelect } from '@/components/ui/native-select'
import { clampPage, pageCount } from '@/lib/pagination'
import { cn } from '@/lib/utils'

const DEFAULT_PAGE_SIZE_OPTIONS = [20, 50, 100] as const

type ListPagerProps = {
  page: number
  pageSize: number
  total: number
  onPageChange: (page: number) => void
  onPageSizeChange?: (pageSize: number) => void
  pageSizeOptions?: readonly number[]
  /** Unit noun for the total, already translated (e.g. "users"). */
  totalLabel?: string
  className?: string
}

/**
 * Pager for lists whose rows are already in memory.
 *
 * Why this exists next to `DataTablePagination`: that one is driven by a
 * tanstack `Table` instance. The analytics pages render plain `<table>` rows
 * from a fetched array, and every one of them that shipped without a pager
 * ended up mounting thousands of rows at once — the page freezes long before
 * the data is a problem.
 */
export function ListPager({
  page,
  pageSize,
  total,
  onPageChange,
  onPageSizeChange,
  pageSizeOptions = DEFAULT_PAGE_SIZE_OPTIONS,
  totalLabel,
  className,
}: ListPagerProps) {
  const { t } = useTranslation()
  const pages = pageCount(total, pageSize)
  const current = clampPage(page, total, pageSize)

  return (
    <div
      className={cn(
        'flex flex-wrap items-center justify-between gap-2 text-xs',
        className
      )}
    >
      <span className='text-muted-foreground tabular-nums'>
        {total.toLocaleString()}
        {totalLabel ? ` ${totalLabel}` : ''}
      </span>
      <div className='flex items-center gap-2'>
        {onPageSizeChange && (
          <NativeSelect
            size='sm'
            className='w-20'
            aria-label={t('Rows per page')}
            value={String(pageSize)}
            onChange={(event) => onPageSizeChange(Number(event.target.value))}
          >
            {pageSizeOptions.map((size) => (
              <option key={size} value={size}>
                {size}
              </option>
            ))}
          </NativeSelect>
        )}
        <span className='text-muted-foreground tabular-nums'>
          {t('Page {{page}} of {{total}}', { page: current, total: pages })}
        </span>
        <Button
          size='icon'
          variant='outline'
          className='size-7'
          aria-label={t('Go to previous page')}
          disabled={current <= 1}
          onClick={() => onPageChange(current - 1)}
        >
          <ChevronLeft className='size-4' />
        </Button>
        <Button
          size='icon'
          variant='outline'
          className='size-7'
          aria-label={t('Go to next page')}
          disabled={current >= pages}
          onClick={() => onPageChange(current + 1)}
        >
          <ChevronRight className='size-4' />
        </Button>
      </div>
    </div>
  )
}
