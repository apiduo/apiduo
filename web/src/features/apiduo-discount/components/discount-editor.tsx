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
import { ArrowDown, ArrowUp, Plus, X } from 'lucide-react'
import { useMemo, useState } from 'react'

import { MultiSelect } from '@/components/multi-select'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { apiduoBlockMark } from '@/features/apiduo-marks/marks'
import { cn } from '@/lib/utils'

import { GROUP_LIST_ID, MODEL_LIST_ID } from '../lib/dom-ids'
import {
  dateValueToUntil,
  diagnoseRule,
  findDeadRows,
  hasBlockingError,
  isExpired,
  rateToZhe,
  splitList,
  untilToDateValue,
  zheToRate,
} from '../lib/match'
import {
  mergeModelSelection,
  selectionSummary,
} from '../lib/model-selection'
import { MAX_DISCOUNT_RULES, type DiscountRule } from '../types'
import { EffectivePricePanel } from './effective-price-panel'

export type DiscountEditorProps = {
  rules: DiscountRule[]
  onChange: (rules: DiscountRule[]) => void
  /** 全站分组，来自 /api/group/ */
  groups: string[]
  /** 该用户可用的分组，来自折扣接口；空数组表示未知，不提示可用性 */
  usableGroups: string[]
  /** 分组倍率，来自 /api/pricing，用于展示「组价」基线 */
  groupRatios: Record<string, number>
  /** 模型名候选 */
  models: string[]
  /** 定价还在拉取：此时不能断言「暂无模型」 */
  modelsLoading?: boolean
  disabled?: boolean
}

function newRule(defaultGroup: string): DiscountRule {
  return { groups: defaultGroup || '*', models: '*', rate: 0.8, until: 0 }
}

export function DiscountEditor({
  rules,
  onChange,
  groups,
  usableGroups,
  groupRatios,
  models,
  modelsLoading = false,
  disabled = false,
}: DiscountEditorProps) {
  // 「折」的输入草稿：受控 input 直接回写数字会吃掉用户正在输入的 "7."。
  const [rateDrafts, setRateDrafts] = useState<Record<number, string>>({})

  const nowSeconds = Math.floor(Date.now() / 1000)
  const modelSet = useMemo(() => new Set(models), [models])

  const deadRows = useMemo(
    () => findDeadRows(rules, usableGroups, nowSeconds),
    // nowSeconds 只是时间基线，随渲染变化没有意义；按规则与可用分组重算即可
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [rules, usableGroups]
  )

  const diagnoses = useMemo(
    () =>
      rules.map((rule, index) =>
        diagnoseRule(rule, {
          knownGroups: groups,
          usableGroups,
          knownModels: modelSet,
          groupRatios,
          nowSeconds,
          dead: deadRows[index] === true,
          messages: {
            groupsRequired: '填分组，或 * 表示全部',
            modelsRequired: '填模型，或 * 表示全部',
            rateRange: '折扣要在 1–10 折之间',
            groupUnknown: (group) => `分组「${group}」不存在`,
            groupUnusable: (group) => `该用户用不到「${group}」，这条永不命中`,
            modelUnknown: (list) =>
              `模型「${list.slice(0, 2).join('、')}」${
                list.length > 2 ? ` 等 ${list.length} 个` : ''
              }不在目录里，确认没拼错`,
            priceIncrease: (group, baseRatio, rate) =>
              `「${group}」组价 ${baseRatio.toFixed(2)}，这条 ${rate.toFixed(
                2
              )} 是涨价 ${((rate / baseRatio - 1) * 100).toFixed(1)}%`,
            globalScope: '对全部分组、全部模型生效，含以后新增的模型',
          },
        })
      ),
    // nowSeconds 只是时间基线，随渲染变化没有意义；按规则与可用分组重算即可
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [rules, groups, usableGroups, modelSet, groupRatios, deadRows]
  )

  const blockingCount = diagnoses.filter(hasBlockingError).length

  let modelHint = ' · 暂无模型（未配置渠道）'
  if (modelsLoading) modelHint = ' · 模型候选加载中…'
  else if (models.length > 0) modelHint = ` · 模型候选 ${models.length} 个`

  const modelOptions = useMemo(
    () => [
      { label: '全部模型（*）', value: '*' },
      ...models.map((model) => ({ label: model, value: model })),
    ],
    [models]
  )

  const update = (index: number, patch: Partial<DiscountRule>) => {
    onChange(rules.map((rule, i) => (i === index ? { ...rule, ...patch } : rule)))
  }

  const move = (index: number, offset: number) => {
    const target = index + offset
    if (target < 0 || target >= rules.length) return
    const next = [...rules]
    const [item] = next.splice(index, 1)
    next.splice(target, 0, item)
    setRateDrafts({})
    onChange(next)
  }

  const remove = (index: number) => {
    setRateDrafts({})
    onChange(rules.filter((_, i) => i !== index))
  }

  const add = () => {
    if (rules.length >= MAX_DISCOUNT_RULES) return
    setRateDrafts({})
    onChange([...rules, newRule(usableGroups[0] ?? groups[0] ?? '*')])
  }

  return (
    <div
      data-apiduo={apiduoBlockMark('user:model-discounts')}
      className='space-y-4'
    >
      <datalist id={GROUP_LIST_ID}>
        {groups.map((group) => (
          <option key={group} value={group} />
        ))}
        <option value='*' />
      </datalist>
      <datalist id={MODEL_LIST_ID}>
        {models.map((model) => (
          <option key={model} value={model} />
        ))}
        <option value='*' />
      </datalist>

      <div className='flex items-start justify-between gap-3'>
        <div className='min-w-0'>
          <div className='text-sm font-medium'>模型折扣</div>
          <p className='text-muted-foreground mt-1 text-[12px] leading-relaxed'>
            从上往下第一条命中即停，不相乘。分组和模型都可写多个（逗号分隔），
            <code className='mx-0.5 font-mono'>*</code>
            表示全部，到期留空表示永久。
          </p>
        </div>
        <span className='text-muted-foreground shrink-0 font-mono text-[11.5px] tabular-nums'>
          {rules.length}/{MAX_DISCOUNT_RULES}
        </span>
      </div>

      <div className='text-muted-foreground text-[11.5px]'>
        分组 {groups.length} 个 · 该用户可用 {usableGroups.length} 个
        {modelHint}
      </div>

      <div className='hidden grid-cols-[18px_84px_minmax(0,1fr)_52px_96px_64px] items-center gap-2 border-b pb-1.5 text-[11.5px] text-muted-foreground sm:grid'>
        <span />
        <span>分组</span>
        <span>模型</span>
        <span>折扣</span>
        <span>到期</span>
        <span />
      </div>

      <div className='space-y-2 empty:hidden sm:space-y-0'>
        {rules.map((rule, index) => {
          const diagnosis = diagnoses[index]
          const expired = isExpired(rule, nowSeconds)
          const isDead = deadRows[index] && !hasBlockingError(diagnosis)
          const selectedModels = splitList(rule.models)
          const rowMessages = [
            ...(diagnosis.errors.groups ? [diagnosis.errors.groups] : []),
            ...(diagnosis.errors.models ? [diagnosis.errors.models] : []),
            ...(diagnosis.errors.rate ? [diagnosis.errors.rate] : []),
            ...diagnosis.warnings,
          ]

          return (
            <div
              key={index}
              className={cn(
                'grid grid-cols-2 gap-x-2 gap-y-1.5 rounded-lg border p-2',
                'sm:grid-cols-[18px_84px_minmax(0,1fr)_52px_96px_64px] sm:items-center sm:gap-2 sm:rounded-none sm:border-0 sm:border-b sm:border-border/60 sm:p-0 sm:py-2',
                (expired || isDead) && 'opacity-50'
              )}
            >
              <span className='text-muted-foreground hidden text-right font-mono text-[10px] tabular-nums sm:block'>
                {index + 1}
              </span>

              <div className='col-span-1 min-w-0'>
                <span className='text-muted-foreground mb-1 block text-[10.5px] sm:hidden'>
                  分组
                </span>
                <Input
                  aria-label={`第 ${index + 1} 行分组`}
                  className='h-8 px-2 font-mono text-[12px]'
                  disabled={disabled}
                  list={GROUP_LIST_ID}
                  value={rule.groups}
                  onChange={(event) =>
                    update(index, { groups: event.target.value })
                  }
                />
              </div>

              <div className='col-span-1 min-w-0'>
                <span className='text-muted-foreground mb-1 block text-[10.5px] sm:hidden'>
                  模型
                </span>
                <MultiSelect
                  allowCreate
                  ariaLabel={`第 ${index + 1} 行模型`}
                  className='min-h-8 px-1.5 py-0.5 font-mono text-[12px]'
                  createLabel='使用「{{value}}」'
                  disabled={disabled}
                  onChange={(values) =>
                    update(index, {
                      models: mergeModelSelection(rule.models, values),
                    })
                  }
                  options={modelOptions}
                  placeholder='模型或 * 表示全部'
                  popupClassName='w-[min(22rem,calc(100vw-2rem))]'
                  renderSelectedSummary={
                    selectedModels.length > 0 ? selectionSummary : undefined
                  }
                  selected={selectedModels}
                />
              </div>

              <div className='col-span-1 min-w-0'>
                <span className='text-muted-foreground mb-1 block text-[10.5px] sm:hidden'>
                  折扣
                </span>
                <div className='relative'>
                  <Input
                    aria-label={`第 ${index + 1} 行折扣，单位折`}
                    className={cn(
                      'h-8 px-2 pr-4 text-[12px] tabular-nums',
                      diagnosis.errors.rate && 'border-destructive'
                    )}
                    disabled={disabled}
                    inputMode='decimal'
                    value={rateDrafts[index] ?? rateToZhe(rule.rate)}
                    onChange={(event) => {
                      const text = event.target.value
                      setRateDrafts((draft) => ({ ...draft, [index]: text }))
                      const rate = zheToRate(text)
                      if (Number.isFinite(rate) && rate > 0 && rate < 1) {
                        update(index, { rate })
                      }
                    }}
                    onBlur={() =>
                      setRateDrafts((draft) => {
                        const next = { ...draft }
                        delete next[index]
                        return next
                      })
                    }
                  />
                  <span className='text-muted-foreground pointer-events-none absolute top-1/2 right-1 -translate-y-1/2 text-[10px]'>
                    折
                  </span>
                </div>
              </div>

              <div className='col-span-1 min-w-0'>
                <span className='text-muted-foreground mb-1 block text-[10.5px] sm:hidden'>
                  到期
                </span>
                <div className='group relative'>
                  <Input
                    aria-label={`第 ${index + 1} 行到期日`}
                    className={cn(
                      'h-8 px-1.5 text-[12px] [color-scheme:light] dark:[color-scheme:dark]',
                      // 未设到期时把 Chrome 自带的「年/月/日」占位藏掉，
                      // 换成「永久」；聚焦后再露出来，不影响选日期。
                      rule.until === 0 && 'text-transparent focus:text-current'
                    )}
                    disabled={disabled}
                    type='date'
                    value={untilToDateValue(rule.until)}
                    onChange={(event) =>
                      update(index, {
                        until: dateValueToUntil(event.target.value),
                      })
                    }
                  />
                  {rule.until === 0 && (
                    <span className='text-muted-foreground pointer-events-none absolute inset-y-0 left-1.5 flex items-center text-[11px] group-focus-within:hidden'>
                      永久
                    </span>
                  )}
                </div>
              </div>

              <div className='col-span-2 flex items-center justify-end gap-0.5 sm:col-span-1'>
                <Button
                  aria-label={`上移第 ${index + 1} 行`}
                  className='text-muted-foreground h-7 w-6'
                  disabled={disabled || index === 0}
                  onClick={() => move(index, -1)}
                  size='icon'
                  type='button'
                  variant='ghost'
                >
                  <ArrowUp className='size-3' />
                </Button>
                <Button
                  aria-label={`下移第 ${index + 1} 行`}
                  className='text-muted-foreground h-7 w-6'
                  disabled={disabled || index === rules.length - 1}
                  onClick={() => move(index, 1)}
                  size='icon'
                  type='button'
                  variant='ghost'
                >
                  <ArrowDown className='size-3' />
                </Button>
                <Button
                  aria-label={`删除第 ${index + 1} 行`}
                  className='text-muted-foreground h-7 w-6'
                  disabled={disabled}
                  onClick={() => remove(index)}
                  size='icon'
                  type='button'
                  variant='ghost'
                >
                  <X className='size-3' />
                </Button>
              </div>

              {(rowMessages.length > 0 || expired || isDead) && (
                <div className='col-span-2 flex flex-wrap gap-x-2 gap-y-0.5 text-[11px] leading-tight sm:col-span-6 sm:col-start-2'>
                  {rowMessages.map((message) => (
                    <span
                      key={message}
                      className={cn(
                        diagnosis.errors.groups === message ||
                          diagnosis.errors.models === message ||
                          diagnosis.errors.rate === message
                          ? 'text-destructive'
                          : 'text-amber-600 dark:text-amber-400'
                      )}
                    >
                      {message}
                    </span>
                  ))}
                  {expired && (
                    <span className='text-muted-foreground'>已过期，不生效</span>
                  )}
                  {isDead && (
                    <span className='text-amber-600 dark:text-amber-400'>
                      被前面的行完全盖住，永不命中
                    </span>
                  )}
                </div>
              )}
            </div>
          )
        })}
      </div>

      <div className='flex flex-wrap items-center gap-2'>
        <Button
          className='h-8 gap-1 px-2.5 text-[12px]'
          disabled={disabled || rules.length >= MAX_DISCOUNT_RULES}
          onClick={add}
          size='sm'
          type='button'
          variant='outline'
        >
          <Plus className='size-3' />
          添加一行
        </Button>
        {blockingCount > 0 && (
          <span className='text-destructive text-[11.5px]'>
            {blockingCount} 行填写有误，保存会被拒绝
          </span>
        )}
        {rules.length === 0 && (
          <span className='text-muted-foreground text-[11.5px]'>
            没有规则时按分组倍率原价计费
          </span>
        )}
      </div>

      <EffectivePricePanel
        groupRatios={groupRatios}
        groups={groups}
        models={models}
        nowSeconds={nowSeconds}
        rules={rules}
        usableGroups={usableGroups}
      />
    </div>
  )
}
