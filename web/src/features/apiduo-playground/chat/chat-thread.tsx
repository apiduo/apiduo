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
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  Conversation,
  ConversationContent,
  ConversationScrollButton,
} from '@/components/ai-elements/conversation'
import { Loader } from '@/components/ai-elements/loader'
import { Message } from '@/components/ai-elements/message'

import { PlaygroundEmptyState } from '@/features/playground/components/chat/playground-empty-state'
import { MessageActions } from '@/features/playground/components/message/message-actions'
import { MessageErrorActions } from '@/features/playground/components/message/message-error-actions'
import { PlaygroundMessageContent } from '@/features/playground/components/message/playground-message-content'
import { PlaygroundMessageEditor } from '@/features/playground/components/message/playground-message-editor'
import {
  getChatMessageRenderState,
  getEditingMessageContent,
  getMessageAlignment,
  getPreviousUserMessage,
  isErrorMessage,
} from '@/features/playground/lib'
import type {
  Message as MessageType,
  PlaygroundMessageLayoutMode,
} from '@/features/playground/types'

import type { MessageBilling } from '../hooks/use-billing-ledger'
import { BillPanel, MoneyChip } from '../components/message-billing'

const MAX_RENDERED_HISTORY_MESSAGES = 24

interface ChatThreadProps {
  messages: MessageType[]
  billingByKey: Record<string, MessageBilling>
  expandedBillingKey: string | null
  onToggleBilling: (key: string) => void
  onOpenRequest: (key: string) => void
  onCopyCurl: (key: string) => void
  modelName: string
  vendor?: string
  onCopyMessage?: (message: MessageType) => void
  onRegenerateMessage?: (message: MessageType) => void
  onEditMessage?: (message: MessageType) => void
  onDeleteMessage?: (message: MessageType) => void
  onSelectPrompt?: (prompt: string) => void
  isGenerating?: boolean
  isLoadingMessages?: boolean
  editingKey?: string | null
  onSaveEdit?: (newContent: string) => void
  onCancelEdit?: (open: boolean) => void
  onSaveEditAndSubmit?: (newContent: string) => void
  messageLayoutMode?: PlaygroundMessageLayoutMode
}

export function ChatThread({
  messages,
  billingByKey,
  expandedBillingKey,
  onToggleBilling,
  onOpenRequest,
  onCopyCurl,
  modelName,
  vendor,
  onCopyMessage,
  onRegenerateMessage,
  onEditMessage,
  onDeleteMessage,
  onSelectPrompt,
  isGenerating = false,
  isLoadingMessages = false,
  editingKey,
  onSaveEdit,
  onCancelEdit,
  onSaveEditAndSubmit,
  messageLayoutMode = 'alternating',
}: ChatThreadProps) {
  const { t } = useTranslation()
  const [editText, setEditText] = useState('')
  const [originalText, setOriginalText] = useState('')
  const [sourceMessageKeys, setSourceMessageKeys] = useState<
    ReadonlySet<string>
  >(() => new Set())

  const visibleMessageOffset = Math.max(
    0,
    messages.length - MAX_RENDERED_HISTORY_MESSAGES
  )
  const visibleMessages = messages.slice(visibleMessageOffset)

  function handleToggleMessageSource(message: MessageType): void {
    setSourceMessageKeys((current) => {
      const next = new Set(current)
      if (next.has(message.key)) next.delete(message.key)
      else next.add(message.key)
      return next
    })
  }

  useEffect(() => {
    if (!editingKey) return
    const content = getEditingMessageContent(messages, editingKey)
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setEditText(content)
    setOriginalText(content)
  }, [editingKey, messages])

  let chatContent = visibleMessages.map((message, visibleIndex) => {
    const messageIndex = visibleMessageOffset + visibleIndex
    const { alwaysShowActions, content, isEditing } = getChatMessageRenderState(
      messages,
      message,
      messageIndex,
      editingKey
    )
    const isError = isErrorMessage(message)
    const previousUserMessage = isError
      ? getPreviousUserMessage(messages, messageIndex)
      : null
    const alignment = getMessageAlignment(message, messageLayoutMode)
    const isSourceVisible = sourceMessageKeys.has(message.key)
    const billing = billingByKey[message.key]
    const billingExpanded = expandedBillingKey === message.key

    return (
      <Message
        className='group flex-row-reverse py-2.5'
        from={message.from}
        key={message.key}
      >
        <div className='w-full min-w-0 flex-1 basis-full'>
          {isEditing ? (
            <PlaygroundMessageEditor
              editText={editText}
              message={message}
              onCancelEdit={onCancelEdit}
              onEditTextChange={setEditText}
              onSaveEdit={onSaveEdit}
              onSaveEditAndSubmit={onSaveEditAndSubmit}
              originalText={originalText}
            />
          ) : (
            <PlaygroundMessageContent
              alignment={alignment}
              actions={
                // items-start 会把子项收缩到内容宽度，对账单要显式拉满
                <div className='flex w-full flex-col'>
                  <div className='flex w-full flex-wrap items-center gap-2'>
                    <MessageActions
                      alwaysVisible={alwaysShowActions}
                      className='mt-1.5'
                      isGenerating={isGenerating}
                      isSourceVisible={isSourceVisible}
                      message={message}
                      onCopy={onCopyMessage}
                      onDelete={onDeleteMessage}
                      onEdit={onEditMessage}
                      onRegenerate={onRegenerateMessage}
                      onToggleSource={handleToggleMessageSource}
                    />
                    {billing ? (
                      <div className='ml-auto'>
                        <MoneyChip
                          billing={billing}
                          expanded={billingExpanded}
                          onToggle={() => onToggleBilling(message.key)}
                        />
                      </div>
                    ) : null}
                  </div>
                  {billing && billingExpanded ? (
                    <BillPanel
                      billing={billing}
                      modelName={modelName}
                      onCopyCurl={() => onCopyCurl(message.key)}
                      onOpenRequest={() => onOpenRequest(message.key)}
                      vendor={vendor}
                    />
                  ) : null}
                </div>
              }
              errorActions={
                isError ? (
                  <MessageErrorActions
                    disabled={isGenerating}
                    onDelete={
                      onDeleteMessage
                        ? () => onDeleteMessage(message)
                        : undefined
                    }
                    onEditPrompt={
                      onEditMessage && previousUserMessage
                        ? () => onEditMessage(previousUserMessage)
                        : undefined
                    }
                    onRetry={
                      onRegenerateMessage
                        ? () => onRegenerateMessage(message)
                        : undefined
                    }
                  />
                ) : undefined
              }
              isSourceVisible={isSourceVisible}
              message={message}
              versionContent={content}
            />
          )}
        </div>
      </Message>
    )
  })

  if (visibleMessages.length === 0 && onSelectPrompt) {
    chatContent = [
      <PlaygroundEmptyState key='empty' onSelectPrompt={onSelectPrompt} />,
    ]
  }

  if (isLoadingMessages) {
    chatContent = [
      <div
        className='text-muted-foreground flex min-h-[min(520px,calc(100svh-18rem))] items-center justify-center gap-2 text-sm'
        key='loading'
      >
        <Loader />
        <span>{t('Loading conversation...')}</span>
      </div>,
    ]
  }

  return (
    <Conversation>
      <ConversationContent className='p-0'>
        <div className='mx-auto w-full max-w-4xl px-4 py-4'>{chatContent}</div>
      </ConversationContent>
      <ConversationScrollButton />
    </Conversation>
  )
}
