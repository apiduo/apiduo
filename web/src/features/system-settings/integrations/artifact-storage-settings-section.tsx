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
import { ArrowDown, ArrowUp, Plus, Trash2 } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'

import {
  SettingsFormGrid,
  SettingsFormGridItem,
  SettingsSwitchField,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'
import {
  ARTIFACT_STORAGE_PROVIDERS,
  parseArtifactStorageTargets,
  providerDefaults,
  type ArtifactStorageTarget,
} from '../artifact-storage-types'
import type { SiteSettings } from '../types'

const MEGABYTE = 1024 * 1024

type StorageState = {
  enabled: boolean
  targets: ArtifactStorageTarget[]
  presignTTLSeconds: number
  maxObjectBytes: number
  /**
   * One selector per line. It is kept as text because the operator edits it as
   * text; the JSON form is only produced on save.
   */
  rewritePaths: string
  /** Field names used when no selector is configured. */
  fieldNames: string
}

type ArtifactStorageSettingsSectionProps = {
  settings: SiteSettings
}

function buildState(settings: SiteSettings): StorageState {
  const legacy: Partial<ArtifactStorageTarget> = {
    endpoint: settings['artifact_storage_setting.endpoint'],
    bucket: settings['artifact_storage_setting.bucket'],
    region: settings['artifact_storage_setting.region'],
    access_key: settings['artifact_storage_setting.access_key'],
    secret_key: settings['artifact_storage_setting.secret_key'],
    prefix: settings['artifact_storage_setting.prefix'],
    path_style: settings['artifact_storage_setting.path_style'],
  }
  return {
    enabled: settings['artifact_storage_setting.enabled'] ?? false,
    targets: parseArtifactStorageTargets(
      settings['artifact_storage_setting.targets'],
      legacy
    ),
    presignTTLSeconds:
      settings['artifact_storage_setting.presign_ttl_seconds'] ?? 900,
    maxObjectBytes: settings['artifact_storage_setting.max_object_bytes'] ?? 0,
    rewritePaths: parseRewritePaths(
      settings['artifact_storage_setting.rewrite_paths']
    ),
    fieldNames: parseRewritePaths(
      settings['artifact_storage_setting.field_names']
    ),
  }
}

/** Reads the stored JSON array into one selector per line. */
function parseRewritePaths(raw: string | undefined): string {
  if (!raw?.trim()) return ''
  try {
    const parsed: unknown = JSON.parse(raw)
    if (!Array.isArray(parsed)) return ''
    return parsed
      .filter((item): item is string => typeof item === 'string')
      .join('\n')
  } catch {
    return ''
  }
}

/** Splits the textarea into the array stored in the option. */
function serializeRewritePaths(raw: string): string[] {
  return raw
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean)
}

function serialize(state: StorageState): string {
  return JSON.stringify({
    enabled: state.enabled,
    targets: state.targets,
    presignTTLSeconds: state.presignTTLSeconds,
    maxObjectBytes: state.maxObjectBytes,
    rewritePaths: serializeRewritePaths(state.rewritePaths),
    fieldNames: serializeRewritePaths(state.fieldNames),
  })
}

function emptyTarget(provider: ArtifactStorageTarget['provider']): ArtifactStorageTarget {
  const defaults = providerDefaults(provider)
  return {
    provider,
    endpoint: '',
    bucket: '',
    region: defaults.regionHint,
    path_style: defaults.pathStyle,
  }
}

/**
 * Gateway-wide object storage, as an ordered fallback pool.
 *
 * Every destination speaks S3; the provider only decides addressing defaults
 * and the endpoint hint. Position is priority: a failed write moves to the next
 * entry, and the last resort is always the upstream proxy.
 */
export function ArtifactStorageSettingsSection({
  settings,
}: ArtifactStorageSettingsSectionProps) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()

  const initial = useMemo(() => buildState(settings), [settings])
  const [state, setState] = useState<StorageState>(initial)
  const [baseline, setBaseline] = useState<StorageState>(initial)

  useEffect(() => {
    setState(initial)
    setBaseline(initial)
  }, [initial])

  const dirty = useMemo(
    () => serialize(state) !== serialize(baseline),
    [state, baseline]
  )

  const updateTarget = (
    index: number,
    patch: Partial<ArtifactStorageTarget>
  ) => {
    setState((current) => ({
      ...current,
      targets: current.targets.map((target, position) =>
        position === index ? { ...target, ...patch } : target
      ),
    }))
  }

  const moveTarget = (index: number, delta: number) => {
    setState((current) => {
      const next = [...current.targets]
      const destination = index + delta
      if (destination < 0 || destination >= next.length) return current
      const [moved] = next.splice(index, 1)
      next.splice(destination, 0, moved)
      return { ...current, targets: next }
    })
  }

  const removeTarget = (index: number) => {
    setState((current) => ({
      ...current,
      targets: current.targets.filter((_, position) => position !== index),
    }))
  }

  const onSave = async () => {
    if (!dirty) {
      toast.info(t('No changes to save'))
      return
    }
    // A row without an endpoint or bucket is not a target: the backend skips
    // it, but it would still sit in the list looking configured. Dropping it
    // here keeps the stored JSON equal to what the operator can see.
    const usable = state.targets.filter(
      (target) => target.endpoint.trim() && target.bucket.trim()
    )
    if (state.enabled && usable.length === 0) {
      toast.error(t('Add a target with an endpoint and a bucket first'))
      return
    }
    const next: StorageState = { ...state, targets: usable }

    await updateOption.mutateAsync({
      key: 'artifact_storage_setting.enabled',
      value: next.enabled,
    })
    await updateOption.mutateAsync({
      key: 'artifact_storage_setting.targets',
      value: JSON.stringify(next.targets),
    })
    await updateOption.mutateAsync({
      key: 'artifact_storage_setting.presign_ttl_seconds',
      value: next.presignTTLSeconds,
    })
    await updateOption.mutateAsync({
      key: 'artifact_storage_setting.max_object_bytes',
      value: next.maxObjectBytes,
    })
    await updateOption.mutateAsync({
      key: 'artifact_storage_setting.rewrite_paths',
      value: JSON.stringify(serializeRewritePaths(next.rewritePaths)),
    })
    await updateOption.mutateAsync({
      key: 'artifact_storage_setting.field_names',
      value: JSON.stringify(serializeRewritePaths(next.fieldNames)),
    })
    setState(next)
    setBaseline(next)
  }

  const maxObjectMegabytes =
    state.maxObjectBytes > 0
      ? Math.round((state.maxObjectBytes / MEGABYTE) * 100) / 100
      : 0

  return (
    <SettingsSection title={t('Object storage')}>
      <SettingsPageFormActions
        onSave={onSave}
        isSaving={updateOption.isPending}
      />

      <div className='space-y-6'>
        <p className='text-muted-foreground text-xs'>
          {t(
            'Used only by channels that opt in to keeping their generated artifacts. A channel without its own endpoint inherits this pool; leave it disabled to keep proxying the upstream.'
          )}
        </p>

        <SettingsFormGrid>
          <SettingsSwitchField
            controlId='artifact-storage-enabled'
            checked={state.enabled}
            onCheckedChange={(checked) =>
              setState((current) => ({ ...current, enabled: checked }))
            }
            label={t('Enable object storage')}
            description={t(
              'S3-compatible targets, including MinIO, Alibaba Cloud OSS, Tencent Cloud COS and Volcengine TOS. Nothing is uploaded until a channel opts in.'
            )}
          />

          <SettingsFormGridItem span='full'>
            <div className='space-y-2'>
              <Label htmlFor='artifact-storage-field-names'>
                {t('Built-in field names')}
              </Label>
              <Textarea
                id='artifact-storage-field-names'
                rows={3}
                className='font-mono text-xs'
                spellCheck={false}
                placeholder={'url\nimage_url\naudio_url\nvideo_url\ndata'}
                value={state.fieldNames}
                disabled={!state.enabled}
                onChange={(event) =>
                  setState((current) => ({
                    ...current,
                    fieldNames: event.target.value,
                  }))
                }
              />
              <p className='text-muted-foreground text-xs'>
                {t(
                  'One field name per line, used only when no selector below is configured. Leave empty for the default: url, image_url, audio_url, video_url, file_url, b64, base64, data, audio, audio_data.'
                )}
              </p>
            </div>
          </SettingsFormGridItem>

          <SettingsFormGridItem span='full'>
            <div className='space-y-2'>
              <Label htmlFor='artifact-storage-rewrite-paths'>
                {t('Custom match paths')}
              </Label>
              <Textarea
                id='artifact-storage-rewrite-paths'
                rows={4}
                className='font-mono text-xs'
                spellCheck={false}
                placeholder={'$.data[*].url\n$.candidates[*].content.parts[*].inlineData.data\n$.content[*].source.data'}
                value={state.rewritePaths}
                disabled={!state.enabled}
                onChange={(event) =>
                  setState((current) => ({
                    ...current,
                    rewritePaths: event.target.value,
                  }))
                }
              />
              <div className='text-muted-foreground space-y-1 text-xs'>
                <p>
                  {t(
                    'One selector per line. A channel that declares its own selectors replaces this list; leave it empty to use the built-in field names above.'
                  )}
                </p>
                <p>
                  {t(
                    'Syntax: $ is the root, .field picks an object key, [*] or .* matches every child, [0] or .0 picks a fixed index.'
                  )}
                </p>
                <p>
                  {t(
                    'Append => field to write the stored address into another field, and => field clear to blank the original. Use it for payload fields that must keep their own contract.'
                  )}
                </p>
                <p className='font-mono text-[11px]'>
                  $.data[*].url · OpenAI images &nbsp;|&nbsp;
                  $.candidates[*].content.parts[*].inlineData.data · Gemini
                  &nbsp;|&nbsp; $.data[*].b64_json =&gt; url clear · inline
                  payload
                </p>
              </div>
            </div>
          </SettingsFormGridItem>

          <SettingsFormGridItem>
            <div className='space-y-2'>
              <Label htmlFor='artifact-storage-presign-ttl'>
                {t('Signed URL lifetime (seconds)')}
              </Label>
              <Input
                id='artifact-storage-presign-ttl'
                type='number'
                min={1}
                max={604800}
                value={String(state.presignTTLSeconds)}
                disabled={!state.enabled}
                onChange={(event) =>
                  setState((current) => ({
                    ...current,
                    presignTTLSeconds: Math.max(
                      1,
                      Math.min(604800, Number(event.target.value) || 900)
                    ),
                  }))
                }
              />
            </div>
          </SettingsFormGridItem>

          <SettingsFormGridItem>
            <div className='space-y-2'>
              <Label htmlFor='artifact-storage-max-object'>
                {t('Max object size (MB)')}
              </Label>
              <Input
                id='artifact-storage-max-object'
                type='number'
                min={0}
                value={maxObjectMegabytes > 0 ? String(maxObjectMegabytes) : ''}
                disabled={!state.enabled}
                onChange={(event) => {
                  const megabytes = Number(event.target.value)
                  setState((current) => ({
                    ...current,
                    maxObjectBytes:
                      Number.isFinite(megabytes) && megabytes > 0
                        ? Math.round(megabytes * MEGABYTE)
                        : 0,
                  }))
                }}
              />
              <p className='text-muted-foreground text-xs'>
                {t('Leave empty to use the gateway download limit.')}
              </p>
            </div>
          </SettingsFormGridItem>
        </SettingsFormGrid>

        <div className='space-y-3'>
          <div className='flex items-end justify-between gap-4'>
            <div className='space-y-0.5'>
              <Label>{t('Fallback order')}</Label>
              <p className='text-muted-foreground text-xs'>
                {t(
                  'Writes are tried from top to bottom. A target that fails cools down for a short while and is skipped, so one dead store cannot slow every request down.'
                )}
              </p>
            </div>
            <Button
              type='button'
              size='sm'
              variant='outline'
              disabled={!state.enabled}
              onClick={() =>
                setState((current) => ({
                  ...current,
                  targets: [...current.targets, emptyTarget('minio')],
                }))
              }
            >
              <Plus data-icon='inline-start' />
              <span>{t('Add target')}</span>
            </Button>
          </div>

          {state.targets.length === 0 ? (
            <p className='text-muted-foreground rounded-lg border border-dashed p-4 text-sm'>
              {t(
                'No target configured. A channel that opts in will keep proxying the upstream until one is added.'
              )}
            </p>
          ) : null}

          {state.targets.map((target, index) => {
            const defaults = providerDefaults(target.provider)
            const pathStyle = target.path_style ?? defaults.pathStyle
            return (
              <div
                key={index}
                role='group'
                aria-label={t('Storage target')}
                className='border-border/60 space-y-4 rounded-lg border p-4'
              >
                <div className='flex items-center justify-between gap-2'>
                  <div className='flex items-center gap-2'>
                    <span className='bg-muted text-muted-foreground flex size-6 items-center justify-center rounded-full text-xs font-semibold'>
                      {index + 1}
                    </span>
                    <span className='text-sm font-medium'>
                      {t('Storage target')}
                    </span>
                  </div>
                  <div className='flex items-center gap-1'>
                    <Button
                      type='button'
                      size='icon'
                      variant='ghost'
                      aria-label={t('Move up')}
                      disabled={!state.enabled || index === 0}
                      onClick={() => moveTarget(index, -1)}
                    >
                      <ArrowUp className='size-4' />
                    </Button>
                    <Button
                      type='button'
                      size='icon'
                      variant='ghost'
                      aria-label={t('Move down')}
                      disabled={
                        !state.enabled || index === state.targets.length - 1
                      }
                      onClick={() => moveTarget(index, 1)}
                    >
                      <ArrowDown className='size-4' />
                    </Button>
                    <Button
                      type='button'
                      size='icon'
                      variant='ghost'
                      aria-label={t('Remove')}
                      disabled={!state.enabled}
                      onClick={() => removeTarget(index)}
                    >
                      <Trash2 className='size-4' />
                    </Button>
                  </div>
                </div>

                <div className='grid grid-cols-1 gap-4 sm:grid-cols-2'>
                  <div className='space-y-2'>
                    <Label>{t('Provider')}</Label>
                    <Select
                      items={ARTIFACT_STORAGE_PROVIDERS.map((provider) => ({
                        value: provider.value,
                        label: provider.label,
                      }))}
                      value={target.provider ?? 's3'}
                      disabled={!state.enabled}
                      onValueChange={(value) => {
                        const next = providerDefaults(value ?? undefined)
                        updateTarget(index, {
                          provider: next.value,
                          // Follow the provider's addressing style unless the
                          // operator already typed an endpoint of their own.
                          path_style: next.pathStyle,
                          region: target.region || next.regionHint,
                        })
                      }}
                    >
                      <SelectTrigger>
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent alignItemWithTrigger={false}>
                        <SelectGroup>
                          {ARTIFACT_STORAGE_PROVIDERS.map((provider) => (
                            <SelectItem
                              key={provider.value}
                              value={provider.value}
                            >
                              {provider.label}
                            </SelectItem>
                          ))}
                        </SelectGroup>
                      </SelectContent>
                    </Select>
                  </div>

                  <div className='space-y-2'>
                    <Label>{t('Endpoint')}</Label>
                    <Input
                      placeholder={defaults.endpointHint}
                      value={target.endpoint}
                      disabled={!state.enabled}
                      onChange={(event) =>
                        updateTarget(index, { endpoint: event.target.value })
                      }
                    />
                  </div>

                  <div className='space-y-2'>
                    <Label>{t('Bucket')}</Label>
                    <Input
                      value={target.bucket}
                      disabled={!state.enabled}
                      onChange={(event) =>
                        updateTarget(index, { bucket: event.target.value })
                      }
                    />
                  </div>

                  <div className='space-y-2'>
                    <Label>{t('Region')}</Label>
                    <Input
                      placeholder={defaults.regionHint}
                      value={target.region ?? ''}
                      disabled={!state.enabled}
                      onChange={(event) =>
                        updateTarget(index, { region: event.target.value })
                      }
                    />
                  </div>

                  <div className='space-y-2'>
                    <Label>{t('Access key')}</Label>
                    <Input
                      autoComplete='off'
                      value={target.access_key ?? ''}
                      disabled={!state.enabled}
                      onChange={(event) =>
                        updateTarget(index, { access_key: event.target.value })
                      }
                    />
                  </div>

                  <div className='space-y-2'>
                    <Label>{t('Secret key')}</Label>
                    <Input
                      type='password'
                      autoComplete='new-password'
                      value={target.secret_key ?? ''}
                      disabled={!state.enabled}
                      onChange={(event) =>
                        updateTarget(index, { secret_key: event.target.value })
                      }
                    />
                  </div>

                  <div className='space-y-2'>
                    <Label>{t('Key prefix')}</Label>
                    <Input
                      placeholder='artifacts'
                      value={target.prefix ?? ''}
                      disabled={!state.enabled}
                      onChange={(event) =>
                        updateTarget(index, { prefix: event.target.value })
                      }
                    />
                  </div>

                  <div className='flex items-center justify-between gap-4 pt-1'>
                    <div className='space-y-0.5'>
                      <Label className='text-sm'>
                        {t('Path-style addressing')}
                      </Label>
                      <p className='text-muted-foreground text-xs'>
                        {t('Required by MinIO; turn it off for cloud stores.')}
                      </p>
                    </div>
                    <Button
                      type='button'
                      size='sm'
                      variant={pathStyle ? 'default' : 'outline'}
                      disabled={!state.enabled}
                      onClick={() =>
                        updateTarget(index, { path_style: !pathStyle })
                      }
                    >
                      {pathStyle ? t('On') : t('Off')}
                    </Button>
                  </div>
                </div>
              </div>
            )
          })}
        </div>
      </div>
    </SettingsSection>
  )
}
