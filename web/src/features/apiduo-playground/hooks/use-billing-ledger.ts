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
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import { waitForRequestLog } from '../billing/api'
import { findModel, getCachedPricing, loadPricing } from '../billing/pricing'
import { computeChatQuota, computeImageQuota } from '../billing/quota'
import type {
  ChatUsageTokens,
  PricingSnapshot,
  QuotaBreakdown,
} from '../billing/types'

export type BillingStatus = 'pending' | 'settled' | 'estimated'

export interface MessageBilling {
  status: BillingStatus
  quota: number
  promptTokens: number
  completionTokens: number
  cacheTokens: number
  breakdown: QuotaBreakdown | null
  requestId: string
  channelId: number | null
  channelName: string
  firstTokenMs: number | null
  durationMs: number
  /** usage = 上游真实上报；estimate = 按字符数折算，必须在 UI 上标出来 */
  source: 'usage' | 'estimate'
  stopped: boolean
}

export interface BillingClientContext {
  model: string
  group: string
}

/**
 * 粗略折算：中文按字、英文按 4 字符 ≈ 1 token 取中间值。
 * 只在上游没回 usage（如用户中途停止）时兜底，且一律标为估算。
 */
export function estimateTokens(text: string): number {
  if (!text) return 0
  return Math.max(1, Math.ceil(text.length / 2.5))
}

export function useBillingLedger() {
  const [pricing, setPricing] = useState<PricingSnapshot | null>(
    getCachedPricing
  )
  const [billingByKey, setBillingByKey] = useState<
    Record<string, MessageBilling>
  >({})
  const cancelledRef = useRef(false)

  useEffect(() => {
    cancelledRef.current = false
    void loadPricing()
      .then((snapshot) => {
        if (!cancelledRef.current) setPricing(snapshot)
      })
      .catch(() => {
        // 定价拉不到时对账单会退化成「倍率未知」，不影响对话
      })
    return () => {
      cancelledRef.current = true
    }
  }, [])

  const refreshPricing = useCallback(async () => {
    const snapshot = await loadPricing(true)
    setPricing(snapshot)
    return snapshot
  }, [])

  const quote = useCallback(
    (usage: ChatUsageTokens, context: BillingClientContext) => {
      const snapshot = pricing
      const model = findModel(snapshot, context.model)
      return computeChatQuota(
        usage,
        model,
        snapshot,
        context.group,
        context.model
      )
    },
    [pricing]
  )

  /**
   * 结算一条消息：先用 usage 落一个「待对账」的估算值，再拿 request id 反查日志。
   * 查到了用日志的 quota 覆盖（已对账），查不到就保留估算并标注。
   */
  const settle = useCallback(
    (messageKey: string, input: {
      usage: ChatUsageTokens | null
      fallbackPrompt: number
      requestId: string
      stopped: boolean
      durationMs: number
      firstTokenMs: number | null
      context: BillingClientContext
      /** image 按张结算，chat 按 token；两套口径不共用同一个公式 */
      kind?: 'chat' | 'image'
      imageCount?: number
    }) => {
      const tokens: ChatUsageTokens = input.usage ?? {
        promptTokens: input.fallbackPrompt,
        completionTokens: 0,
        cacheTokens: 0,
      }
      const computeFor = (tokensForKind: ChatUsageTokens) => {
        const model = findModel(pricing, input.context.model)
        return input.kind === 'image'
          ? computeImageQuota(
              input.imageCount ?? 1,
              model,
              pricing,
              input.context.group,
              input.context.model
            )
          : computeChatQuota(
              tokensForKind,
              model,
              pricing,
              input.context.group,
              input.context.model
            )
      }
      const breakdown = computeFor(tokens)
      const quota = breakdown?.quota ?? 0

      setBillingByKey((prev) => ({
        ...prev,
        [messageKey]: {
          status: input.requestId ? 'pending' : 'estimated',
          quota,
          promptTokens: tokens.promptTokens,
          completionTokens: tokens.completionTokens,
          cacheTokens: tokens.cacheTokens,
          breakdown,
          requestId: input.requestId,
          channelId: null,
          channelName: '',
          firstTokenMs: input.firstTokenMs,
          durationMs: input.durationMs,
          source: input.usage ? 'usage' : 'estimate',
          stopped: input.stopped,
        },
      }))

      if (!input.requestId) return

      void waitForRequestLog(
        input.requestId,
        () => cancelledRef.current
      ).then((record) => {
        if (cancelledRef.current) return
        setBillingByKey((prev) => {
          const existing = prev[messageKey]
          if (!existing) return prev
          if (!record) {
            return {
              ...prev,
              [messageKey]: { ...existing, status: 'estimated' },
            }
          }
          // 日志里的 token 才是结算口径，用它重算可让对账单与日志逐位对齐
          const settledTokens: ChatUsageTokens = {
            promptTokens: record.promptTokens || existing.promptTokens,
            completionTokens:
              record.completionTokens || existing.completionTokens,
            cacheTokens: existing.cacheTokens,
          }
          const settledBreakdown = computeFor(settledTokens)
          return {
            ...prev,
            [messageKey]: {
              ...existing,
              status: 'settled',
              quota: record.quota,
              promptTokens: settledTokens.promptTokens,
              completionTokens: settledTokens.completionTokens,
              breakdown: settledBreakdown,
              channelId: record.channelId || null,
              channelName: record.channelName,
              source: 'usage',
            },
          }
        })
      })
    },
    [pricing]
  )

  const remove = useCallback((messageKey: string) => {
    setBillingByKey((prev) => {
      if (!(messageKey in prev)) return prev
      const next = { ...prev }
      delete next[messageKey]
      return next
    })
  }, [])

  const reset = useCallback(() => {
    setBillingByKey({})
  }, [])

  const totals = useMemo(() => {
    let quota = 0
    let settled = 0
    let count = 0
    for (const item of Object.values(billingByKey)) {
      quota += item.quota
      count += 1
      if (item.status === 'settled') settled += 1
    }
    return { quota, count, settled }
  }, [billingByKey])

  return {
    pricing,
    billingByKey,
    totals,
    settle,
    remove,
    reset,
    refreshPricing,
    quote,
  }
}

export type BillingLedger = ReturnType<typeof useBillingLedger>
