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
import {
  forwardRef,
  useEffect,
  useImperativeHandle,
  useRef,
  useState,
} from 'react'

import { usePricingData } from '@/features/pricing/hooks/use-pricing-data'

import { getUserDiscounts, updateUserDiscounts } from '../api'
import type { DiscountRule } from '../types'
import { DiscountEditor } from './discount-editor'

export type UserDiscountSectionRef = {
  /**
   * 把本次编辑提交上去。
   *
   * 返回 null 表示没有改动（不发请求）；返回响应表示发过请求，调用方据此
   * 决定是否关闭抽屉——折扣没存上却关掉抽屉，运营会以为已经生效。
   */
  flush: (userId: number) => Promise<{ success: boolean; message?: string } | null>
}

export type UserDiscountSectionProps = {
  /** 被编辑的用户；创建模式下为 undefined */
  userId?: number
  /** 全站分组，来自编辑抽屉已有的 /api/group/ 查询 */
  groups: string[]
  /** 抽屉是否处于「编辑已有用户」状态 */
  enabled: boolean
}

/**
 * 用户编辑抽屉里的折扣区块。
 *
 * 折扣存在 users.setting 的独立 key 里，不参与用户主表单的 zod schema，
 * 但保存是同一个动作：抽屉提交时通过 ref.flush 一并写入。
 * 逻辑集中在这里，上游的 users-mutate-drawer.tsx 只保留挂载点。
 */
export const UserDiscountSection = forwardRef<
  UserDiscountSectionRef,
  UserDiscountSectionProps
>(function UserDiscountSection({ userId, groups, enabled }, ref) {
  const [rules, setRules] = useState<DiscountRule[]>([])
  const [usableGroups, setUsableGroups] = useState<string[]>([])
  const [loading, setLoading] = useState(false)
  const loaded = useRef<DiscountRule[]>([])

  const { models, groupRatio, isLoading } = usePricingData(enabled)
  const modelNames = models.map((model) => model.model_name)

  useEffect(() => {
    if (!enabled || !userId) {
      loaded.current = []
      setRules([])
      setUsableGroups([])
      return
    }

    let cancelled = false
    setLoading(true)
    getUserDiscounts(userId)
      .then((result) => {
        if (cancelled) return
        const next = result.success ? (result.data?.rules ?? []) : []
        loaded.current = next
        setRules(next)
        setUsableGroups(result.data?.usable_groups ?? [])
      })
      .catch(() => {
        // 读取失败按「没有折扣」处理：写回时也只会写本次新增的规则，
        // 不会把服务端已有规则误删（flush 只在有改动时才发请求）。
        if (!cancelled) {
          loaded.current = []
          setRules([])
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })

    return () => {
      cancelled = true
    }
  }, [enabled, userId])

  useImperativeHandle(ref, () => ({
    flush: async (targetId: number) => {
      const changed =
        JSON.stringify(rules) !== JSON.stringify(loaded.current)
      if (!changed) return null

      const response = await updateUserDiscounts(targetId, rules)
      if (response.success) loaded.current = rules
      return { success: response.success, message: response.message }
    },
  }))

  if (!enabled) return null

  if (loading) {
    return <div className='text-muted-foreground text-xs'>加载中…</div>
  }

  return (
    <DiscountEditor
      rules={rules}
      onChange={setRules}
      groups={groups}
      usableGroups={usableGroups}
      groupRatios={groupRatio}
      models={modelNames}
      modelsLoading={enabled && isLoading}
    />
  )
})
