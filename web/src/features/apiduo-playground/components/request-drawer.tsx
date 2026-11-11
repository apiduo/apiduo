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

import { apiduoBlockMark } from '@/features/apiduo-marks/marks'
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'

import { formatQuota } from '../billing/quota'
import type { MessageBilling } from '../hooks/use-billing-ledger'
import { buildRequestCurl, type RequestMeta } from '../lib/request-meta'
import type { PlaygroundMode } from '../types'

/* The playground route is the public one with the /pg prefix — except image
   edits, which are a different route, not a different body. */
function playgroundPath(mode: PlaygroundMode, refs: number): string {
  if (mode === 'image') {
    return refs > 0 ? '/pg/images/edits' : '/pg/images/generations'
  }
  if (mode === 'video') return '/pg/video/generations'
  return '/pg/chat/completions'
}

const SETTLEMENT_LABELS: Record<string, string> = {
  settled: 'Matched the consume log',
  estimated: 'Estimated, log not written yet',
  pending: 'Reconciling…',
}

interface RequestDrawerProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  billing: MessageBilling | null
  modelName: string
  group: string
  mode: PlaygroundMode
  /** 这一笔运行实际发出的内容；刷新后为空，回落到示例。 */
  meta?: RequestMeta | null
}

interface KvRowProps {
  label: string
  value: string
}

function KvRow({ label, value }: KvRowProps) {
  return (
    <div className='border-border/60 flex gap-3 border-b border-dashed py-1.5 text-[11.5px] last:border-b-0'>
      <span className='text-muted-foreground w-[92px] shrink-0'>{label}</span>
      <span className='min-w-0 font-mono text-[11px] break-all'>{value}</span>
    </div>
  )
}

export function RequestDrawer({
  open,
  onOpenChange,
  billing,
  modelName,
  group,
  mode,
  meta,
}: RequestDrawerProps) {
  const { t } = useTranslation()
  const origin =
    typeof window === 'undefined' ? 'https://apiduo.cn' : window.location.origin
  const official = buildRequestCurl(meta, mode, modelName, origin)

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        className='w-full gap-0 overflow-y-auto sm:max-w-[520px]'
        data-apiduo={apiduoBlockMark('playground:billing')}
        side='right'
      >
        <SheetHeader className='border-border/70 border-b px-4 py-3'>
          <SheetTitle className='text-[13px]'>{t('Request details')}</SheetTitle>
        </SheetHeader>

        <div className='flex flex-col gap-2 px-4 py-3'>
          <div className='flex flex-wrap gap-1.5'>
            <span className='bg-muted rounded-full px-2 py-0.5 font-mono text-[10.5px]'>
              {modelName}
            </span>
            <span className='bg-muted rounded-full px-2 py-0.5 font-mono text-[10.5px]'>
              {group}
            </span>
            {billing?.channelId ? (
              <span className='bg-muted rounded-full px-2 py-0.5 font-mono text-[10.5px]'>
                {t('Channel #{{id}}', { id: billing.channelId })}
              </span>
            ) : null}
            {billing ? (
              <span className='rounded-full bg-emerald-500/12 px-2 py-0.5 font-mono text-[10.5px] font-semibold text-emerald-700 dark:text-emerald-400'>
                {formatQuota(billing.quota)}
              </span>
            ) : null}
          </div>

          <details className='border-border/70 bg-background overflow-hidden rounded-lg border'>
            <summary className='bg-muted/40 hover:bg-muted/60 flex cursor-pointer list-none items-center gap-2 px-3 py-2 text-[11.5px] font-semibold transition-colors [&::-webkit-details-marker]:hidden'>
              {t('Request')}
              <span className='text-muted-foreground font-mono text-[10px] font-normal'>
                POST {official.path}
              </span>
              <span className='text-muted-foreground ml-auto text-[9px]'>▸</span>
            </summary>
            <div className='px-3 py-2.5'>
              <pre className='bg-muted/50 overflow-x-auto rounded-md p-2.5 font-mono text-[10.5px] leading-4'>
                {official.body}
              </pre>
            </div>
          </details>

          <details className='border-border/70 bg-background overflow-hidden rounded-lg border'>
            <summary className='bg-muted/40 hover:bg-muted/60 flex cursor-pointer list-none items-center gap-2 px-3 py-2 text-[11.5px] font-semibold transition-colors [&::-webkit-details-marker]:hidden'>
              {t('Routing and settlement')}
              <span className='text-muted-foreground ml-auto text-[9px]'>▸</span>
            </summary>
            <div className='px-3 py-2'>
              <KvRow
                label={t('Request ID')}
                value={billing?.requestId || t('Not available')}
              />
              <KvRow
                label={t('Playground route')}
                value={playgroundPath(mode, meta?.refs ?? 0)}
              />
              <KvRow label={t('Public route')} value={official.path} />
              <KvRow
                label={t('Channel')}
                value={
                  billing?.channelId
                    ? `#${billing.channelId}${billing.channelName ? ` · ${billing.channelName}` : ''}`
                    : t('Pending')
                }
              />
              <KvRow
                label={t('First token')}
                value={
                  billing?.firstTokenMs != null
                    ? `${(billing.firstTokenMs / 1000).toFixed(2)}s`
                    : '-'
                }
              />
              <KvRow
                label={t('Duration')}
                value={
                  billing ? `${(billing.durationMs / 1000).toFixed(2)}s` : '-'
                }
              />
              <KvRow
                label={t('Settlement')}
                value={t(
                  SETTLEMENT_LABELS[billing?.status ?? 'pending'] ?? 'pending'
                )}
              />
            </div>
          </details>

          <details className='border-border/70 bg-background overflow-hidden rounded-lg border'>
            <summary className='bg-muted/40 hover:bg-muted/60 flex cursor-pointer list-none items-center gap-2 px-3 py-2 text-[11.5px] font-semibold transition-colors [&::-webkit-details-marker]:hidden'>
              {t('Equivalent cURL')}
              <span className='text-muted-foreground font-mono text-[10px] font-normal'>
                {t('Public API')}
              </span>
              <span className='text-muted-foreground ml-auto text-[9px]'>▸</span>
            </summary>
            <div className='px-3 py-2.5'>
              <pre className='bg-muted/50 overflow-x-auto rounded-md p-2.5 font-mono text-[10.5px] leading-4 whitespace-pre-wrap break-words'>
                {official.curl}
              </pre>
              <p className='text-muted-foreground mt-2 text-[10.5px] leading-4'>
                {t(
                  'Replace sk-... with your own API token. The group comes from the token, so the public call never sends it; Playground goes through /pg, which is equivalent.'
                )}
              </p>
            </div>
          </details>
        </div>
      </SheetContent>
    </Sheet>
  )
}
