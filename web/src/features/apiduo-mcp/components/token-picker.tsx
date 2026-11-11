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
import { Link } from '@tanstack/react-router'
import { Check, ChevronDown, Plus } from 'lucide-react'
import { useEffect, useId, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { cn } from '@/lib/utils'

import { describeBlocker, type McpToken } from '../api'

// 新手提示只在页面首次挂载时出现，切换客户端不重播。
let nudgeShown = false

/** `sk-R**********S5aG` is too wide for the trigger; keep the last four. */
function shortMask(key: string) {
  if (!key) return ''
  return key.length > 4 ? `sk-…${key.slice(-4)}` : key
}

interface TokenPickerProps {
  tokens: McpToken[]
  selected: McpToken | null
  disabled?: boolean
  creating?: boolean
  onSelect: (id: number) => void
  onCreate: () => void
}

export function TokenPicker({
  tokens,
  selected,
  disabled,
  creating,
  onSelect,
  onCreate,
}: TokenPickerProps) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const nudge = useMemo(() => {
    if (nudgeShown) return false
    nudgeShown = true
    return true
  }, [])
  const wrap = useRef<HTMLDivElement>(null)
  const menuId = useId()

  useEffect(() => {
    if (!open) return
    const onPointerDown = (event: MouseEvent) => {
      if (!wrap.current?.contains(event.target as Node)) setOpen(false)
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setOpen(false)
    }
    document.addEventListener('mousedown', onPointerDown)
    document.addEventListener('keydown', onKeyDown)
    return () => {
      document.removeEventListener('mousedown', onPointerDown)
      document.removeEventListener('keydown', onKeyDown)
    }
  }, [open])

  const blocker = selected
    ? describeBlocker(selected, {
        disabled: t('Disabled'),
        expired: t('Expired'),
        exhausted: t('No quota left'),
        ipAllowlist: t('Has an IP allowlist, external clients will be blocked'),
      })
    : ''

  // No usable token: turn the trigger itself into the create action, so a
  // brand-new account never has to leave the page for the token list.
  if (!selected) {
    return (
      <button
        type='button'
        disabled={disabled || creating}
        onClick={onCreate}
        className={cn(
          'apiduo-mcp-press my-0.5 inline-flex h-[22px] items-center gap-1 rounded-md border border-dashed px-1.5 align-middle text-[11.5px] font-medium',
          'border-primary/40 bg-primary/5 text-primary',
          'hover:border-primary/60 hover:bg-primary/10',
          (disabled || creating) && 'pointer-events-none opacity-60'
        )}
      >
        <Plus className='size-3' />
        {creating ? t('Creating…') : t('New MCP token')}
      </button>
    )
  }

  return (
    <div
      ref={wrap}
      className={cn(
        'relative inline-block align-middle',
        nudge && 'apiduo-mcp-nudge'
      )}
    >
      <button
        type='button'
        aria-haspopup='listbox'
        aria-expanded={open}
        aria-controls={menuId}
        onClick={() => setOpen((v) => !v)}
        className={cn(
          'apiduo-mcp-press my-0.5 inline-flex h-[22px] max-w-full items-center gap-1.5 rounded-md border px-1.5 align-middle',
          'border-border bg-muted/60 text-foreground',
          'hover:bg-muted'
        )}
      >
        <span
          aria-hidden
          className={cn(
            'size-1.5 shrink-0 rounded-full',
            blocker ? 'bg-amber-500' : 'bg-emerald-500'
          )}
        />
        <span className='max-w-[132px] truncate font-sans text-[11.5px] font-medium'>
          {selected.name}
        </span>
        <span className='font-mono text-[10.5px] text-muted-foreground'>
          {shortMask(selected.key)}
        </span>
        <ChevronDown
          className={cn(
            'apiduo-mcp-chevron size-3 shrink-0 text-muted-foreground transition-transform duration-200',
            open && 'rotate-180'
          )}
        />
      </button>

      <div
        id={menuId}
        role='listbox'
        data-open={open ? 'true' : 'false'}
        aria-hidden={!open}
        className={cn(
          'apiduo-mcp-menu absolute left-0 z-30 mt-1.5 w-[292px] max-w-[calc(100vw-2rem)]',
          'rounded-xl border border-border p-1 shadow-xl z-50',
          'max-sm:left-auto max-sm:right-0'
        )}
      >
        {tokens.length === 0 ? (
          <p className='px-2 py-2 text-xs text-muted-foreground'>
            {t('This account has no tokens yet.')}
          </p>
        ) : (
          tokens.map((token) => {
            const why = describeBlocker(token, {
              disabled: t('Disabled'),
              expired: t('Expired'),
              exhausted: t('No quota left'),
              ipAllowlist: t(
                'Has an IP allowlist, external clients will be blocked'
              ),
            })
            return (
              <button
                key={token.id}
                type='button'
                role='option'
                aria-selected={token.id === selected.id}
                onClick={() => {
                  onSelect(token.id)
                  setOpen(false)
                }}
                className='apiduo-mcp-press flex w-full items-center gap-2 rounded-lg px-2 py-1.5 text-left hover:bg-muted/70'
              >
                <span
                  aria-hidden
                  className={cn(
                    'mt-[7px] size-1.5 shrink-0 self-start rounded-full',
                    why ? 'bg-amber-500' : 'bg-emerald-500'
                  )}
                />
                <span className='min-w-0 flex-1'>
                  <span className='block truncate text-[12.5px] text-foreground'>
                    {token.name}
                  </span>
                  {why ? (
                    <span className='block text-[11px] text-amber-600'>{why}</span>
                  ) : (
                    <span className='block font-mono text-[11px] text-muted-foreground'>
                      {shortMask(token.key)}
                    </span>
                  )}
                </span>
                {token.id === selected.id && (
                  <Check className='size-3.5 shrink-0 text-primary' />
                )}
              </button>
            )
          })
        )}

        <div className='my-1 h-px bg-border' />
        <button
          type='button'
          disabled={creating}
          onClick={() => {
            onCreate()
            setOpen(false)
          }}
          className='apiduo-mcp-press flex w-full items-center gap-1.5 rounded-lg px-2 py-1.5 text-[12.5px] text-primary hover:bg-primary/10'
        >
          <Plus className='size-3' />
          {creating ? t('Creating…') : t('New MCP token')}
        </button>
        <p className='px-2 pb-1 pt-0.5 text-[11px] text-muted-foreground'>
          {t('No expiry, no quota cap, no IP allowlist.')}
        </p>
        <Link
          to='/keys'
          className='block rounded-lg px-2 py-1.5 text-[12px] text-muted-foreground hover:bg-muted/70 hover:text-foreground'
        >
          {t('Manage tokens')}
        </Link>
      </div>
    </div>
  )
}
