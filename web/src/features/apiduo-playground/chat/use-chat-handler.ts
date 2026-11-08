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
import { useCallback, useEffect, useRef, useState } from 'react'

import {
  applyChatCompletionResponse,
  applyStreamingChunk,
  buildChatCompletionPayload,
  completeAssistantMessage,
  hasChatCompletionChoice,
  isAssistantMessageFinal,
  parseRequestErrorDetails,
  updateAssistantMessageWithError,
  updateLastAssistantMessage,
} from '@/features/playground/lib'
import type {
  Message,
  ParameterEnabled,
  PlaygroundConfig,
} from '@/features/playground/types'
import { handleServerError } from '@/lib/handle-server-error'

import type { ChatUsageTokens } from '../billing/types'
import { parseUsage } from '../billing/usage'
import { fetchChatCompletion, streamChatCompletion } from './stream-client'

/** 一次请求结束时的结算输入；usage 为空表示只能估算 */
export interface RequestOutcome {
  usage: ChatUsageTokens | null
  requestId: string
  stopped: boolean
  failed: boolean
  startedAt: number
  firstTokenAt: number | null
  completedAt: number
}

interface UseChatHandlerOptions {
  config: PlaygroundConfig
  parameterEnabled: ParameterEnabled
  onMessageUpdate: (updater: (prev: Message[]) => Message[]) => void
  onFinish: (outcome: RequestOutcome) => void
}

const STREAM_UPDATE_FLUSH_MS = 50

type PendingStreamChunks = {
  generation: number
  content: string
  reasoning: string
}

function mergePendingChunk(current: string, next: string): string {
  if (!current || !next.startsWith(current)) return current + next
  return next
}

export function useChatHandler({
  config,
  parameterEnabled,
  onMessageUpdate,
  onFinish,
}: UseChatHandlerOptions) {
  const [isRequesting, setIsRequesting] = useState(false)
  const abortRef = useRef<AbortController | null>(null)
  const generationRef = useRef(0)
  const pendingRef = useRef<PendingStreamChunks>({
    generation: 0,
    content: '',
    reasoning: '',
  })
  const flushTimerRef = useRef<number | null>(null)
  const outcomeRef = useRef<RequestOutcome | null>(null)

  const discardPending = useCallback((generation: number) => {
    if (flushTimerRef.current !== null) {
      window.clearTimeout(flushTimerRef.current)
      flushTimerRef.current = null
    }
    pendingRef.current = { generation, content: '', reasoning: '' }
  }, [])

  const flush = useCallback(
    (generation: number) => {
      if (generation !== generationRef.current) return
      if (flushTimerRef.current !== null) {
        window.clearTimeout(flushTimerRef.current)
        flushTimerRef.current = null
      }

      const pending = pendingRef.current
      if (pending.generation !== generation) return
      if (!pending.content && !pending.reasoning) return

      pendingRef.current = { generation, content: '', reasoning: '' }
      onMessageUpdate((prev) => {
        if (generation !== generationRef.current) return prev
        return updateLastAssistantMessage(prev, (message) => {
          let next = message
          if (pending.reasoning) {
            next = applyStreamingChunk(next, 'reasoning', pending.reasoning)
          }
          if (pending.content) {
            next = applyStreamingChunk(next, 'content', pending.content)
          }
          return next
        })
      })
    },
    [onMessageUpdate]
  )

  const scheduleFlush = useCallback(
    (generation: number) => {
      if (generation !== generationRef.current) return
      if (flushTimerRef.current !== null) return
      flushTimerRef.current = window.setTimeout(() => {
        flush(generation)
      }, STREAM_UPDATE_FLUSH_MS)
    },
    [flush]
  )

  useEffect(
    () => () => {
      generationRef.current += 1
      if (flushTimerRef.current !== null) {
        window.clearTimeout(flushTimerRef.current)
      }
      abortRef.current?.abort()
      abortRef.current = null
    },
    []
  )

  const finish = useCallback(
    (
      generation: number,
      patch: Partial<RequestOutcome>,
      options?: { skipFlush?: boolean }
    ) => {
      if (generation !== generationRef.current) return
      // 调用方可能在写终态之前已经 flush 过；再 flush 一次会把 status 覆盖回
      // streaming，导致「消息已显示完成但结算永远不触发」。
      if (!options?.skipFlush) flush(generation)
      setIsRequesting(false)

      const base = outcomeRef.current
      const outcome: RequestOutcome = {
        usage: patch.usage ?? base?.usage ?? null,
        requestId: patch.requestId ?? base?.requestId ?? '',
        stopped: patch.stopped ?? base?.stopped ?? false,
        failed: patch.failed ?? base?.failed ?? false,
        startedAt: base?.startedAt ?? Date.now(),
        firstTokenAt: base?.firstTokenAt ?? null,
        completedAt: Date.now(),
      }
      outcomeRef.current = null
      onFinish(outcome)
    },
    [flush, onFinish]
  )

  const sendChat = useCallback(
    (messages: Message[]) => {
      const generation = generationRef.current + 1
      generationRef.current = generation
      abortRef.current?.abort()
      discardPending(generation)
      setIsRequesting(true)

      const controller = new AbortController()
      abortRef.current = controller

      const payload = buildChatCompletionPayload(
        messages,
        config,
        parameterEnabled
      )

      const now = Date.now()
      outcomeRef.current = {
        usage: null,
        requestId: '',
        stopped: false,
        failed: false,
        startedAt: now,
        firstTokenAt: null,
        completedAt: now,
      }

      if (config.stream) {
        void streamChatCompletion(
          // 关键：不带这个开关，网关不会在流末尾下发 usage
          { ...payload, stream_options: { include_usage: true } },
          controller.signal,
          {
            onUpdate: (type, chunk) => {
              if (generation !== generationRef.current) return
              const pending = pendingRef.current
              if (pending.generation !== generation) return
              pending[type] = mergePendingChunk(pending[type], chunk)
              const outcome = outcomeRef.current
              if (outcome && outcome.firstTokenAt === null) {
                outcome.firstTokenAt = Date.now()
              }
              scheduleFlush(generation)
            },
            onUsage: (usage) => {
              const outcome = outcomeRef.current
              if (outcome) outcome.usage = usage
            },
            onComplete: ({ requestId }) => {
              if (generation !== generationRef.current) return
              flush(generation)
              onMessageUpdate((prev) => {
                if (generation !== generationRef.current) return prev
                return updateLastAssistantMessage(prev, (message) =>
                  isAssistantMessageFinal(message)
                    ? message
                    : completeAssistantMessage(message)
                )
              })
              finish(generation, { requestId }, { skipFlush: true })
            },
            onError: (message, errorCode) => {
              if (generation !== generationRef.current) return
              flush(generation)
              handleServerError(new Error(message))
              onMessageUpdate((prev) => {
                if (generation !== generationRef.current) return prev
                return updateAssistantMessageWithError(
                  prev,
                  message,
                  errorCode,
                  'Request error occurred'
                )
              })
              finish(generation, { failed: true }, { skipFlush: true })
            },
          }
        )
        return
      }

      void (async () => {
        try {
          const { body, requestId, error } = await fetchChatCompletion(
            payload,
            controller.signal
          )
          if (
            controller.signal.aborted ||
            generation !== generationRef.current
          ) {
            return
          }

          if (error || !body) {
            const message = error?.[0] ?? 'Request error occurred'
            handleServerError(new Error(message))
            onMessageUpdate((prev) =>
              updateAssistantMessageWithError(
                prev,
                message,
                error?.[1],
                'Request error occurred'
              )
            )
            finish(generation, { failed: true })
            return
          }

          const usage = parseUsage(body.usage)
          if (!hasChatCompletionChoice(body)) {
            onMessageUpdate((prev) =>
              updateAssistantMessageWithError(
                prev,
                'Request error occurred',
                undefined,
                'Request error occurred'
              )
            )
            finish(generation, { failed: true })
            return
          }

          onMessageUpdate((prev) =>
            updateLastAssistantMessage(prev, (message) => {
              const next = applyChatCompletionResponse(message, body)
              return next ?? message
            })
          )
          finish(generation, { usage, requestId })
        } catch (error) {
          if (controller.signal.aborted) return
          const { errorCode, errorMessage } = parseRequestErrorDetails(error)
          handleServerError(new Error(errorMessage))
          onMessageUpdate((prev) =>
            updateAssistantMessageWithError(
              prev,
              errorMessage,
              errorCode,
              'Request error occurred'
            )
          )
          finish(generation, { failed: true })
        }
      })()
    },
    [
      config,
      parameterEnabled,
      discardPending,
      scheduleFlush,
      flush,
      onMessageUpdate,
      finish,
    ]
  )

  const stopGeneration = useCallback(() => {
    const generation = generationRef.current
    flush(generation)
    const idle = generation + 1
    generationRef.current = idle
    discardPending(idle)
    abortRef.current?.abort()
    abortRef.current = null
    setIsRequesting(false)

    onMessageUpdate((prev) => {
      if (generationRef.current !== generation) {
        // 只判 isAssistantMessagePending 会漏掉已经进入 streaming 的消息，
        // 它们会一直停在流式态，结算也就永远不触发。凡是非终态都要收尾。
        return updateLastAssistantMessage(prev, (message) =>
          isAssistantMessageFinal(message)
            ? message
            : completeAssistantMessage(message)
        )
      }
      return prev
    })

    const outcome = outcomeRef.current
    if (outcome) {
      outcomeRef.current = null
      onFinish({ ...outcome, stopped: true, completedAt: Date.now() })
    }
  }, [flush, discardPending, onMessageUpdate, onFinish])

  return { sendChat, stopGeneration, isGenerating: isRequesting }
}
