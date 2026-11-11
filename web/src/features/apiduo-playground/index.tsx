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
import { motion } from 'motion/react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { usePlaygroundOptions } from '@/features/playground/hooks/use-playground-options'
import { usePlaygroundState } from '@/features/playground/hooks/use-playground-state'
import type { ModelOption } from '@/features/playground/types'
import { cn } from '@/lib/utils'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import { ModeTabs } from './components/mode-tabs'
import { PriceStrip, SpendBadge } from './components/price-strip'
import { RequestDrawer } from './components/request-drawer'
import { ChatWorkspace } from './chat/chat-workspace'
import { ImageStage } from './v2/image-stage'
import { useBillingLedger } from './hooks/use-billing-ledger'
import { useApiduoSpring } from './hooks/use-motion'
import { buildOfficialCurl } from './lib/official-curl'
import './styles/fluid.css'
import './styles/motion.css'
import { VideoWorkspace } from './video/video-workspace'
import type { PlaygroundMode } from './types'

const MODE_STORAGE_KEY = 'apiduo_playground_mode'

const MODE_ENDPOINTS: Record<PlaygroundMode, string[]> = {
  chat: ['openai', 'anthropic', 'gemini', 'openai-response'],
  image: ['image-generation'],
  video: ['openai-video'],
}

function readStoredMode(): PlaygroundMode {
  if (typeof window === 'undefined') return 'chat'
  const stored = window.localStorage.getItem(MODE_STORAGE_KEY)
  return stored === 'image' || stored === 'video' ? stored : 'chat'
}

/**
 * 模型按端点类型分到三个工作区。一个模型都没匹配上时退回全量列表，
 * 宁可选错也不要让用户面对空下拉框——很多自建渠道并不上报 endpoint 类型。
 */
function filterModels(
  models: ModelOption[],
  pricing: ReturnType<typeof useBillingLedger>['pricing'],
  mode: PlaygroundMode
): ModelOption[] {
  if (!pricing) return models
  const wanted = MODE_ENDPOINTS[mode]
  const matched = models.filter((model) => {
    const info = pricing.models[model.value]
    if (!info || info.endpointTypes.length === 0) return mode === 'chat'
    return info.endpointTypes.some((type) => wanted.includes(type))
  })
  return matched.length > 0 ? matched : models
}

export function ApiduoPlayground() {
  const { t } = useTranslation()
  const [mode, setMode] = useState<PlaygroundMode>(readStoredMode)
  const [expandedBillingKey, setExpandedBillingKey] = useState<string | null>(
    null
  )
  const [drawerKey, setDrawerKey] = useState<string | null>(null)

  const {
    config,
    parameterEnabled,
    messages,
    isLoadingMessages,
    models,
    groups,
    updateMessages,
    setModels,
    setGroups,
    updateConfig,
    updateParameterEnabled,
    clearMessages,
  } = usePlaygroundState()

  const ledger = useBillingLedger()

  const { isLoadingModels } = usePlaygroundOptions({
    currentGroup: config.group,
    currentModel: config.model,
    setGroups,
    setModels,
    updateConfig,
  })

  const visibleModels = useMemo(
    () => filterModels(models, ledger.pricing, mode),
    [models, ledger.pricing, mode]
  )

  // 切模式后如果当前模型不在该模式的可选范围内，自动落到第一个可用模型
  useEffect(() => {
    if (visibleModels.length === 0) return
    if (visibleModels.some((model) => model.value === config.model)) return
    updateConfig('model', visibleModels[0].value)
  }, [visibleModels, config.model, updateConfig])

  const workspaceSpring = useApiduoSpring('settle')

  const handleModeChange = useCallback(
    (next: PlaygroundMode) => {
      setMode(next)
      setExpandedBillingKey(null)
      setDrawerKey(null)
      if (typeof window !== 'undefined') {
        window.localStorage.setItem(MODE_STORAGE_KEY, next)
      }
    },
    []
  )

  const handleToggleBilling = useCallback((key: string) => {
    setExpandedBillingKey((current) => (current === key ? null : key))
  }, [])

  const handleOpenRequest = useCallback((key: string) => {
    setDrawerKey(key)
  }, [])

  const handleCopyCurl = useCallback(
    (key: string) => {
      void key
      const origin =
        typeof window === 'undefined'
          ? 'https://apiduo.cn'
          : window.location.origin
      const { curl } = buildOfficialCurl(mode, config.model, origin)
      void navigator.clipboard
        .writeText(curl)
        .then(
          () => toast.success(t('cURL copied')),
          () => toast.error(t('Copy failed'))
        )
        .catch(() => toast.error(t('Copy failed')))
    },
    [mode, config.model, t]
  )

  return (
    <div className='relative flex size-full min-h-0 flex-col overflow-hidden'>
      <header className='border-border/60 flex flex-wrap items-center gap-2 border-b px-4 py-2'>
        <ModeTabs onChange={handleModeChange} value={mode} />

        {/*
          In image mode the composer owns the model: the picker sits where the
          parameters are, and a second dropdown in the header would let the two
          disagree for a frame. Chat and video keep the header pair until they
          get the same treatment.
        */}
        {mode === 'image' ? null : (
          <Select
            onValueChange={(value) => {
              if (value !== null) updateConfig('model', value)
            }}
            value={config.model}
          >
            <SelectTrigger className='h-7 w-[190px] font-mono text-[11.5px]'>
              <SelectValue placeholder={t('Select a model')} />
            </SelectTrigger>
            <SelectContent>
              {visibleModels.map((model) => (
                <SelectItem key={model.value} value={model.value}>
                  {model.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}

        <Select
          onValueChange={(value) => {
            if (value !== null) updateConfig('group', value)
          }}
          value={config.group}
        >
          <SelectTrigger className='h-7 w-[118px] font-mono text-[11.5px]'>
            <SelectValue placeholder={t('Group')} />
          </SelectTrigger>
          <SelectContent>
            {groups.map((group) => (
              <SelectItem key={group.value} value={group.value}>
                {group.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <SpendBadge
          className='ml-auto'
          count={ledger.totals.count}
          quota={ledger.totals.quota}
          settled={ledger.totals.settled}
        />
      </header>

      {mode === 'image' ? null : (
        <div className={cn('px-4 pt-1.5 pb-1')}>
          <PriceStrip
            group={config.group}
            mode={mode}
            model={config.model}
            pricing={ledger.pricing}
          />
        </div>
      )}

      {/*
        One motion.div keyed by mode, not three siblings that get swapped. The
        key change is what makes the workspace arrive with a spring; the point
        is that switching feels like moving within one surface rather than
        loading a different page.
      */}
      <motion.div
        animate={{ opacity: 1, y: 0 }}
        className='flex min-h-0 flex-1 flex-col overflow-hidden'
        initial={{ opacity: 0, y: 6 }}
        key={mode}
        transition={workspaceSpring}
      >
        {mode === 'chat' ? (
          <ChatWorkspace
            clearMessages={clearMessages}
            config={config}
            expandedBillingKey={expandedBillingKey}
            groups={groups}
            isLoadingMessages={isLoadingMessages}
            isLoadingModels={isLoadingModels}
            ledger={ledger}
            messages={messages}
            models={visibleModels}
            onCopyCurl={handleCopyCurl}
            onOpenRequest={handleOpenRequest}
            onToggleBilling={handleToggleBilling}
            parameterEnabled={parameterEnabled}
            updateConfig={updateConfig}
            updateMessages={updateMessages}
            updateParameterEnabled={updateParameterEnabled}
          />
        ) : null}

        {mode === 'image' ? (
          <ImageStage
            expandedBillingKey={expandedBillingKey}
            group={config.group}
            ledger={ledger}
            model={config.model}
            models={visibleModels}
            onCopyCurl={handleCopyCurl}
            onModelChange={(value) => updateConfig('model', value)}
            onOpenRequest={handleOpenRequest}
            onToggleBilling={handleToggleBilling}
          />
        ) : null}

        {mode === 'video' ? (
          <VideoWorkspace
            expandedBillingKey={expandedBillingKey}
            group={config.group}
            ledger={ledger}
            model={config.model}
            onCopyCurl={handleCopyCurl}
            onOpenRequest={handleOpenRequest}
            onToggleBilling={handleToggleBilling}
          />
        ) : null}
      </motion.div>

      <RequestDrawer
        billing={drawerKey ? (ledger.billingByKey[drawerKey] ?? null) : null}
        group={config.group}
        mode={mode}
        modelName={config.model}
        onOpenChange={(open) => {
          if (!open) setDrawerKey(null)
        }}
        open={drawerKey !== null}
      />
    </div>
  )
}
