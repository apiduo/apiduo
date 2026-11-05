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
import { Check } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog } from '@/components/dialog'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

import {
  createAdvancedAsyncConfig,
  formatPathInput,
  formatStatusMapInput,
  normalizeAdvancedAsyncConfig,
  parseAdvancedAsyncConfig,
  parsePathInput,
  parseStatusMapInput,
  stringifyAdvancedAsyncConfig,
  validateAdvancedAsyncConfig,
} from '../../lib/advanced-async'
import type {
  AdvancedAsyncConfig,
  AdvancedCustomRouteAuth,
} from '../../types'

type AdvancedAsyncEditorDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  value: string
  onSave: (value: string) => void
}

const AUTH_TYPES = ['header', 'query', 'none'] as const
const SUBMIT_METHODS = ['POST', 'PUT', 'PATCH'] as const
const QUERY_METHODS = ['GET', 'POST'] as const
const CONTENT_TYPES = ['json', 'multipart'] as const
const TASK_ID_LOCATIONS = ['query', 'path'] as const

export function AdvancedAsyncEditorDialog({
  open,
  onOpenChange,
  value,
  onSave,
}: AdvancedAsyncEditorDialogProps) {
  const { t } = useTranslation()
  const [config, setConfig] = useState<AdvancedAsyncConfig>(
    () => parseAdvancedAsyncConfig(value) || createAdvancedAsyncConfig()
  )
  const [activeTab, setActiveTab] = useState<'form' | 'json'>('form')
  const [jsonText, setJsonText] = useState(() =>
    stringifyAdvancedAsyncConfig(
      parseAdvancedAsyncConfig(value) || createAdvancedAsyncConfig()
    )
  )
  const [jsonError, setJsonError] = useState<string | null>(null)

  useEffect(() => {
    if (!open) return
    const next = parseAdvancedAsyncConfig(value) || createAdvancedAsyncConfig()
    setConfig(next)
    setJsonText(stringifyAdvancedAsyncConfig(next))
    setJsonError(null)
    setActiveTab('form')
  }, [open, value])

  const validationError = useMemo(
    () => validateAdvancedAsyncConfig(config),
    [config]
  )

  const update = (patch: Partial<AdvancedAsyncConfig>) => {
    setConfig((current) => normalizeAdvancedAsyncConfig({ ...current, ...patch }))
  }

  const updateSubmit = (patch: Partial<AdvancedAsyncConfig['submit']>) => {
    update({ submit: { ...config.submit, ...patch } })
  }

  const updateQuery = (patch: Partial<AdvancedAsyncConfig['query']>) => {
    update({ query: { ...config.query, ...patch } })
  }

  const updateFields = (patch: Partial<AdvancedAsyncConfig['fields']>) => {
    update({ fields: { ...config.fields, ...patch } })
  }

  const updateAuth = (
    target: 'submit' | 'query',
    patch: Partial<AdvancedCustomRouteAuth>
  ) => {
    const current =
      target === 'submit' ? config.submit.auth : config.query.auth
    const next: AdvancedCustomRouteAuth = {
      type: current?.type || 'header',
      name: current?.name || '',
      value: current?.value || '',
      ...patch,
    }
    if (target === 'submit') updateSubmit({ auth: next })
    else updateQuery({ auth: next })
  }

  const switchTab = (nextTab: string) => {
    if (nextTab === 'json') {
      setJsonText(stringifyAdvancedAsyncConfig(config))
      setJsonError(null)
    }
    setActiveTab(nextTab as 'form' | 'json')
  }

  const applyJson = (): AdvancedAsyncConfig | null => {
    const parsed = parseAdvancedAsyncConfig(jsonText)
    if (!parsed) {
      setJsonError(t('Invalid JSON'))
      return null
    }
    const error = validateAdvancedAsyncConfig(parsed)
    if (error) {
      setJsonError(t(error.message))
      return null
    }
    setJsonError(null)
    setConfig(parsed)
    return parsed
  }

  const save = () => {
    const next = activeTab === 'json' ? applyJson() : config
    if (!next) return
    if (validateAdvancedAsyncConfig(next)) return
    onSave(stringifyAdvancedAsyncConfig(next))
    onOpenChange(false)
  }

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('Advanced async routes')}
      description={t(
        'Describe the upstream task API. The gateway submits work and polls for the result.'
      )}
      contentClassName='flex max-h-[min(90dvh,var(--dialog-available-height))] flex-col gap-0 p-0 sm:max-w-4xl'
      headerClassName='border-b px-6 py-4'
      footerClassName='border-t px-6 py-4'
      contentHeight='70vh'
      footer={
        <>
          <Button
            type='button'
            variant='outline'
            onClick={() => onOpenChange(false)}
          >
            {t('Cancel')}
          </Button>
          <Button
            type='button'
            onClick={save}
            disabled={activeTab === 'form' && validationError !== null}
          >
            <Check data-icon='inline-start' />
            {t('Save changes')}
          </Button>
        </>
      }
    >
      <Tabs value={activeTab} onValueChange={switchTab} className='min-w-0 gap-0'>
        <div className='border-b px-4 py-3'>
          <TabsList className='grid h-auto w-full grid-cols-2 gap-1'>
            <TabsTrigger value='form'>{t('Form')}</TabsTrigger>
            <TabsTrigger value='json'>{t('Full JSON')}</TabsTrigger>
          </TabsList>
        </div>

        <TabsContent value='form' className='flex flex-col gap-6 p-4'>
          {validationError ? (
            <Alert variant='destructive'>
              <AlertDescription>{t(validationError.message)}</AlertDescription>
            </Alert>
          ) : null}

          <Field
            label={t('Incoming paths')}
            hint={t('One client path per line, for example /v1/video/generations')}
          >
            <textarea
              className='border-input bg-background min-h-16 w-full rounded-md border px-3 py-2 font-mono text-sm'
              value={(config.incoming_paths || []).join('\n')}
              onChange={(event) =>
                update({
                  incoming_paths: event.target.value
                    .split('\n')
                    .map((line) => line.trim())
                    .filter(Boolean),
                })
              }
            />
          </Field>

          <section className='space-y-3'>
            <h3 className='text-sm font-medium'>{t('Submit')}</h3>
            <div className='grid gap-3 sm:grid-cols-2'>
              <Field label={t('Upstream path')}>
                <input
                  className='border-input bg-background h-9 w-full rounded-md border px-3 font-mono text-sm'
                  value={config.submit.upstream_path}
                  placeholder='/v1/video_generation'
                  onChange={(event) =>
                    updateSubmit({ upstream_path: event.target.value })
                  }
                />
              </Field>
              <Field label={t('Method')}>
                <select
                  className='border-input bg-background h-9 w-full rounded-md border px-3 text-sm'
                  value={config.submit.method || 'POST'}
                  onChange={(event) =>
                    updateSubmit({ method: event.target.value })
                  }
                >
                  {SUBMIT_METHODS.map((method) => (
                    <option key={method} value={method}>
                      {method}
                    </option>
                  ))}
                </select>
              </Field>
              <Field label={t('Content type')}>
                <select
                  className='border-input bg-background h-9 w-full rounded-md border px-3 text-sm'
                  value={config.submit.content_type || 'json'}
                  onChange={(event) =>
                    updateSubmit({ content_type: event.target.value })
                  }
                >
                  {CONTENT_TYPES.map((type) => (
                    <option key={type} value={type}>
                      {type}
                    </option>
                  ))}
                </select>
              </Field>
            </div>
            <AuthEditor
              auth={config.submit.auth}
              onChange={(patch) => updateAuth('submit', patch)}
              label={t('Submit auth')}
            />
          </section>

          <section className='space-y-3'>
            <h3 className='text-sm font-medium'>{t('Query')}</h3>
            <div className='grid gap-3 sm:grid-cols-2'>
              <Field
                label={t('Upstream path')}
                hint={t('Use {task_id} to place the id in the path')}
              >
                <input
                  className='border-input bg-background h-9 w-full rounded-md border px-3 font-mono text-sm'
                  value={config.query.upstream_path}
                  placeholder='/v1/query/video_generation'
                  onChange={(event) =>
                    updateQuery({ upstream_path: event.target.value })
                  }
                />
              </Field>
              <Field label={t('Method')}>
                <select
                  className='border-input bg-background h-9 w-full rounded-md border px-3 text-sm'
                  value={config.query.method || 'GET'}
                  onChange={(event) => updateQuery({ method: event.target.value })}
                >
                  {QUERY_METHODS.map((method) => (
                    <option key={method} value={method}>
                      {method}
                    </option>
                  ))}
                </select>
              </Field>
              <Field label={t('Task id location')}>
                <select
                  className='border-input bg-background h-9 w-full rounded-md border px-3 text-sm'
                  value={config.query.task_id_location || 'query'}
                  onChange={(event) =>
                    updateQuery({ task_id_location: event.target.value })
                  }
                >
                  {TASK_ID_LOCATIONS.map((location) => (
                    <option key={location} value={location}>
                      {location}
                    </option>
                  ))}
                </select>
              </Field>
              <Field label={t('Task id parameter')}>
                <input
                  className='border-input bg-background h-9 w-full rounded-md border px-3 font-mono text-sm'
                  value={config.query.task_id_param || 'task_id'}
                  onChange={(event) =>
                    updateQuery({ task_id_param: event.target.value })
                  }
                />
              </Field>
            </div>
            <AuthEditor
              auth={config.query.auth}
              onChange={(patch) => updateAuth('query', patch)}
              label={t('Query auth')}
            />
          </section>

          <section className='space-y-3'>
            <h3 className='text-sm font-medium'>{t('Response fields')}</h3>
            <p className='text-muted-foreground text-xs'>
              {t('Dot-separated paths into the vendor JSON, e.g. data.task.id')}
            </p>
            <div className='grid gap-3 sm:grid-cols-2'>
              <PathField
                label={t('Task id path')}
                value={config.fields.task_id_path}
                onChange={(path) => updateFields({ task_id_path: path })}
              />
              <PathField
                label={t('Status path')}
                value={config.fields.status_path}
                onChange={(path) => updateFields({ status_path: path })}
              />
              <PathField
                label={t('Result URL path')}
                value={config.fields.result_url_path}
                onChange={(path) => updateFields({ result_url_path: path })}
              />
              <PathField
                label={t('Progress path')}
                value={config.fields.progress_path}
                onChange={(path) => updateFields({ progress_path: path })}
              />
              <PathField
                label={t('Error path')}
                value={config.fields.error_path}
                onChange={(path) => updateFields({ error_path: path })}
              />
            </div>
          </section>

          <Field
            label={t('Status mapping')}
            hint={t('One vendor status per line: vendor = GATEWAY_STATUS')}
          >
            <textarea
              className='border-input bg-background min-h-24 w-full rounded-md border px-3 py-2 font-mono text-sm'
              value={formatStatusMapInput(config.fields.status_map)}
              placeholder={'succeeded = SUCCESS\nfailed = FAILURE'}
              onChange={(event) =>
                updateFields({
                  status_map: parseStatusMapInput(event.target.value),
                })
              }
            />
          </Field>
        </TabsContent>

        <TabsContent value='json' className='flex flex-col gap-3 p-4'>
          {jsonError ? (
            <Alert variant='destructive'>
              <AlertDescription>{jsonError}</AlertDescription>
            </Alert>
          ) : null}
          <textarea
            className='border-input bg-background min-h-[420px] w-full rounded-md border px-3 py-2 font-mono text-xs'
            value={jsonText}
            onChange={(event) => setJsonText(event.target.value)}
            onBlur={applyJson}
          />
        </TabsContent>
      </Tabs>
    </Dialog>
  )
}

function Field(props: {
  label: string
  hint?: string
  children: React.ReactNode
}) {
  return (
    <label className='flex flex-col gap-1.5'>
      <span className='text-sm font-medium'>{props.label}</span>
      {props.children}
      {props.hint ? (
        <span className='text-muted-foreground text-xs'>{props.hint}</span>
      ) : null}
    </label>
  )
}

function PathField(props: {
  label: string
  value: string[] | undefined
  onChange: (value: string[]) => void
}) {
  return (
    <Field label={props.label}>
      <input
        className='border-input bg-background h-9 w-full rounded-md border px-3 font-mono text-sm'
        value={formatPathInput(props.value)}
        placeholder='data.task.id'
        onChange={(event) => props.onChange(parsePathInput(event.target.value))}
      />
    </Field>
  )
}

function AuthEditor(props: {
  auth: AdvancedCustomRouteAuth | undefined
  onChange: (patch: Partial<AdvancedCustomRouteAuth>) => void
  label: string
}) {
  const { t } = useTranslation()
  const authType = props.auth?.type || 'header'
  return (
    <div className='space-y-2'>
      <div className='flex items-center gap-2'>
        <span className='text-sm font-medium'>{props.label}</span>
        <Badge variant='outline'>{authType}</Badge>
      </div>
      <div className='grid gap-3 sm:grid-cols-3'>
        <Field label={t('Type')}>
          <select
            className='border-input bg-background h-9 w-full rounded-md border px-3 text-sm'
            value={authType}
            onChange={(event) =>
              props.onChange({
                type: event.target.value as AdvancedCustomRouteAuth['type'],
              })
            }
          >
            {AUTH_TYPES.map((type) => (
              <option key={type} value={type}>
                {type}
              </option>
            ))}
          </select>
        </Field>
        <Field label={t('Name')}>
          <input
            className='border-input bg-background h-9 w-full rounded-md border px-3 font-mono text-sm'
            value={props.auth?.name || ''}
            disabled={authType === 'none'}
            placeholder='Authorization'
            onChange={(event) => props.onChange({ name: event.target.value })}
          />
        </Field>
        <Field label={t('Value')}>
          <input
            className='border-input bg-background h-9 w-full rounded-md border px-3 font-mono text-sm'
            value={props.auth?.value || ''}
            disabled={authType === 'none'}
            placeholder='Bearer {api_key}'
            onChange={(event) => props.onChange({ value: event.target.value })}
          />
        </Field>
      </div>
    </div>
  )
}
