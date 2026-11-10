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
import { Loader2 } from 'lucide-react'
import { useState } from 'react'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

import { mcpApi, mcpKeys } from './api'
import { useMcpCopy } from './copy'
import { RETRY_ACTIONS, type ErrorRule } from './types'

type RulesPanelProps = {
  profiles: { model_id: string }[]
  activeModelId: string | null
}

/** 一行规则：可重试 / 动作 / 话术，改完按行保存。 */
function RuleTable(props: {
  rows: ErrorRule[]
  onSaved: () => void
  t: (key: string) => string
}) {
  const [drafts, setDrafts] = useState<Record<number, Partial<ErrorRule>>>({})
  const [savingId, setSavingId] = useState<number | null>(null)
  const [savedId, setSavedId] = useState<number | null>(null)

  const save = useMutation({
    mutationFn: async (row: ErrorRule) => {
      const merged = { ...row, ...drafts[row.id] }
      setSavingId(row.id)
      return mcpApi.saveRule(row.id, {
        retryable: merged.retryable,
        retry_action: merged.retry_action,
        retry_after_ms: merged.retry_after_ms ?? 0,
        hint: merged.hint,
        enabled: true,
      })
    },
    onSuccess: (_data, row) => {
      setSavedId(row.id)
      setDrafts((prev) => {
        const next = { ...prev }
        delete next[row.id]
        return next
      })
      props.onSaved()
    },
    onError: (error) =>
      toast.error(error instanceof Error ? error.message : String(error)),
    onSettled: () => setSavingId(null),
  })

  if (props.rows.length === 0) {
    return (
      <p className='text-muted-foreground py-4 text-xs'>{props.t('No rules yet')}</p>
    )
  }

  const saveLabel = (id: number) => {
    if (savingId === id) return <Loader2 className='size-3 animate-spin' />
    if (savedId === id && !drafts[id]) return props.t('Saved')
    return props.t('Save')
  }

  const patch = (id: number, next: Partial<ErrorRule>) =>
    setDrafts((prev) => ({ ...prev, [id]: { ...prev[id], ...next } }))

  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead className='w-[180px]'>{props.t('Match')}</TableHead>
          <TableHead className='w-[88px]'>{props.t('Retryable')}</TableHead>
          <TableHead className='w-[112px]'>{props.t('Action')}</TableHead>
          <TableHead>{props.t('Message to the agent')}</TableHead>
          <TableHead className='w-[64px]' />
        </TableRow>
      </TableHeader>
      <TableBody>
        {props.rows.map((row) => {
          const draft = { ...row, ...drafts[row.id] }
          return (
            <TableRow key={row.id}>
              <TableCell className='font-mono text-xs'>{row.match}</TableCell>
              <TableCell>
                <Select
                  value={draft.retryable ? '1' : '0'}
                  onValueChange={(v) => patch(row.id, { retryable: v === '1' })}
                >
                  <SelectTrigger className='h-7 w-full text-xs'>
                    <SelectValue>
                      {draft.retryable ? props.t('Yes') : props.t('No')}
                    </SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value='1'>{props.t('Yes')}</SelectItem>
                    <SelectItem value='0'>{props.t('No')}</SelectItem>
                  </SelectContent>
                </Select>
              </TableCell>
              <TableCell>
                <Select
                  value={draft.retry_action}
                  onValueChange={(v) =>
                    patch(row.id, { retry_action: v as string })
                  }
                >
                  <SelectTrigger className='h-7 w-full text-xs'>
                    <SelectValue>
                      {RETRY_ACTIONS.find((a) => a.value === draft.retry_action)
                        ?.label ?? draft.retry_action}
                    </SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    {RETRY_ACTIONS.map((a) => (
                      <SelectItem key={a.value} value={a.value}>
                        {a.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </TableCell>
              <TableCell>
                <Input
                  className='h-7 text-xs'
                  value={draft.hint}
                  onChange={(e) => patch(row.id, { hint: e.target.value })}
                />
              </TableCell>
              <TableCell>
                <Button
                  size='sm'
                  variant='ghost'
                  className='h-7 px-2 text-xs'
                  disabled={savingId === row.id || !drafts[row.id]}
                  onClick={() => save.mutate(row)}
                >
                  {saveLabel(row.id)}
                </Button>
              </TableCell>
            </TableRow>
          )
        })}
      </TableBody>
    </Table>
  )
}

export function RulesPanel(props: RulesPanelProps) {
  const t = useMcpCopy()
  const [modelId, setModelId] = useState<string>(props.activeModelId ?? '')

  const query = useQuery({
    queryKey: mcpKeys.rules(modelId || undefined),
    queryFn: () => mcpApi.rules(modelId || undefined),
    retry: false,
  })

  const items = query.data?.items ?? []
  const global = items.filter((r) => r.scope === 'global')
  const model = items.filter((r) => r.scope === 'model')

  return (
    <div className='space-y-4'>
      <Card>
        <CardHeader className='flex-row items-center justify-between gap-3'>
          <div>
            <CardTitle className='text-sm'>{t('Global defaults')}</CardTitle>
            <p className='text-muted-foreground mt-1 text-xs'>
              {t(
                'Unmatched errors are treated as non-retryable; retrying costs money'
              )}
            </p>
          </div>
          <Select
            value={modelId || '__none__'}
            onValueChange={(v) => setModelId(!v || v === '__none__' ? '' : v)}
          >
            <SelectTrigger className='w-[220px]'>
              <SelectValue>{modelId || '—'}</SelectValue>
            </SelectTrigger>
            <SelectContent>
              <SelectItem value='__none__'>{t('Models')} · —</SelectItem>
              {props.profiles.map((p) => (
                <SelectItem key={p.model_id} value={p.model_id}>
                  {p.model_id}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </CardHeader>
        <CardContent className='overflow-x-auto'>
          {query.isLoading ? (
            <div className='text-muted-foreground flex items-center gap-2 py-4 text-xs'>
              <Loader2 className='size-3.5 animate-spin' />
            </div>
          ) : (
            <RuleTable
              rows={global}
              t={t}
              onSaved={() => void query.refetch()}
            />
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader className='pb-3'>
          <CardTitle className='text-sm'>{t('Model overrides')}</CardTitle>
        </CardHeader>
        <CardContent className='overflow-x-auto'>
          {model.length === 0 ? (
            <p className='text-muted-foreground py-2 text-xs'>
              {t('This model has no overrides — the global defaults apply')}
            </p>
          ) : (
            <RuleTable
              rows={model}
              t={t}
              onSaved={() => void query.refetch()}
            />
          )}
        </CardContent>
      </Card>
    </div>
  )
}
