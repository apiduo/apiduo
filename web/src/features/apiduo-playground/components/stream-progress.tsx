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
import { AnimatePresence, motion } from 'motion/react'
import { useEffect, useState } from 'react'

import { MESSAGE_STATUS } from '@/features/playground/constants'
import type { MessageStatus } from '@/features/playground/types'
import { cn } from '@/lib/utils'

import { useApiduoSpring } from '../hooks/use-motion'

type Phase = 'indeterminate' | 'growing' | 'finishing' | 'failed' | 'hidden'

interface StreamProgressProps {
  status: MessageStatus | undefined
  /**
   * 已到达的字符数。用它算进度而不是用 token：流式过程中拿不到 usage，
   * 而字符数是唯一在每一帧都真实可知的量。
   */
  chars: number
}

/**
 * 这个刻度不是"总量"——流式响应没有总量。它是一条渐近线：
 *
 *   ratio = 1 - e^(-chars / 620)
 *
 * 300 字到 38 %，1000 字到 80 %，2000 字到 96 %。含义是"随着内容变长，
 * 我越来越接近结束"，在完成的那一帧真的到 100 %。它不会在生成途中冒充
 * 精确百分比，所以没有 aria-valuenow——对读屏用户来说这条是装饰，
 * 真正的状态由消息本身表达。
 *
 * 620 这个尺度是经验值：本项目的回复中位数在 400~900 字之间，取这个数
 * 让中位长度的回复走完大约 2/3 到 4/5 的进度，短回复也不会一上来就满格。
 */
const CHAR_SCALE = 620

export function StreamProgress({ status, chars }: StreamProgressProps) {
  const [phase, setPhase] = useState<Phase>(() => {
    if (status === MESSAGE_STATUS.LOADING) return 'indeterminate'
    if (status === MESSAGE_STATUS.STREAMING) return 'growing'
    return 'hidden'
  })
  const spring = useApiduoSpring('settle')

  useEffect(() => {
    if (status === MESSAGE_STATUS.LOADING) {
      setPhase('indeterminate')
      return
    }
    if (status === MESSAGE_STATUS.STREAMING) {
      setPhase('growing')
      return
    }
    if (status === MESSAGE_STATUS.COMPLETE) {
      // 先填满、亮一下，再收掉。少了这一段，进度条会在最后一帧凭空消失，
      // 看起来像没画完。
      setPhase('finishing')
      const timer = window.setTimeout(() => setPhase('hidden'), 660)
      return () => window.clearTimeout(timer)
    }
    if (status === MESSAGE_STATUS.ERROR) {
      setPhase('failed')
      const timer = window.setTimeout(() => setPhase('hidden'), 560)
      return () => window.clearTimeout(timer)
    }
    setPhase('hidden')
  }, [status])

  const visible = phase !== 'hidden'
  const estimate = Math.max(
    0.04,
    Math.min(0.94, 1 - Math.exp(-Math.max(0, chars) / CHAR_SCALE))
  )
  // 完成时真的走到 100%；失败时停在断点，不假装走完
  const ratio = phase === 'finishing' ? 1 : estimate

  return (
    <AnimatePresence initial={false}>
      {visible ? (
        <motion.div
          animate={{ opacity: 1, height: 3, marginTop: 10 }}
          aria-hidden='true'
          className='relative w-full'
          exit={{ opacity: 0, height: 0, marginTop: 0 }}
          initial={{ opacity: 0, height: 3, marginTop: 10 }}
          key='stream-progress'
          transition={spring}
        >
          <div
            className={cn(
              'apd-progress-track relative h-[3px] w-full overflow-visible rounded-full',
              phase === 'finishing' && 'apd-progress-done',
              phase === 'failed' && 'apd-progress-failed'
            )}
          >
            {phase === 'indeterminate' ? (
              <>
                {/* 两条不同速度的扫描带：一条是主体，一条更淡、稍慢，
                    两束叠在一起才像"正在渲染"，单条更像加载条 */}
                <span className='apd-indeterminate-track' />
                <span className='apd-indeterminate-track apd-indeterminate-track--echo' />
              </>
            ) : (
              <motion.div
                animate={{ width: `${ratio * 100}%` }}
                className={cn(
                  'relative h-full rounded-full',
                  phase === 'failed' ? 'bg-destructive' : 'apd-progress-fill'
                )}
                initial={false}
                transition={{
                  type: 'tween',
                  duration: 0.42,
                  ease: [0.22, 1, 0.36, 1],
                }}
              >
                {/* 前端的光点：进度推进时它先动，条子的填充后到，
                    读起来像有个笔头在写 */}
                <span
                  className={cn(
                    'apd-progress-head',
                    phase === 'failed' && 'apd-progress-head--failed'
                  )}
                />
              </motion.div>
            )}
          </div>
        </motion.div>
      ) : null}
    </AnimatePresence>
  )
}
