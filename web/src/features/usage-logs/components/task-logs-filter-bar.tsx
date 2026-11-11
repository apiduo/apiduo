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
/* APIDUO:task-filters:BEGIN
   任务日志筛选栏整体重写：加模型/状态维度、重排布局、绘图日志保持原维度。
   这个组件在本仓已完全自有，逐段标记只会把合并面切得更碎，故整份入块。 */
import { useQueryClient, useIsFetching } from '@tanstack/react-query'
import { useNavigate, getRouteApi } from '@tanstack/react-router'
import type { Table } from '@tanstack/react-table'
import { useState, useEffect, useCallback, useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import { buildSearchParams } from '../lib/filter'
import { getDefaultTimeRange } from '../lib/utils'
import type { DrawingLogFilters, LogCategory, TaskLogFilters } from '../types'
import { CompactDateTimeRangePicker } from './compact-date-time-range-picker'
import {
  LogsFilterField,
  LogsFilterInput,
  LogsFilterToolbar,
} from './logs-filter-toolbar'
import { useLogsViewScope } from './usage-logs-provider'

const route = getRouteApi('/_authenticated/usage-logs/$section')

type TaskLikeLogCategory = Extract<LogCategory, 'drawing' | 'task'>
type TaskLogsFilters = DrawingLogFilters | TaskLogFilters

// 「排队中」在库里是三个状态，一个下拉选项要能表达集合，所以 value 直接就是
// 后端接受的逗号串。顺序跟着任务生命周期走，找失败任务的人从下往上扫。
const TASK_STATUS_FILTERS = [
  { value: 'SUBMITTED,QUEUED,NOT_START', label: 'Queued' },
  { value: 'IN_PROGRESS', label: 'In Progress' },
  { value: 'SUCCESS', label: 'Success' },
  { value: 'FAILURE', label: 'Failed' },
] as const
const TASK_STATUS_ALL = 'all'

interface TaskLogsFilterBarProps<TData> {
  table: Table<TData>
  logCategory: TaskLikeLogCategory
}

function getFilterValue(
  filters: TaskLogsFilters,
  logCategory: TaskLikeLogCategory
): string {
  if (logCategory === 'drawing') {
    return (filters as DrawingLogFilters).mjId || ''
  }
  return (filters as TaskLogFilters).taskId || ''
}

function setFilterValue(
  filters: TaskLogsFilters,
  logCategory: TaskLikeLogCategory,
  value: string
): TaskLogsFilters {
  if (logCategory === 'drawing') {
    return { ...filters, mjId: value }
  }
  return { ...filters, taskId: value }
}

export function TaskLogsFilterBar<TData>(props: TaskLogsFilterBarProps<TData>) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const searchParams = route.useSearch()
  const { isAdminView: isAdmin } = useLogsViewScope()
  const fetchingLogs = useIsFetching({ queryKey: ['logs'] })
  const isTaskLogs = props.logCategory === 'task'

  const [filters, setFilters] = useState<TaskLogsFilters>(() => {
    const { start, end } = getDefaultTimeRange()
    return { startTime: start, endTime: end }
  })

  useEffect(() => {
    const { start, end } = getDefaultTimeRange()
    const baseFilters = {
      startTime: searchParams.startTime
        ? new Date(searchParams.startTime)
        : start,
      endTime: searchParams.endTime ? new Date(searchParams.endTime) : end,
      ...(searchParams.channel
        ? { channel: String(searchParams.channel) }
        : {}),
    }
    const next: TaskLogsFilters = isTaskLogs
      ? {
          ...baseFilters,
          ...(searchParams.filter ? { taskId: searchParams.filter } : {}),
          ...(searchParams.model ? { model: searchParams.model } : {}),
          ...(searchParams.status ? { status: searchParams.status } : {}),
        }
      : {
          ...baseFilters,
          ...(searchParams.filter ? { mjId: searchParams.filter } : {}),
        }

    setFilters(next)
  }, [
    isTaskLogs,
    searchParams.startTime,
    searchParams.endTime,
    searchParams.channel,
    searchParams.filter,
    searchParams.model,
    searchParams.status,
  ])

  const handleChange = useCallback(
    (field: keyof TaskLogsFilters, value: Date | string | undefined) => {
      setFilters((prev) => ({ ...prev, [field]: value }))
    },
    []
  )

  const handleApply = useCallback(() => {
    const filterParams = buildSearchParams(filters, props.logCategory)
    navigate({
      to: '/usage-logs/$section',
      params: { section: props.logCategory },
      search: {
        ...filterParams,
        page: 1,
      },
    })
    queryClient.invalidateQueries({ queryKey: ['logs'] })
  }, [filters, navigate, props.logCategory, queryClient])

  const handleReset = useCallback(() => {
    const { start, end } = getDefaultTimeRange()
    const resetFilters: TaskLogsFilters = { startTime: start, endTime: end }
    setFilters(resetFilters)

    navigate({
      to: '/usage-logs/$section',
      params: { section: props.logCategory },
      search: {
        page: 1,
        startTime: start.getTime(),
        endTime: end.getTime(),
      },
    })
    queryClient.invalidateQueries({ queryKey: ['logs'] })
  }, [navigate, props.logCategory, queryClient])

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (e.key === 'Enter') handleApply()
    },
    [handleApply]
  )

  const handleFilterChange = useCallback(
    (value: string) => {
      setFilters((prev) => setFilterValue(prev, props.logCategory, value))
    },
    [props.logCategory]
  )

  const taskFilters = filters as TaskLogFilters
  const filterValue = getFilterValue(filters, props.logCategory)
  const placeholder = isTaskLogs
    ? t('Filter by task ID')
    : t('Filter by MjProxy task ID')
  const statusItems = useMemo(
    () => [
      { value: TASK_STATUS_ALL, label: t('All Status') },
      ...TASK_STATUS_FILTERS.map((status) => ({
        value: status.value,
        label: t(status.label),
      })),
    ],
    [t]
  )
  const statusValue = taskFilters.status || TASK_STATUS_ALL
  const selectedStatusLabel =
    statusItems.find((item) => item.value === statusValue)?.label ??
    t('All Status')
  const hasAdditionalFilters =
    !!filterValue ||
    !!filters.channel ||
    !!taskFilters.model ||
    !!taskFilters.status

  const dateRangeFilter = (
    <LogsFilterField wide>
      <CompactDateTimeRangePicker
        start={filters.startTime}
        end={filters.endTime}
        onChange={({ start, end }) => {
          handleChange('startTime', start)
          handleChange('endTime', end)
        }}
      />
    </LogsFilterField>
  )
  const taskIdFilter = (
    <LogsFilterField>
      <LogsFilterInput
        aria-label={t('Task ID')}
        placeholder={placeholder}
        value={filterValue}
        onChange={(e) => handleFilterChange(e.target.value)}
        onKeyDown={handleKeyDown}
      />
    </LogsFilterField>
  )
  // 绘图日志没有模型维度（MjProxy 的模型不在任务表上），只在任务日志渲染。
  const modelFilter = isTaskLogs ? (
    <LogsFilterField>
      <LogsFilterInput
        aria-label={t('Model Name')}
        placeholder={t('Model Name')}
        value={taskFilters.model || ''}
        onChange={(e) => handleChange('model', e.target.value)}
        onKeyDown={handleKeyDown}
      />
    </LogsFilterField>
  ) : null
  const statusFilter = isTaskLogs ? (
    <LogsFilterField>
      <Select
        items={statusItems}
        value={statusValue}
        onValueChange={(value) => {
          const next = value ?? TASK_STATUS_ALL
          handleChange('status', next === TASK_STATUS_ALL ? '' : next)
        }}
      >
        <SelectTrigger aria-label={t('Status')}>
          <SelectValue className='min-w-0'>
            <span className='truncate'>{selectedStatusLabel}</span>
          </SelectValue>
        </SelectTrigger>
        <SelectContent alignItemWithTrigger={false}>
          <SelectGroup>
            {statusItems.map((item) => (
              <SelectItem key={item.value} value={item.value}>
                {item.label}
              </SelectItem>
            ))}
          </SelectGroup>
        </SelectContent>
      </Select>
    </LogsFilterField>
  ) : null
  const channelFilter = isAdmin ? (
    <LogsFilterField>
      <LogsFilterInput
        aria-label={t('Channel ID')}
        placeholder={t('Channel ID')}
        value={filters.channel || ''}
        onChange={(e) => handleChange('channel', e.target.value)}
        onKeyDown={handleKeyDown}
      />
    </LogsFilterField>
  ) : null

  return (
    <LogsFilterToolbar
      table={props.table}
      primaryFilters={
        <>
          {dateRangeFilter}
          {modelFilter}
          {statusFilter}
          {taskIdFilter}
          {channelFilter}
        </>
      }
      mobilePinnedFilters={dateRangeFilter}
      mobileFilters={
        <>
          {modelFilter}
          {statusFilter}
          {taskIdFilter}
          {channelFilter}
        </>
      }
      mobileFilterCount={
        [filterValue, filters.channel, taskFilters.model, taskFilters.status]
          .filter(Boolean).length
      }
      hasActiveFilters={hasAdditionalFilters}
      onSearch={handleApply}
      searchLoading={fetchingLogs > 0}
      onReset={handleReset}
    />
  )
}
/* APIDUO:task-filters:END */
