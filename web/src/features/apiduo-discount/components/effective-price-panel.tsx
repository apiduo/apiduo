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
import { Search } from 'lucide-react'
import { motion, useReducedMotion } from 'motion/react'
import { useId, useMemo, useState } from 'react'

import { Input } from '@/components/ui/input'
import { cn } from '@/lib/utils'

import { MODEL_LIST_ID } from '../lib/dom-ids'
import {
  isExpired,
  isValidRate,
  matchesList,
  resolveDiscount,
  summarizeGroup,
  untilToDateValue,
} from '../lib/match'
import type { DiscountRule } from '../types'

export type EffectivePricePanelProps = {
  rules: DiscountRule[]
  /** 全站分组；用户可用分组未知时作为预览范围 */
  groups: string[]
  /** 该用户可用的分组，空数组表示未知 */
  usableGroups: string[]
  /** 分组倍率，未命中规则时的基线 */
  groupRatios: Record<string, number>
  /** 模型候选，用于清点「其余多少个模型」 */
  models: string[]
  nowSeconds: number
}

/**
 * 生效检查：把「该用户在这个分组下每个模型多少钱」按分段展示。
 *
 * 段数只取决于规则数（≤ MAX_DISCOUNT_RULES + 1），与模型总量无关，
 * 因此 1000 个模型也只是「其余 997 个模型按组价」一行。
 */
export function EffectivePricePanel({
  rules,
  groups,
  usableGroups,
  groupRatios,
  models,
  nowSeconds,
}: EffectivePricePanelProps) {
  const reduceMotion = useReducedMotion()
  const pillLayoutId = useId()
  const [pickedGroup, setPickedGroup] = useState('')
  const [trialModel, setTrialModel] = useState('')

  const groupList = usableGroups.length > 0 ? usableGroups : groups
  const activeGroup = groupList.includes(pickedGroup)
    ? pickedGroup
    : (groupList[0] ?? '')
  const baseRate = groupRatios[activeGroup] ?? 1

  const segments = useMemo(
    () => summarizeGroup(rules, activeGroup, models, baseRate, nowSeconds),
    [rules, activeGroup, models, baseRate, nowSeconds]
  )

  /** 有规则覆盖的分组打个点，避免「没点开就以为没配」。 */
  const coveredGroups = useMemo(
    () =>
      new Set(
        groupList.filter((group) =>
          rules.some(
            (rule) =>
              !isExpired(rule, nowSeconds) &&
              isValidRate(rule.rate) &&
              matchesList(rule.groups, group)
          )
        )
      ),
    [groupList, rules, nowSeconds]
  )

  const trial = trialModel.trim()
    ? resolveDiscount(rules, activeGroup, trialModel.trim(), nowSeconds)
    : null
  const hitCount = segments.filter((segment) => segment.kind === 'rule').length

  if (groupList.length === 0) return null

  return (
    <section
      aria-label='生效检查'
      className='border-border/60 bg-muted/30 overflow-hidden rounded-lg border'
    >
      <div className='flex items-center justify-between gap-2 px-2.5 py-1.5'>
        <span className='text-[11px] font-medium'>生效检查</span>
        <span className='text-muted-foreground font-mono text-[10px] tabular-nums'>
          {hitCount > 0 ? `${hitCount} 条命中` : '无命中'}
        </span>
      </div>

      <div
        aria-label='预览分组'
        className='flex gap-1 overflow-x-auto px-2 pb-1.5 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden'
        role='tablist'
      >
        {groupList.map((group) => {
          const active = group === activeGroup
          return (
            <button
              key={group}
              aria-selected={active}
              className={cn(
                'relative flex h-6 shrink-0 items-center gap-1 rounded-full px-2 font-mono text-[11px] transition-colors',
                active
                  ? 'text-foreground'
                  : 'text-muted-foreground hover:text-foreground'
              )}
              onClick={() => setPickedGroup(group)}
              role='tab'
              type='button'
            >
              {active && (
                <motion.span
                  className='bg-background ring-border absolute inset-0 rounded-full shadow-sm ring-1'
                  layoutId={pillLayoutId}
                  transition={
                    reduceMotion
                      ? { duration: 0 }
                      : { type: 'spring', stiffness: 520, damping: 40, mass: 0.7 }
                  }
                />
              )}
              <span className='relative'>{group}</span>
              {coveredGroups.has(group) && (
                <span className='bg-primary relative size-1 rounded-full' />
              )}
            </button>
          )
        })}
      </div>

      <div
        aria-label={activeGroup}
        className='border-border/50 bg-background/70 border-t px-2.5 py-2'
        role='tabpanel'
      >
        {segments.map((segment, index) => {
          const isRule = segment.kind === 'rule'
          let coverage: string
          if (isRule) {
            if (!segment.catchesRest) {
              coverage = `${segment.models.length} 个模型`
            } else {
              // 首段就是 * 时它接管全部模型，说「其余」会误导
              coverage = index === 0 ? '全部模型' : '其余全部'
            }
          } else if (models.length === 0 || segment.restCount === 0) {
            // 候选未知，或候选全被点名：段价仍然适用于未列出的模型
            coverage = '其余模型'
          } else if (segment.restCount === models.length) {
            coverage = `全部 ${segment.restCount} 个模型`
          } else {
            coverage = `其余 ${segment.restCount} 个模型`
          }

          // 规则是绝对折：组倍率不是 1 时，「7 折」相对组价并不是 -30%。
          // 运营最容易在这里配成涨价，所以把真实幅度直接算出来。
          const delta = baseRate > 0 ? 1 - segment.rate / baseRate : 0
          const showsDelta = isRule && Math.abs(delta) >= 0.005

          return (
            <div
              key={isRule ? `rule-${segment.index}` : 'base'}
              className={cn(
                'flex items-start gap-2 py-1.5 first:pt-0 last:pb-0',
                index > 0 && 'border-border/40 border-t',
                !isRule && 'border-dashed'
              )}
            >
              <span
                className={cn(
                  'w-11 shrink-0 font-mono text-[12px] leading-5 font-semibold tabular-nums',
                  isRule ? 'text-primary' : 'text-muted-foreground'
                )}
              >
                ×{segment.rate.toFixed(2)}
              </span>

              <div className='min-w-0 flex-1'>
                <div className='flex items-center gap-1.5 leading-5'>
                  {isRule ? (
                    <>
                      <span className='bg-primary/10 text-primary rounded px-1 font-mono text-[10px]'>
                        第 {segment.index + 1} 行
                      </span>
                      {segment.until > 0 && (
                        <span
                          className='text-muted-foreground font-mono text-[10px] tabular-nums'
                          title={`${untilToDateValue(segment.until)} 到期`}
                        >
                          至{' '}
                          {untilToDateValue(segment.until)
                            .slice(5)
                            .replace('-', '/')}
                        </span>
                      )}
                      {showsDelta && (
                        <span
                          className={cn(
                            'shrink-0 font-mono text-[10px] tabular-nums',
                            delta < 0 ? 'text-destructive' : 'text-muted-foreground'
                          )}
                          title='相对该分组倍率的实际变化'
                        >
                          较组价 {delta < 0 ? '+' : '-'}
                          {Math.abs(delta * 100).toFixed(1)}%
                        </span>
                      )}
                    </>
                  ) : (
                    <span className='bg-muted text-muted-foreground rounded px-1 font-mono text-[10px]'>
                      组价
                    </span>
                  )}
                  <span className='text-muted-foreground ml-auto shrink-0 text-[10px] tabular-nums'>
                    {coverage}
                  </span>
                </div>

                {isRule && segment.models.length > 0 && (
                  <div
                    className='text-muted-foreground mt-0.5 truncate font-mono text-[10px]'
                    title={segment.models.join(', ')}
                  >
                    {segment.models.slice(0, 3).join(' · ')}
                    {segment.models.length > 3 &&
                      ` · 等 ${segment.models.length} 个`}
                  </div>
                )}

                {!isRule && rules.length === 0 && (
                  <div className='text-muted-foreground/80 mt-0.5 text-[10px]'>
                    未配置规则，按分组倍率计费
                  </div>
                )}
              </div>
            </div>
          )
        })}

        <div className='border-border/40 mt-2 flex items-center gap-1.5 border-t pt-2'>
          <Search aria-hidden className='text-muted-foreground/70 size-3 shrink-0' />
          <Input
            aria-label='试算模型'
            className='h-5 flex-1 border-0 bg-transparent px-0 font-mono text-[11px] shadow-none focus-visible:ring-0 dark:bg-transparent'
            list={MODEL_LIST_ID}
            onChange={(event) => setTrialModel(event.target.value)}
            placeholder='试算该分组下的任意模型'
            value={trialModel}
          />
          {trialModel.trim() && (
            <output
              aria-label='试算结果'
              className='flex shrink-0 items-baseline gap-1 font-mono text-[11px] tabular-nums'
            >
              <b
                className={cn(
                  'font-semibold',
                  trial ? 'text-primary' : 'text-muted-foreground'
                )}
              >
                ×{(trial?.rate ?? baseRate).toFixed(2)}
              </b>
              <span className='text-muted-foreground text-[10px]'>
                {trial ? `第 ${trial.index + 1} 行` : '组价'}
              </span>
            </output>
          )}
        </div>
      </div>
    </section>
  )
}
