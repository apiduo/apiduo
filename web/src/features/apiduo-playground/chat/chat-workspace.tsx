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

import { PlaygroundInput } from '@/features/playground/components/input/playground-input'
import { usePlaygroundConversation } from '@/features/playground/hooks/use-playground-conversation'
import type {
  Message,
  ParameterEnabled,
  PlaygroundConfig,
} from '@/features/playground/types'

import {
  estimateTokens,
  type BillingLedger,
} from '../hooks/use-billing-ledger'
import { ChatThread } from './chat-thread'
import { useChatHandler, type RequestOutcome } from './use-chat-handler'

interface ChatWorkspaceProps {
  config: PlaygroundConfig
  parameterEnabled: ParameterEnabled
  messages: Message[]
  isLoadingMessages: boolean
  models: Array<{ label: string; value: string }>
  groups: Array<{ label: string; value: string; ratio: number | string }>
  isLoadingModels: boolean
  updateMessages: (updater: (prev: Message[]) => Message[]) => void
  updateConfig: <K extends keyof PlaygroundConfig>(
    key: K,
    value: PlaygroundConfig[K]
  ) => void
  updateParameterEnabled: (key: keyof ParameterEnabled, value: boolean) => void
  clearMessages: () => void
  ledger: BillingLedger
  onOpenRequest: (messageKey: string) => void
  onCopyCurl: (messageKey: string) => void
  expandedBillingKey: string | null
  onToggleBilling: (messageKey: string) => void
}

export function ChatWorkspace({
  config,
  parameterEnabled,
  messages,
  isLoadingMessages,
  models,
  groups,
  isLoadingModels,
  updateMessages,
  updateConfig,
  updateParameterEnabled,
  clearMessages,
  ledger,
  onOpenRequest,
  onCopyCurl,
  expandedBillingKey,
  onToggleBilling,
}: ChatWorkspaceProps) {
  const pendingOutcomeRef = useRef<RequestOutcome | null>(null)
  // 停止时消息内容可能没变（引用相同），messages 不会触发 effect；
  // 用递增计数强制跑一次结算，否则会偶发「停止后没有金额」。
  const [settleTick, setSettleTick] = useState(0)

  const handleFinish = useCallback((outcome: RequestOutcome) => {
    pendingOutcomeRef.current = outcome
    setSettleTick((tick) => tick + 1)
  }, [])

  const { sendChat, stopGeneration, isGenerating: isRequesting } = useChatHandler({
    config,
    parameterEnabled,
    onMessageUpdate: updateMessages,
    onFinish: handleFinish,
  })

  const {
    editingMessageKey,
    handleSendMessage: send,
    handleRegenerateMessage,
    handleEditMessage,
    handleEditOpenChange,
    applyEdit,
    handleDeleteMessage,
  } = usePlaygroundConversation({
    messages,
    updateMessages,
    sendChat,
  })

  // 请求结束到消息落库之间隔一次 render，用 effect 取最后一条的 key 再结算
  useEffect(() => {
    const outcome = pendingOutcomeRef.current
    // eslint-disable-next-line no-console
    if (!outcome) return
    const last = messages.at(-1)
    if (!last || last.from !== 'assistant') return
    const isFinal = last.status === 'complete' || last.status === 'error'
    // 正常完成要等消息落成终态；但停止/失败时状态可能来不及收尾，
    // 这时直接按已有内容估算结算，否则会偶发「点了停止却没有金额」。
    if (!isFinal && !outcome.stopped && !outcome.failed) return

    pendingOutcomeRef.current = null

    const lastUser = [...messages].reverse().find((m) => m.from === 'user')
    const promptText = lastUser?.versions[0]?.content ?? ''

    ledger.settle(last.key, {
      usage: outcome.usage,
      fallbackPrompt: estimateTokens(promptText),
      requestId: outcome.requestId,
      stopped: outcome.stopped,
      durationMs: Math.max(0, outcome.completedAt - outcome.startedAt),
      firstTokenMs:
        outcome.firstTokenAt != null
          ? outcome.firstTokenAt - outcome.startedAt
          : null,
      context: { model: config.model, group: config.group },
    })
  }, [messages, settleTick, ledger, config.model, config.group])

  const handleClear = () => {
    handleEditOpenChange(false)
    clearMessages()
    ledger.reset()
  }

  return (
    <div className='relative flex size-full min-h-0 flex-col overflow-hidden'>
      <div className='flex min-h-0 flex-1 flex-col overflow-hidden'>
        <ChatThread
          billingByKey={ledger.billingByKey}
          editingKey={editingMessageKey}
          expandedBillingKey={expandedBillingKey}
          isGenerating={isRequesting}
          isLoadingMessages={isLoadingMessages}
          messages={messages}
          modelName={config.model}
          onCancelEdit={handleEditOpenChange}
          onCopyCurl={onCopyCurl}
          onDeleteMessage={handleDeleteMessage}
          onEditMessage={handleEditMessage}
          onOpenRequest={onOpenRequest}
          onRegenerateMessage={handleRegenerateMessage}
          onSaveEdit={(newContent) => applyEdit(newContent, false)}
          onSaveEditAndSubmit={(newContent) => applyEdit(newContent, true)}
          onSelectPrompt={send}
          onToggleBilling={onToggleBilling}
        />
      </div>

      <div className='mx-auto w-full max-w-4xl'>
        <PlaygroundInput
          config={config}
          disabled={isRequesting}
          groupValue={config.group}
          groups={groups}
          hasMessages={messages.length > 0}
          isGenerating={isRequesting}
          isModelLoading={isLoadingModels}
          models={models}
          modelValue={config.model}
          onClearMessages={handleClear}
          onConfigChange={updateConfig}
          onGroupChange={(value) => updateConfig('group', value)}
          onModelChange={(value) => updateConfig('model', value)}
          onParameterEnabledChange={updateParameterEnabled}
          onStop={stopGeneration}
          onSubmit={send}
          parameterEnabled={parameterEnabled}
        />
      </div>
    </div>
  )
}
