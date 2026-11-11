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
import { toast } from 'sonner'
import { useTranslation } from 'react-i18next'

import { usePlaygroundConversation } from '@/features/playground/hooks/use-playground-conversation'
import {
  createLoadingAssistantMessage,
  createUserMessage,
  type MessageStateUpdater,
} from '@/features/playground/lib'
import type {
  Message,
  ParameterEnabled,
  PlaygroundConfig,
} from '@/features/playground/types'

import {
  estimateTokens,
  type BillingLedger,
} from '../hooks/use-billing-ledger'
import { compressImage } from '../lib/compress-image'
import type { RequestMeta } from '../lib/request-meta'
import type { PlaygroundMode } from '../types'
import { Composer } from '../v2/composer'
import type { RefImage } from '../v2/types'
import { formatCurrencyFromUSD } from '@/lib/currency'

import { findModel, resolveGroupRatio } from '../billing/pricing'
import { ChatThread } from './chat-thread'
import { useChatHandler, type RequestOutcome } from './use-chat-handler'

const CHAT_TEMPERATURES = [0.2, 0.7, 1.0] as const
/* How much history goes on the wire, in tokens. A long thread otherwise runs
   into the model's window with no warning and the request fails outright. */
const CHAT_CONTEXTS = [2048, 8192, 32768] as const
/* 原型给聊天模型 4–8 张；我们统一 4 张，单张压到 768px JPEG 后再发送。 */
const CHAT_REF_LIMIT = 4

interface ChatWorkspaceProps {
  config: PlaygroundConfig
  parameterEnabled: ParameterEnabled
  messages: Message[]
  isLoadingMessages: boolean
  models: Array<{ label: string; value: string }>
  groups: Array<{ label: string; value: string; ratio: number | string }>
  isLoadingModels: boolean
  updateMessages: (updater: MessageStateUpdater) => void
  updateConfig: <K extends keyof PlaygroundConfig>(
    key: K,
    value: PlaygroundConfig[K]
  ) => void
  updateParameterEnabled: (key: keyof ParameterEnabled, value: boolean) => void
  clearMessages: () => void
  mode: PlaygroundMode
  modeCounts: Record<PlaygroundMode, number>
  onModeChange: (mode: PlaygroundMode) => void
  ledger: BillingLedger
  onOpenRequest: (messageKey: string, meta?: RequestMeta) => void
  onCopyCurl: (messageKey: string, meta?: RequestMeta) => void
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
  updateMessages,
  updateConfig,
  updateParameterEnabled,
  clearMessages,
  mode,
  modeCounts,
  onModeChange,
  ledger,
  onOpenRequest,
  onCopyCurl,
  expandedBillingKey,
  onToggleBilling,
}: ChatWorkspaceProps) {
  const { t } = useTranslation()
  const [prompt, setPrompt] = useState('')

  /* Same arithmetic the price strip uses: model ratio 1 = $2 per million
     input tokens, output = input × completionRatio, both after the group
     discount. Takes a model id so the picker can quote every row, not just
     the selected one. */
  const priceOf = useCallback(
    (target: string) => {
      const info = findModel(ledger.pricing, target)
      if (!info) return ''
      const { effective } = resolveGroupRatio(
        ledger.pricing,
        config.group,
        target
      )
      const unit = info.quotaType === 1 ? info.modelPrice : info.modelRatio * 2
      const input = unit * effective
      const output = input * (info.completionRatio || 1)
      const fmt = (usd: number) =>
        formatCurrencyFromUSD(usd, {
          digitsLarge: 2,
          digitsSmall: 4,
          abbreviate: false,
        })
      return `${t('In')} ${fmt(input)} · ${t('Out')} ${fmt(output)}`
    },
    [config.group, ledger.pricing, t]
  )
  const priceLabel = useMemo(
    () => priceOf(config.model),
    [priceOf, config.model]
  )
  const isNew = useCallback(
    (target: string) =>
      (findModel(ledger.pricing, target)?.tags ?? []).includes('new'),
    [ledger.pricing]
  )
  /* 运营在模型管理里写的一句话，列表第二行就是它；没写才回落到能力事实。 */
  const descOf = useCallback(
    (target: string) => findModel(ledger.pricing, target)?.description || null,
    [ledger.pricing]
  )
  const pendingOutcomeRef = useRef<RequestOutcome | null>(null)
  // 停止时消息内容可能没变（引用相同），messages 不会触发 effect；
  // 用递增计数强制跑一次结算，否则会偶发「停止后没有金额」。
  const [settleTick, setSettleTick] = useState(0)

  const handleFinish = useCallback((outcome: RequestOutcome) => {
    pendingOutcomeRef.current = outcome
    setSettleTick((tick) => tick + 1)
  }, [])

  const [contextTokens, setContextTokens] = useState<number>(CHAT_CONTEXTS[1])
  const [refs, setRefs] = useState<RefImage[]>([])
  /* Images live beside the messages, keyed by message key: the persisted
     message schema has no slot for base64, and a few pictures would blow the
     localStorage quota. The map is what both the request and the bubble read. */
  const [imagesByKey, setImagesByKey] = useState<
    Record<string, Array<{ id: string; url: string }>>
  >({})
  const imagesByKeyRef = useRef(imagesByKey)
  imagesByKeyRef.current = imagesByKey
  const liveRefs = useRef(refs)
  liveRefs.current = refs

  const addRefFiles = useCallback((files: File[]) => {
    const images = files.filter((file) => file.type.startsWith('image/'))
    if (images.length === 0) return
    setRefs((prev) => {
      const room = Math.max(0, CHAT_REF_LIMIT - prev.length)
      return [
        ...prev,
        ...images.slice(0, room).map((file) => ({
          id: `chat-ref-${Date.now()}-${Math.random().toString(36).slice(2, 6)}`,
          file,
          url: URL.createObjectURL(file),
        })),
      ]
    })
  }, [])

  const removeRef = useCallback((id: string) => {
    const target = liveRefs.current.find((item) => item.id === id)
    if (target) URL.revokeObjectURL(target.url)
    setRefs((prev) => prev.filter((item) => item.id !== id))
  }, [])

  useEffect(
    () => () => {
      for (const item of liveRefs.current) URL.revokeObjectURL(item.url)
    },
    []
  )


  const { sendChat, stopGeneration, isGenerating: isRequesting } = useChatHandler({
    config,
    parameterEnabled,
    contextTokens,
    onMessageUpdate: updateMessages,
    onFinish: handleFinish,
  })

  /* Every send path — send, regenerate, edit-and-resend — goes through this
     wrapper, so an image attached three turns ago still travels with the
     history the model is asked to read. */
  const sendChatWithImages = useCallback(
    (next: Message[]) => sendChat(next, imagesByKeyRef.current),
    [sendChat]
  )

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
    sendChat: sendChatWithImages,
  })

  /* A turn with pictures needs its own message pair: the shared helper only
     appends text, and the images are keyed by the user message's id. */
  const sendWithAttachments = useCallback(
    async (text: string, files: File[]) => {
      try {
        const urls = await Promise.all(files.map((file) => compressImage(file)))
        const startedAt = Date.now()
        const userMessage = createUserMessage(text, startedAt)
        const next = [
          ...messages,
          userMessage,
          createLoadingAssistantMessage(startedAt),
        ]
        const map = {
          ...imagesByKeyRef.current,
          [userMessage.key]: urls.map((url, index) => ({
            id: `${userMessage.key}-img-${index}`,
            url,
          })),
        }
        imagesByKeyRef.current = map
        setImagesByKey(map)
        updateMessages(next)
        sendChat(next, map)
      } catch (error) {
        toast.error(
          error instanceof Error ? error.message : t('Attachment failed')
        )
      }
    },
    [messages, sendChat, t, updateMessages]
  )

  // 请求结束到消息落库之间隔一次 render，用 effect 取最后一条的 key 再结算
  useEffect(() => {
    const outcome = pendingOutcomeRef.current
    if (!outcome) return
    const last = messages.at(-1)
    if (!last || last.from !== 'assistant') return
    const isFinal = last.status === 'complete' || last.status === 'error'
    // 正常完成要等消息落成终态；但停止/失败时状态可能来不及收尾，
    // 这时直接按已有内容估算结算，否则会偶发「点了停止却没有金额」。
    if (!isFinal && !outcome.stopped && !outcome.failed) return

    pendingOutcomeRef.current = null

    // 失败且上游没回 usage：这一单没有任何可计费的量。之前这里会掉进估算分支，
    // 给一条根本没花过钱的失败消息挂上金额，还把它累加进会话总计 —— 用户会以为
    // 出错也被扣了钱。部分生成后才断开的请求会带 usage，那种仍然按实际用量结算。
    if (outcome.failed && !outcome.usage) return

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
    <div className='apg relative flex size-full min-h-0 flex-col overflow-hidden'>
      <div className='flex min-h-0 flex-1 flex-col overflow-hidden'>
        <ChatThread
          billingByKey={ledger.billingByKey}
          imagesByKey={imagesByKey}
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

      {/* One composer for the page: chat keeps the same shell as image and
          video, so the three modes read as one surface rather than three. */}
      <Composer
        busy={isRequesting}
        cap={1}
        count={1}
        descOf={descOf}
        isNew={isNew}
        group={config.group}
        groups={groups}
        hasResults={messages.length > 0}
        mode={mode}
        modeCounts={modeCounts}
        model={config.model}
        models={models}
        priceApprox={false}
        priceLabel={priceLabel}
        priceUnit={t('M tokens')}
        temperature={config.temperature}
        temperatures={CHAT_TEMPERATURES}
        onTemperatureChange={(value) => {
          updateConfig('temperature', value)
          // The chip is the only temperature control left on this page, so a
          // value the payload drops would be a knob that reads but does not
          // apply. A previously disabled flag re-arms on the first change.
          if (!parameterEnabled.temperature) {
            updateParameterEnabled('temperature', true)
          }
        }}
        context={contextTokens}
        contexts={CHAT_CONTEXTS}
        onContextChange={setContextTokens}
        allowRefs
        onAddRefs={addRefFiles}
        onClear={handleClear}
        onCountChange={() => undefined}
        onGroupChange={(value) => updateConfig('group', value)}
        onModeChange={onModeChange}
        onModelChange={(value) => updateConfig('model', value)}
        onPromptChange={setPrompt}
        onRatioChange={() => undefined}
        onRemoveRef={removeRef}
        onResolutionChange={() => undefined}
        onStop={stopGeneration}
        onSubmit={() => {
          const text = prompt.trim()
          if (!text) return
          setPrompt('')
          if (refs.length > 0) {
            const files = refs.map((item) => item.file)
            for (const item of refs) URL.revokeObjectURL(item.url)
            setRefs([])
            void sendWithAttachments(text, files)
            return
          }
          send(text)
        }}
        placeholder={t('Ask anything…')}
        priceOf={priceOf}
        prompt={prompt}
        ratio='1:1'
        refCap={CHAT_REF_LIMIT}
        refNote=''
        refs={refs}
        resolution='1024'
        running={isRequesting ? 1 : 0}
        totalOf={() => ''}
      />
    </div>
  )
}
