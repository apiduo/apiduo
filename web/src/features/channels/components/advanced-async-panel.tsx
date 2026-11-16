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
import { Route } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { UseFormReturn } from 'react-hook-form'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'

import type { ChannelFormValues } from '../lib'
import { getAdvancedAsyncStats } from '../lib/advanced-async'
import { AdvancedAsyncEditorDialog } from './dialogs/advanced-async-editor-dialog'

type AdvancedAsyncPanelProps = {
  form: UseFormReturn<ChannelFormValues>
  /** 敏感字段锁定时不弹编辑框：改的是上游路由，属敏感配置。 */
  locked: boolean
}

// 高级异步渠道的路由面板与编辑器。
//
// 整块从 drawer 搬出来：那 70 行原本夹在上游的 FormField 序列中间，而这一段
// 正是上游近两次改动覆盖的位置（4590-4657）—— 留在原处，每次上游动这块都要
// 重解一遍。搬出来以后 drawer 里只剩一行调用，冲突时可以直接取上游版本再补一行。
//
// 编辑器放在组件内部而不是 drawer 根节点：DialogContent 自带 portal，
// 渲染位置不影响 DOM 结构，这样 open 状态与面板同生共死，不必再往 drawer 传。
export function AdvancedAsyncPanel({
  form,
  locked,
}: AdvancedAsyncPanelProps) {
  const { t } = useTranslation()
  const [editorOpen, setEditorOpen] = useState(false)
  const value = form.watch('advanced_async')
  const stats = useMemo(() => getAdvancedAsyncStats(value), [value])

  return (
    <>
      <FormField
        control={form.control}
        name='advanced_async'
        render={({ field }) => (
          <FormItem className='space-y-3 border-b py-4'>
            <div className='space-y-3'>
              <div className='flex items-start justify-between gap-3'>
                <div className='min-w-0 space-y-1'>
                  <FormLabel>{t('Advanced async routes')}</FormLabel>
                  <p className='text-muted-foreground text-xs'>
                    {t(
                      'Describe an upstream task API. The gateway submits the work and polls for the result.'
                    )}
                  </p>
                </div>
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  className='shrink-0'
                  onClick={() => setEditorOpen(true)}
                >
                  <Route className='mr-2 h-4 w-4' />
                  {t('Configure async routes')}
                </Button>
              </div>
              <div className='flex flex-wrap items-center gap-2'>
                <Badge variant={stats.configured ? 'secondary' : 'outline'}>
                  {stats.configured ? t('Configured') : t('Not configured')}
                </Badge>
                {stats.configured && !stats.valid ? (
                  <Badge variant='destructive'>{t('Incomplete')}</Badge>
                ) : null}
              </div>
              {stats.incomingPaths.length > 0 ? (
                <ul className='space-y-1'>
                  {stats.incomingPaths.map((path) => (
                    <li
                      key={path}
                      title={path}
                      className='bg-muted/40 text-muted-foreground w-full truncate rounded-md border px-2 py-1 font-mono text-xs'
                    >
                      {path}
                    </li>
                  ))}
                </ul>
              ) : null}
            </div>
            <FormControl>
              <input type='hidden' {...field} />
            </FormControl>
            <FormMessage />
          </FormItem>
        )}
      />
      {editorOpen && !locked && (
        <AdvancedAsyncEditorDialog
          open={editorOpen}
          value={value || ''}
          onOpenChange={setEditorOpen}
          onSave={(nextValue) => {
            form.setValue('advanced_async', nextValue, {
              shouldDirty: true,
              shouldValidate: true,
            })
          }}
        />
      )}
    </>
  )
}
