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
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { RefreshCw } from 'lucide-react'
import { useState } from 'react'

import { Button } from '@/components/ui/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

import { mcpApi, mcpKeys } from './api'
import { useMcpCopy } from './copy'
import { AuditPanel, ObservationsPanel } from './insights-panel'
import { ProfilesPanel } from './profiles-panel'
import { RulesPanel } from './rules-panel'

/**
 * MCP 能力注册。
 *
 * 这一页原来是独立地址 /mcp/admin 上的自包含 HTML，靠 iframe 嵌进设置页。
 * 现在改成原生分区：控件与相邻分区同源（Card/Input/Select/Table），
 * 滚动只有一层，未保存状态直接用路由守卫，不再需要 postMessage 传令牌和主题。
 *
 * /mcp/admin 仍然保留：外部书签和脚本可能直接打那个地址，页面本身没有密钥，
 * 公开可读不影响安全。
 */
export function McpSection() {
  const t = useMcpCopy()
  const queryClient = useQueryClient()
  const [tab, setTab] = useState('models')
  const [activeModelId, setActiveModelId] = useState<string | null>(null)

  const overview = useQuery({
    queryKey: mcpKeys.overview(),
    queryFn: mcpApi.overview,
    retry: false,
  })

  const profiles = useQuery({
    queryKey: mcpKeys.profiles(),
    queryFn: mcpApi.profiles,
    retry: false,
  })

  const list = profiles.data?.items ?? []
  const active = list.find((p) => p.model_id === activeModelId) ?? null

  const summary = overview.data
  // 产物的种类名要跟表单里的下拉一致，不能直接把 image/video 这种键印到概览行。
  const kindText: Record<string, string> = {
    image: t('Image'),
    video: t('Video'),
    search: t('Search'),
  }
  const kindParts = summary
    ? Object.entries(summary.kinds).map(
        ([kind, n]) => `${kindText[kind] ?? kind} ${n}`
      )
    : []

  return (
    <div className='flex min-h-0 flex-col gap-4'>
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <p className='text-muted-foreground text-xs'>
          {summary
            ? [
                t('{{count}} models', { count: summary.profiles }),
                t('enabled {{count}}', { count: summary.enabled }),
                ...kindParts,
                `${t('Log store')} ${summary.log_db}`,
              ].join(' · ')
            : t('Failed to load')}
        </p>
        <Button
          size='sm'
          variant='outline'
          onClick={() => {
            void queryClient.invalidateQueries({ queryKey: mcpKeys.all })
          }}
        >
          <RefreshCw className='size-3.5' />
          {t('Refresh')}
        </Button>
      </div>

      <Tabs value={tab} onValueChange={setTab} className='min-h-0'>
        <TabsList>
          <TabsTrigger value='models'>{t('Models')}</TabsTrigger>
          <TabsTrigger value='rules'>{t('Error rules')}</TabsTrigger>
          <TabsTrigger value='observations'>{t('Observations')}</TabsTrigger>
          <TabsTrigger value='inspection'>{t('Inspection')}</TabsTrigger>
        </TabsList>

        <TabsContent value='models' className='mt-4'>
          <ProfilesPanel
            profiles={list}
            loading={profiles.isLoading}
            onActiveModelChange={setActiveModelId}
            onDataChanged={() => {
              void queryClient.invalidateQueries({ queryKey: mcpKeys.overview() })
            }}
          />
        </TabsContent>

        <TabsContent value='rules' className='mt-4'>
          <RulesPanel profiles={list} activeModelId={activeModelId} />
        </TabsContent>

        <TabsContent value='observations' className='mt-4'>
          <ObservationsPanel
            profileId={active?.id ?? null}
            modelId={active?.model_id ?? null}
          />
        </TabsContent>

        <TabsContent value='inspection' className='mt-4'>
          <AuditPanel
            profileId={active?.id ?? null}
            modelId={active?.model_id ?? null}
          />
        </TabsContent>
      </Tabs>
    </div>
  )
}
