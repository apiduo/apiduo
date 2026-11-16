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

import { formatMonthDay } from '@/lib/time'
import { cn } from '@/lib/utils'

import type { AnnouncementPhase } from '../../types'

interface AnnouncementTimelineProps {
  phases: AnnouncementPhase[]
}

type PhaseState = 'done' | 'now' | 'todo'

/**
 * 阶段的三态由 `at` 与当前时刻比较得出，不落库。
 * 存状态就要有人定时改写它，定时任务一停状态就错。
 */
function resolveState(index: number, reachedIndex: number): PhaseState {
  if (index > reachedIndex) return 'todo'
  if (index === reachedIndex) return 'now'
  return 'done'
}

/**
 * 发布公告的阶段时间线。
 *
 * 只读展示，没有交互 —— 公告面板里的时间线回答的是「走到哪一步了」，
 * 不是「让我点点看」。节点数量在后端校验里封顶 8 个。
 */
export function AnnouncementTimeline({ phases }: AnnouncementTimelineProps) {
  const { t } = useTranslation()
  if (phases.length === 0) return null

  const now = Date.now()
  // 最后一个已经到达的阶段就是「当前」；一个都没到则为 -1，全部未开始
  const reachedIndex = phases.reduce(
    (last, phase, index) =>
      new Date(phase.at).getTime() <= now ? index : last,
    -1
  )

  return (
    <ol className='m-0 list-none p-0'>
      {phases.map((phase, index) => {
        const state = resolveState(index, reachedIndex)
        const isLast = index === phases.length - 1
        return (
          <li
            key={`${phase.at}-${phase.title}`}
            className='relative grid grid-cols-[44px_16px_1fr]'
          >
            <time
              dateTime={phase.at}
              className='text-muted-foreground pr-2.5 text-right font-mono text-[11px] leading-4 tabular-nums'
            >
              {formatMonthDay(new Date(phase.at))}
            </time>

            <span className='relative flex justify-center'>
              {/* 轴线连着下一个节点：bottom 越过内边距，不留 4px 缺口 */}
              {!isLast && (
                <span
                  aria-hidden
                  className={cn(
                    'absolute top-3.5 -bottom-1 w-px',
                    state === 'todo'
                      ? 'bg-[repeating-linear-gradient(180deg,var(--color-muted-foreground)_0_3px,transparent_3px_7px)] opacity-40'
                      : 'bg-primary/70'
                  )}
                />
              )}
              <span
                aria-hidden
                className={cn(
                  'relative z-10 mt-1 size-2 rounded-full',
                  state === 'done' && 'bg-primary',
                  state === 'now' && 'size-2.5 bg-primary ring-4 ring-primary/15',
                  state === 'todo' &&
                    'border-muted-foreground/40 bg-card border'
                )}
              />
            </span>

            <div className={cn(isLast ? 'pb-0' : 'pb-5')}>
              <p
                className={cn(
                  'flex items-center gap-1.5 text-[13px] leading-5',
                  state === 'todo'
                    ? 'text-muted-foreground font-medium'
                    : 'font-semibold'
                )}
              >
                {phase.title}
                {state === 'now' && (
                  <span
                    className='bg-accent rounded px-1.5 py-px text-[10px] font-semibold'
                    // accent 底太浅，直接用 --primary 只有 2.4:1
                    style={{
                      color:
                        'color-mix(in oklch, var(--primary) 62%, var(--foreground))',
                    }}
                  >
                    {t('Current')}
                  </span>
                )}
              </p>
              {phase.desc && (
                <p className='text-muted-foreground mt-0.5 text-[11.5px] leading-relaxed'>
                  {phase.desc}
                </p>
              )}
            </div>
          </li>
        )
      })}
    </ol>
  )
}
