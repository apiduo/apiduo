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

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { apiduoBlockMark } from '@/features/apiduo-marks/marks'
import { cn } from '@/lib/utils'

import {
  collectProbeModels,
  dateValueToUntil,
  diagnoseRule,
  findDeadRows,
  hasBlockingError,
  isExpired,
  OTHER_MODEL,
  rateToZhe,
  resolveDiscount,
  splitList,
  untilToDateValue,
  zheToRate,
} from '../lib/match'
import { MAX_DISCOUNT_RULES, type DiscountRule } from '../types'

const GROUP_LIST_ID = 'apiduo-discount-groups'
const MODEL_LIST_ID = 'apiduo-discount-models'

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
  disabled?: boolean
}

function newRule(defaultGroup: string): DiscountRule {
  return { groups: defaultGroup || '*', models: '*', rate: 0.8, until: 0 }
}

function modelLabel(model: string): string {
  return model === OTHER_MODEL ? '其他模型' : model
}

export function DiscountEditor({
  rules,
  onChange,
  groups,
  usableGroups,
  groupRatios,
  models,
  disabled = false,
}: DiscountEditorProps) {
  // 「折」的输入草稿：受控 input 直接回写数字会吃掉用户正在输入的 "7."。
  const [rateDrafts, setRateDrafts] = useState<Record<number, string>>({})
  const [trialGroup, setTrialGroup] = useState('')
  const [trialModel, setTrialModel] = useState('')

  const nowSeconds = Math.floor(Date.now() / 1000)
  const probes = useMemo(
    () => collectProbeModels(rules, models),
    [rules, models]
  )

  const deadRows = useMemo(
    () => findDeadRows(rules, usableGroups, probes, nowSeconds),
    // probes 由 rules 派生，只需跟 rules/usableGroups 走
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [rules, usableGroups, models]
  )

  const diagnoses = useMemo(
    () =>
      rules.map((rule) =>
        diagnoseRule(rule, {
          knownGroups: groups,
          usableGroups,
          messages: {
            groupsRequired: '填分组，或 * 表示全部',
            modelsRequired: '填模型，或 * 表示全部',
            rateRange: '折扣要在 1–10 折之间',
            groupUnknown: (group) => `分组「${group}」不存在`,
            groupUnusable: (group) => `该用户用不到「${group}」，这条永不命中`,
          },
        })
      ),
    [rules, groups, usableGroups]
  )

  const blockingCount = diagnoses.filter(hasBlockingError).length

  const previewGroups = useMemo(() => {
    const base = usableGroups.length > 0 ? usableGroups : groups
    const named = rules.flatMap((rule) =>
      splitList(rule.groups).filter((group) => group !== '*')
    )
    const merged = [...new Set([...named, ...base])]
    return merged.slice(0, 4)
  }, [rules, usableGroups, groups])

  const fallbackTrialGroup = previewGroups[0] ?? ''
  const activeTrialGroup = trialGroup || fallbackTrialGroup
  const activeTrialModel = trialModel || probes[0] || ''
  const trialHit = activeTrialGroup
    ? resolveDiscount(rules, activeTrialGroup, activeTrialModel, nowSeconds)
    : null
  const trialBase = groupRatios[activeTrialGroup] ?? 1

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
      className='space-y-3'
    >
      <datalist id={GROUP_LIST_ID}>
        {groups.map((group) => (
          <option key={group} value={group} />
        ))}
        <option value='*' />
      </datalist>
      <datalist id={MODEL_LIST_ID}>
        {models.slice(0, 200).map((model) => (
          <option key={model} value={model} />
        ))}
        <option value='*' />
      </datalist>

      <div className='flex items-start justify-between gap-3'>
        <div className='min-w-0'>
          <div className='text-sm font-medium'>模型折扣</div>
          <p className='text-muted-foreground mt-0.5 text-xs leading-relaxed'>
            从上往下第一条命中即停，不相乘。分组和模型都可写多个（逗号分隔），
            <code className='mx-0.5 font-mono'>*</code>
            表示全部，到期留空表示永久。
          </p>
        </div>
        <span className='text-muted-foreground shrink-0 font-mono text-[11px] tabular-nums'>
          {rules.length}/{MAX_DISCOUNT_RULES}
        </span>
      </div>

      <div className='text-muted-foreground text-[11px]'>
        分组 {groups.length} 个 · 该用户可用 {usableGroups.length} 个
        {models.length > 0 && ` · 模型候选 ${models.length} 个`}
        {models.length === 0 && ' · 暂无模型（未配置渠道）'}
      </div>

      <div className='hidden grid-cols-[18px_84px_minmax(0,1fr)_52px_100px_52px] items-center gap-1.5 border-b pb-1 text-[11px] text-muted-foreground sm:grid'>
        <span />
        <span>分组</span>
        <span>模型</span>
        <span>折扣</span>
        <span>到期</span>
        <span />
      </div>

      <div className='space-y-2 sm:space-y-0'>
        {rules.map((rule, index) => {
          const diagnosis = diagnoses[index]
          const expired = isExpired(rule, nowSeconds)
          const isDead = deadRows[index] && !hasBlockingError(diagnosis)
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
                'sm:grid-cols-[18px_84px_minmax(0,1fr)_52px_100px_52px] sm:items-center sm:gap-1.5 sm:rounded-none sm:border-0 sm:border-b sm:border-border/60 sm:p-0 sm:py-1.5',
                (expired || isDead) && 'opacity-50'
              )}
            >
              <span className='text-muted-foreground hidden text-right font-mono text-[10px] tabular-nums sm:block'>
                {index + 1}
              </span>

              <div className='col-span-1 min-w-0'>
                <span className='text-muted-foreground mb-0.5 block text-[10px] sm:hidden'>
                  分组
                </span>
                <Input
                  aria-label={`第 ${index + 1} 行分组`}
                  className='h-7 px-1.5 font-mono text-[11px]'
                  disabled={disabled}
                  list={GROUP_LIST_ID}
                  value={rule.groups}
                  onChange={(event) =>
                    update(index, { groups: event.target.value })
                  }
                />
              </div>

              <div className='col-span-1 min-w-0'>
                <span className='text-muted-foreground mb-0.5 block text-[10px] sm:hidden'>
                  模型
                </span>
                <Input
                  aria-label={`第 ${index + 1} 行模型`}
                  className='h-7 px-1.5 font-mono text-[11px]'
                  disabled={disabled}
                  list={MODEL_LIST_ID}
                  value={rule.models}
                  onChange={(event) =>
                    update(index, { models: event.target.value })
                  }
                />
              </div>

              <div className='col-span-1 min-w-0'>
                <span className='text-muted-foreground mb-0.5 block text-[10px] sm:hidden'>
                  折扣
                </span>
                <div className='relative'>
                  <Input
                    aria-label={`第 ${index + 1} 行折扣，单位折`}
                    className={cn(
                      'h-7 px-1.5 pr-4 text-[11px] tabular-nums',
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
                <span className='text-muted-foreground mb-0.5 block text-[10px] sm:hidden'>
                  到期
                </span>
                <div className='group relative'>
                  <Input
                    aria-label={`第 ${index + 1} 行到期日`}
                    className={cn(
                      'h-7 px-1 text-[11px] [color-scheme:light] dark:[color-scheme:dark]',
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
                  className='text-muted-foreground h-6 w-5'
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
                  className='text-muted-foreground h-6 w-5'
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
                  className='text-muted-foreground h-6 w-5'
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
                <div className='col-span-2 flex flex-wrap gap-x-2 gap-y-0.5 text-[10.5px] leading-tight sm:col-span-6 sm:col-start-2'>
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
          className='h-7 gap-1 px-2 text-[11px]'
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
          <span className='text-destructive text-[11px]'>
            {blockingCount} 行填写有误，保存会被拒绝
          </span>
        )}
        {rules.length === 0 && (
          <span className='text-muted-foreground text-[11px]'>
            没有规则时按分组倍率原价计费
          </span>
        )}
      </div>

      {previewGroups.length > 0 && (
        <div className='bg-muted/40 space-y-1.5 rounded-lg p-2.5'>
          <div className='text-muted-foreground text-[11px]'>
            实际生效（按该用户可用分组，自动算，防止配错）
          </div>
          {previewGroups.map((group) => {
            const base = groupRatios[group] ?? 1
            return (
              <div key={group} className='flex flex-wrap items-center gap-1.5'>
                <span className='w-16 shrink-0 truncate font-mono text-[11px]'>
                  {group}
                </span>
                {probes.map((model) => {
                  const hit = resolveDiscount(rules, group, model, nowSeconds)
                  return (
                    <span
                      key={model}
                      className='bg-background inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-[10.5px]'
                    >
                      <span className='text-muted-foreground max-w-24 truncate font-mono'>
                        {modelLabel(model)}
                      </span>
                      <b
                        className={cn(
                          'font-mono tabular-nums',
                          hit ? 'text-primary' : 'text-muted-foreground'
                        )}
                      >
                        {hit ? hit.rate.toFixed(2) : base.toFixed(2)}
                      </b>
                      {hit ? (
                        <span className='text-muted-foreground'>
                          第 {hit.index + 1} 行
                        </span>
                      ) : (
                        <span className='text-muted-foreground'>组价</span>
                      )}
                    </span>
                  )
                })}
              </div>
            )
          })}
          <div className='border-t pt-1.5'>
            <div className='text-muted-foreground mb-1 text-[11px]'>
              试算任意组合
            </div>
            <div className='flex flex-wrap items-center gap-1.5'>
              <Input
                aria-label='试算分组'
                className='h-6 w-24 px-1.5 font-mono text-[11px]'
                list={GROUP_LIST_ID}
                placeholder={fallbackTrialGroup || '分组'}
                value={trialGroup}
                onChange={(event) => setTrialGroup(event.target.value)}
              />
              <Input
                aria-label='试算模型'
                className='h-6 min-w-32 flex-1 px-1.5 font-mono text-[11px]'
                list={MODEL_LIST_ID}
                placeholder={
                  activeTrialModel === OTHER_MODEL ? '其他模型' : activeTrialModel
                }
                value={trialModel}
                onChange={(event) => setTrialModel(event.target.value)}
              />
              <span className='text-[11px]'>
                →{' '}
                <b
                  className={cn(
                    'font-mono tabular-nums',
                    trialHit ? 'text-primary' : 'text-muted-foreground'
                  )}
                >
                  {trialHit ? trialHit.rate.toFixed(2) : trialBase.toFixed(2)}
                </b>{' '}
                <span className='text-muted-foreground'>
                  {trialHit ? `第 ${trialHit.index + 1} 行` : '组价'}
                </span>
              </span>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
