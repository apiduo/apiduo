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

import { cn } from '@/lib/utils'

import type { AuditChange, AuditChangeValue } from '../lib/audit-changes'

/** 值的渲染：类型决定形态，旧值统一压灰。 */
function ChangeValue(props: { value: AuditChangeValue; muted?: boolean }) {
  const { value } = props
  if (value.kind === 'null' || value.kind === 'boolean') {
    return (
      <span
        className={cn(
          'shrink-0 rounded border px-1.5 py-0.5 text-[11px] whitespace-nowrap',
          value.kind === 'null'
            ? 'border-border text-muted-foreground'
            : 'bg-muted text-foreground border-transparent'
        )}
      >
        {value.text}
      </span>
    )
  }
  return (
    <span className='flex min-w-0 items-baseline gap-1.5'>
      <span
        // 长值截断但保留完整内容：审计里的值常有几十个字符，
        // 展开一行会把表格挤成竖排。tooltip 用浏览器原生的，不额外引组件。
        className={cn(
          'truncate font-mono text-[12px]',
          props.muted ? 'text-muted-foreground' : 'text-foreground'
        )}
        title={value.text}
      >
        {value.text}
      </span>
      {value.note && (
        <span className='text-muted-foreground shrink-0 text-[11px]'>
          {value.note}
        </span>
      )}
    </span>
  )
}

/**
 * 变更对比表。
 *
 * 替代原来的嵌套 JSON 树：`{max_ref_images: {before: 3, after: 0}}` 那样渲染，
 * 读的人要自己在两棵子树里对齐同一个字段。表格把「字段 / 旧值 / 新值」放到同一行。
 */
export function AuditChangesTable(props: { changes: AuditChange[] }) {
  const { t } = useTranslation()
  if (props.changes.length === 0) return null

  // create 事件只有初始状态、没有旧值：画成单列，不摆一个空的「旧值 →」。
  // 用 every 而不是 some —— 混着画会让某几行缺列，表格错位比少一列更糟。
  const isInitial = props.changes.every((change) => change.before === null)
  const grid = isInitial
    ? 'grid-cols-[150px_1fr]'
    : 'grid-cols-[150px_1fr_20px_1fr]'

  return (
    <div className='space-y-1.5'>
      <p className='text-muted-foreground text-xs'>
        {isInitial ? t('Initial values') : t('Changes')}
      </p>
      <div className='overflow-hidden rounded-md border'>
        <div
          className={cn(
            'bg-muted/40 text-muted-foreground grid items-center gap-2 px-3 py-1.5 text-[11px]',
            grid
          )}
        >
          <span>{t('Field')}</span>
          {isInitial ? (
            <span>{t('Initial value')}</span>
          ) : (
            <>
              <span>{t('Previous value')}</span>
              <span aria-hidden='true' />
              <span>{t('New value')}</span>
            </>
          )}
        </div>
        {props.changes.map((change) => (
          <div
            key={change.key}
            className={cn(
              'border-t-[0.5px] grid items-center gap-2 px-3 py-2',
              grid
            )}
          >
            <div className='min-w-0'>
              <div className='text-[13px] leading-tight break-words'>
                {change.label}
              </div>
              {/* 有翻译时才补一行原始键名（排障按 key 搜）。没有翻译就直接
                  显示键名，两行一模一样只是噪音。 */}
              {change.label !== change.key && (
                <div className='text-muted-foreground mt-0.5 truncate font-mono text-[11px]'>
                  {change.key}
                </div>
              )}
            </div>
            {!isInitial && (
              <>
                {change.before ? (
                  <ChangeValue value={change.before} muted />
                ) : (
                  <span />
                )}
                <span
                  aria-hidden='true'
                  className='text-muted-foreground text-center text-xs'
                >
                  →
                </span>
              </>
            )}
            <ChangeValue value={change.after} />
          </div>
        ))}
      </div>
    </div>
  )
}
