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
import { useMutation, useQuery } from '@tanstack/react-query'
import { Check, Copy } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { SectionPageLayout } from '@/components/layout'
import { cn } from '@/lib/utils'

import {
  createMcpToken,
  getTokenKey,
  isUsable,
  listTokens,
  type McpToken,
} from './api'
import { CLIENTS, jsonTemplate, MARKS, MCP_PATH, TOKEN_SLOT } from './clients'
import { TokenPicker } from './components/token-picker'
import './mcp.css'

async function writeClipboard(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    return
  } catch {
    // Older browsers and non-secure origins reject the async API; fall back to
    // a hidden textarea so the copy button never silently does nothing.
  }
  const area = document.createElement('textarea')
  area.value = text
  area.style.position = 'fixed'
  area.style.opacity = '0'
  document.body.appendChild(area)
  area.select()
  document.execCommand('copy')
  document.body.removeChild(area)
}

/** Single pass so a key's quotes are never re-matched as a value. */
function highlightJson(src: string) {
  const out: React.ReactNode[] = []
  const re = /("(?:\\.|[^"\\])*")(\s*:)?|[{}]/g
  let last = 0
  let match: RegExpExecArray | null
  let i = 0
  while ((match = re.exec(src))) {
    if (match.index > last) out.push(src.slice(last, match.index))
    if (match[0] === '{' || match[0] === '}') {
      out.push(
        <span key={i++} className='text-muted-foreground'>
          {match[0]}
        </span>
      )
    } else if (match[2]) {
      out.push(
        <span key={i++}>
          <span className='text-violet-600 dark:text-violet-400'>{match[1]}</span>
          <span className='text-muted-foreground'>{match[2]}</span>
        </span>
      )
    } else {
      out.push(
        <span key={i++} className='text-emerald-700 dark:text-emerald-400'>
          {match[1]}
        </span>
      )
    }
    last = re.lastIndex
  }
  out.push(src.slice(last))
  return out
}

export function ApiduoMcp() {
  const { t } = useTranslation()
  const [clientId, setClientId] = useState(CLIENTS[0].id)
  const [selectedId, setSelectedId] = useState<number | null>(null)
  const [copiedKey, setCopiedKey] = useState<string | null>(null)

  const tokensQuery = useQuery({
    queryKey: ['apiduo-mcp-tokens'],
    queryFn: listTokens,
    staleTime: 60_000,
  })

  const tokens = useMemo(() => tokensQuery.data ?? [], [tokensQuery.data])

  useEffect(() => {
    if (selectedId != null || tokens.length === 0) return
    setSelectedId((tokens.find(isUsable) ?? tokens[0]).id)
  }, [tokens, selectedId])

  const selected = tokens.find((token) => token.id === selectedId) ?? null

  const keyQuery = useQuery({
    queryKey: ['apiduo-mcp-key', selectedId],
    queryFn: () => getTokenKey(selectedId as number),
    enabled: selectedId != null,
    staleTime: 5 * 60_000,
    retry: 0,
  })

  const createMutation = useMutation({
    mutationFn: () =>
      createMcpToken(`MCP ${new Date().toLocaleDateString()}`),
    onSuccess: async () => {
      // The create endpoint answers `{success}` and no id, so the new token is
      // found in the refreshed list instead: it is ordered by id desc.
      const fresh = await tokensQuery.refetch()
      const newest = (fresh.data ?? []).reduce<McpToken | null>(
        (best, token) => (!best || token.id > best.id ? token : best),
        null
      )
      if (newest) setSelectedId(newest.id)
      toast.success(t('Token created'))
    },
    onError: () => toast.error(t('Could not create a token')),
  })

  const client = CLIENTS.find((item) => item.id === clientId) ?? CLIENTS[0]
  const mcpUrl = `${window.location.origin}${MCP_PATH}`
  const plainKey = keyQuery.data ?? ''

  const fill = (value: string) =>
    value.split('{url}').join(mcpUrl).split(TOKEN_SLOT).join(plainKey || TOKEN_SLOT)

  const json = client.json
    ? fill(jsonTemplate(client.json, mcpUrl))
    : ''

  const copy = async (slot: string, text: string) => {
    await writeClipboard(text)
    setCopiedKey(slot)
    window.setTimeout(
      () => setCopiedKey((current) => (current === slot ? null : current)),
      1100
    )
  }

  /**
   * 把值里的令牌占位符换成真正的选择器 —— 令牌就长在它被使用的那一行里，
   * 不再单独占一行。复制仍然取整行文本，所以拿到的永远是完整值。
   */
  const renderValue = (value: string) => {
    const at = value.indexOf(TOKEN_SLOT)
    if (at < 0) return fill(value)
    // 一条值里最多一个占位符；真有两个也只把第一个换成选择器，剩下的按纯文本填。
    const tail = value.slice(at + TOKEN_SLOT.length)
    return (
      <>
        {fill(value.slice(0, at))}
        <TokenPicker
          tokens={tokens}
          selected={selected}
          creating={createMutation.isPending}
          onSelect={setSelectedId}
          onCreate={() => createMutation.mutate()}
        />
        {fill(tail)}
      </>
    )
  }

  const rowsText = client.rows
    .map((row) => `${row.label}: ${fill(row.value)}`)
    .join('\n')

  const copyButton = (slot: string, text: string, label: string, wide = false) => {
    const done = copiedKey === slot
    return (
      <button
        type='button'
        title={label}
        onClick={() => copy(slot, text)}
        className={cn(
          'apiduo-mcp-press inline-flex h-[26px] items-center justify-center rounded-md border text-[11.5px]',
          done
            ? 'border-emerald-300 bg-emerald-50 text-emerald-600 dark:border-emerald-800 dark:bg-emerald-950/40 dark:text-emerald-400'
            : 'border-border bg-background text-muted-foreground hover:bg-muted/60 hover:text-foreground',
          wide ? 'min-w-[86px] gap-1.5 px-2.5' : 'w-[26px]'
        )}
      >
        {done ? (
          <Check className='apiduo-mcp-pop size-3.5' strokeWidth={2.4} />
        ) : (
          <Copy className='size-3.5' strokeWidth={1.9} />
        )}
        {wide && (done ? t('Copied') : label)}
      </button>
    )
  }

  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>{t('MCP access')}</SectionPageLayout.Title>
      <SectionPageLayout.Content>
        <div className='apiduo-mcp w-full max-w-[784px] pb-12 pt-1'>
          <p className='apiduo-mcp-rise-soft text-sm text-muted-foreground'>
            {t(
              'Pick a client, then drop the fields below into its MCP settings. The token sits in the Authorization header itself — click it to switch. Clients that read a config file keep theirs under the JSON block.'
            )}
          </p>

          <div className='apiduo-mcp-rise mt-5 grid grid-cols-[34px_minmax(0,1fr)] items-center gap-x-1.5 gap-y-2 max-sm:grid-cols-1'>
            <span className='text-right text-[11px] tracking-wide text-muted-foreground max-sm:hidden'>
              {t('China clients')}
            </span>
            <div className='flex flex-wrap gap-1.5 max-sm:-mx-4 max-sm:flex-nowrap max-sm:overflow-x-auto max-sm:px-4 max-sm:[scrollbar-width:none]'>
              {CLIENTS.filter((item) => item.group === 'cn').map((item) => (
                <ClientChip
                  key={item.id}
                  id={item.id}
                  name={item.name}
                  active={item.id === clientId}
                  onClick={() => setClientId(item.id)}
                />
              ))}
            </div>
            <span className='text-right text-[11px] tracking-wide text-muted-foreground max-sm:hidden'>
              {t('General clients')}
            </span>
            <div className='flex flex-wrap gap-1.5 max-sm:-mx-4 max-sm:flex-nowrap max-sm:overflow-x-auto max-sm:px-4 max-sm:[scrollbar-width:none]'>
              {CLIENTS.filter((item) => item.group === 'gen').map((item) => (
                <ClientChip
                  key={item.id}
                  id={item.id}
                  name={item.name}
                  active={item.id === clientId}
                  onClick={() => setClientId(item.id)}
                />
              ))}
            </div>
          </div>

          <section className='apiduo-mcp-rise-soft mt-3.5 rounded-[13px] border border-border bg-card shadow-[0_1px_2px_rgba(20,20,25,0.025),0_8px_24px_-20px_rgba(20,20,25,0.18)]'>
            <div className='flex items-center gap-2.5 rounded-t-xl border-b border-border bg-muted/30 px-3.5 py-2.5 max-sm:flex-wrap'>
              <span className='flex size-[22px] shrink-0 items-center justify-center rounded-md bg-primary/10 text-primary'>
                <span className='block size-3.5 [&>svg]:size-full'>
                  {MARKS[client.id]}
                </span>
              </span>
              <h2 className='shrink-0 text-[13.5px] font-semibold whitespace-nowrap'>{client.name}</h2>
              <span className='min-w-0 truncate text-[11.5px] text-muted-foreground max-sm:order-3 max-sm:basis-full max-sm:whitespace-normal max-sm:overflow-visible'>
                {client.where}
              </span>
              <span className='ml-auto shrink-0'>
                {copyButton('all', rowsText, t('Copy all'), true)}
              </span>
            </div>

            <div
              className='apiduo-mcp-stagger'
            >
              {client.rows.map((row, index) => (
                <div
                  key={`${client.id}-${row.label}`}
                  style={{ '--apiduo-row-index': index } as React.CSSProperties}
                  className='apiduo-mcp-row grid grid-cols-[16px_100px_minmax(0,1fr)_44px] items-center gap-2 border-b border-border px-2.5 py-2 pl-3.5 last:border-b-0 max-sm:grid-cols-[14px_minmax(0,1fr)_34px] max-sm:gap-x-2.5 max-sm:gap-y-0.5'
                >
                  <span className='flex items-center justify-center text-muted-foreground/70'>
                    <FieldIcon label={row.label} />
                  </span>
                  <span className='truncate text-[12.5px] text-muted-foreground max-sm:col-start-2 max-sm:text-[11.5px]'>
                    {row.label}
                  </span>
                  <span className='font-mono text-[12.2px] leading-relaxed [overflow-wrap:anywhere] text-foreground max-sm:col-start-2 max-sm:text-[12.5px]'>
                    {renderValue(row.value)}
                  </span>
                  <span className='justify-self-end max-sm:col-start-3 max-sm:row-span-2 max-sm:row-start-1 max-sm:self-center'>
                    {copyButton(
                      `${client.id}:${index}`,
                      `${row.label}: ${fill(row.value)}`,
                      t('Copy'),
                      false
                    )}
                  </span>
                </div>
              ))}
            </div>

            <p className='rounded-b-xl border-t border-border bg-muted/20 px-3.5 py-2.5 text-xs leading-relaxed text-muted-foreground'>
              {client.note}
            </p>
          </section>

          {client.json && (
            <details className='apiduo-mcp-rise apiduo-mcp-reveal mt-3 border-b border-border pb-3'>
              <summary className='flex cursor-pointer list-none items-center gap-2 py-1 text-[12.5px] text-muted-foreground hover:text-foreground'>
                <span className='size-1.5 rotate-45 border-r-[1.5px] border-b-[1.5px] border-current' />
                {t('Use a config file (JSON)')}
              </summary>
              <div className='mt-2 overflow-hidden rounded-[10px] border border-border bg-muted/20'>
                <div className='flex items-center border-b border-border bg-background px-3 py-1.5'>
                  <span className='font-mono text-[11px] text-muted-foreground'>
                    {client.file}
                  </span>
                  <span className='ml-auto'>{copyButton('json', json, t('Copy'))}</span>
                </div>
                <pre className='overflow-x-auto px-3 py-2.5 font-mono text-[11.8px] leading-relaxed'>
                  {highlightJson(json)}
                </pre>
              </div>
            </details>
          )}

          <details className='apiduo-mcp-rise apiduo-mcp-reveal mt-3 border-t border-border pt-3'>
            <summary className='flex cursor-pointer list-none items-center gap-2 py-1 text-[12.5px] text-muted-foreground hover:text-foreground'>
              <span className='size-1.5 rotate-45 border-r-[1.5px] border-b-[1.5px] border-current' />
              {t('Verify and troubleshoot')}
            </summary>
            <div className='space-y-1.5 pl-4 pt-2 text-[12.8px] leading-relaxed text-muted-foreground'>
              <p>{t('Confirm the token and network first; four tool names should come back:')}</p>
              <pre className='overflow-x-auto rounded-lg border border-border bg-muted/20 px-3 py-2 font-mono text-[11.4px]'>
{`curl -s ${mcpUrl} \\
  -H "Authorization: Bearer ${plainKey || TOKEN_SLOT}" \\
  -H "Content-Type: application/json" \\
  -H "Accept: application/json, text/event-stream" \\
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'`}
              </pre>
              <p>
                <b className='text-foreground'>401</b>{' '}
                {t('— the token is missing or dead. Check the space after Bearer.')}
              </p>
              <p>
                <b className='text-foreground'>400 Accept</b>{' '}
                {t(
                  '— the caller sent no Accept header. curl sends */* by default so it passes; ' +
                  'other tools must set Accept: application/json, text/event-stream.'
                )}
              </p>
              <p>
                <b className='text-foreground'>{t('Hangs forever')}</b>{' '}
                {t(
                  '— it is an old SSE client: it GETs /mcp, gets 405 and retries. This server only takes POST with Accept: application/json, text/event-stream; switch the transport to Streamable HTTP, or bridge it with mcp-remote.'
                )}
              </p>
              <p>
                <b className='text-foreground'>{t('Tools show but calls fail')}</b>{' '}
                {t(
                  '— the error text comes back verbatim. Most often: no enabled model in the capability registry (models.list returns 0), no quota or a model allowlist on the token, or a parameter outside what the model declares — that one is rejected locally and never billed.'
                )}
              </p>
            </div>
          </details>

          <details className='apiduo-mcp-rise apiduo-mcp-reveal mt-3 border-t border-border pt-3'>
            <summary className='flex cursor-pointer list-none items-center gap-2 py-1 text-[12.5px] text-muted-foreground hover:text-foreground'>
              <span className='size-1.5 rotate-45 border-r-[1.5px] border-b-[1.5px] border-current' />
              {t('Four tools')}
            </summary>
            <div className='space-y-1 pl-4 pt-2 text-[12.8px] text-muted-foreground'>
              <p>
                <code className='rounded bg-muted px-1 py-0.5 font-mono text-[11.5px]'>models.list</code>{' '}
                {t('— available models and their parameter constraints')}
              </p>
              <p>
                <code className='rounded bg-muted px-1 py-0.5 font-mono text-[11.5px]'>image.generate</code>{' '}
                {t('— generate images with reference images, aspect ratio and resolution')}
              </p>
              <p>
                <code className='rounded bg-muted px-1 py-0.5 font-mono text-[11.5px]'>video.generate</code>{' '}
                {t('— submit a video job and get a task_id immediately')}
              </p>
              <p>
                <code className='rounded bg-muted px-1 py-0.5 font-mono text-[11.5px]'>task.get</code>{' '}
                {t('— poll a job status and its artifact URLs')}
              </p>
            </div>
          </details>

          <p className='apiduo-mcp-rise mt-6 border-t border-border pt-3.5 text-[11.5px] text-muted-foreground'>
            <span className='apiduo-mcp-breathe mr-1.5 inline-block size-1.5 rounded-full bg-emerald-500 align-[1px]' />
            Streamable HTTP · <code className='font-mono'>{mcpUrl}</code> ·{' '}
            {t('Only generation calls are billed')}
          </p>
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}

function ClientChip({
  id,
  name,
  active,
  onClick,
}: {
  id: string
  name: string
  active: boolean
  onClick: () => void
}) {
  return (
    <button
      type='button'
      onClick={onClick}
      className={cn(
        'apiduo-mcp-press inline-flex h-7 shrink-0 items-center gap-1.5 rounded-full border px-2.5 text-[12.5px] whitespace-nowrap',
        active
          ? 'border-transparent bg-primary/10 font-semibold text-primary'
          : 'border-border bg-background text-muted-foreground hover:bg-muted/60 hover:text-foreground'
      )}
    >
      <span className='block size-3.5 [&>svg]:size-full'>{MARKS[id]}</span>
      {name}
    </button>
  )
}

const FIELD_PATHS: Record<string, React.ReactNode> = {
  tag: (
    <>
      <path d='M11.7 3.7H19a1.3 1.3 0 0 1 1.3 1.3v7.3L12 20.6a1.8 1.8 0 0 1-2.5 0L3.4 14.5a1.8 1.8 0 0 1 0-2.5z' />
      <circle cx='16.2' cy='7.9' r='1.3' />
    </>
  ),
  plug: (
    <>
      <path d='M9 3.4v4.8M15 3.4v4.8' />
      <path d='M6.2 8.2h11.6v2.6a5.8 5.8 0 0 1-11.6 0z' />
      <path d='M12 16.6v4' />
    </>
  ),
  globe: (
    <>
      <circle cx='12' cy='12' r='8.2' />
      <path d='M3.8 12h16.4' />
      <path d='M12 3.8c2.1 2.3 3.2 5.1 3.2 8.2s-1.1 5.9-3.2 8.2c-2.1-2.3-3.2-5.1-3.2-8.2S9.9 6.1 12 3.8z' />
    </>
  ),
  shield: (
    <>
      <path d='M12 3.4 19 6v5.5c0 4.2-2.8 7.5-7 9.1-4.2-1.6-7-4.9-7-9.1V6z' />
      <path d='M9.3 12.1l2 2 3.5-3.7' />
    </>
  ),
  term: (
    <>
      <path d='M4.4 7.2 9 12l-4.6 4.8' />
      <path d='M12.4 16.8h7.2' />
    </>
  ),
  sliders: (
    <>
      <path d='M4 8.3h8M16.6 8.3H20M4 15.7h3.4M11.8 15.7H20' />
      <circle cx='14.4' cy='8.3' r='2.1' />
      <circle cx='9.6' cy='15.7' r='2.1' />
    </>
  ),
}

/** Order matters: the first pattern that matches wins. */
const FIELD_MATCHERS: Array<[RegExp, string]> = [
  [/命令/, 'term'],
  [/参数/, 'sliders'],
  [/头|header/i, 'shield'],
  [/url|地址/i, 'globe'],
  [/类型/, 'plug'],
]

function FieldIcon({ label }: { label: string }) {
  const pick = FIELD_MATCHERS.find(([re]) => re.test(label))?.[1] ?? 'tag'
  return (
    <svg
      viewBox='0 0 24 24'
      fill='none'
      stroke='currentColor'
      strokeWidth={1.7}
      strokeLinecap='round'
      strokeLinejoin='round'
      aria-hidden='true'
      className='size-3.5'
    >
      {FIELD_PATHS[pick]}
    </svg>
  )
}

