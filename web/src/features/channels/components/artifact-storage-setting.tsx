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
import { useState } from 'react'
import { useFormContext } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { HardDrive } from 'lucide-react'

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
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import {
  ARTIFACT_STORAGE_PROVIDERS,
  providerDefaults,
} from '@/features/system-settings/artifact-storage-types'
import { apiduoBlockMark } from '@/features/apiduo-marks/marks'

import type { ChannelFormValues } from '../lib/channel-form'

/**
 * Per-channel object storage settings.
 *
 * Three states, and the middle one is the default once the switch is on:
 * disabled means "keep proxying the upstream", enabled without an endpoint
 * means "inherit the gateway-wide storage", and an endpoint switches the channel
 * to its own target. The endpoint is therefore the override switch, not a
 * separate checkbox - two controls that must agree are two controls that can
 * disagree.
 */
export type ChannelArtifactStorageConfig = {
  enabled?: boolean
  provider?: string
  endpoint?: string
  bucket?: string
  region?: string
  access_key?: string
  secret_key?: string
  prefix?: string
  path_style?: boolean
  /**
   * Selectors for this channel's responses. They replace the gateway-wide list
   * rather than extend it, because a provider body shape is all-or-nothing.
   */
  rewrite_paths?: string[]
}

const EMPTY_CONFIG: ChannelArtifactStorageConfig = {}

function parseSettingsObject(raw: unknown): Record<string, unknown> {
  if (typeof raw !== 'string' || !raw.trim()) return {}
  try {
    const parsed: unknown = JSON.parse(raw)
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
      return parsed as Record<string, unknown>
    }
  } catch {
    // An unreadable blob is treated as empty; the rest of the form overwrites
    // it on save anyway.
  }
  return {}
}

function readArtifactStorageConfig(
  settings: Record<string, unknown>
): ChannelArtifactStorageConfig {
  const raw = settings.artifact_storage
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return EMPTY_CONFIG
  return raw as ChannelArtifactStorageConfig
}

/** Drops empty values so the stored JSON only states real decisions. */
function pruneConfig(
  config: ChannelArtifactStorageConfig
): ChannelArtifactStorageConfig | undefined {
  if (!config.enabled) return undefined
  const pruned: ChannelArtifactStorageConfig = { enabled: true }
  if (config.provider?.trim()) pruned.provider = config.provider.trim()
  if (config.endpoint?.trim()) pruned.endpoint = config.endpoint.trim()
  if (config.bucket?.trim()) pruned.bucket = config.bucket.trim()
  if (config.region?.trim()) pruned.region = config.region.trim()
  if (config.access_key?.trim()) pruned.access_key = config.access_key.trim()
  if (config.secret_key?.trim()) pruned.secret_key = config.secret_key.trim()
  if (config.prefix?.trim()) pruned.prefix = config.prefix.trim()
  if (config.path_style === false) pruned.path_style = false
  if (config.rewrite_paths?.length) {
    pruned.rewrite_paths = config.rewrite_paths
      .map((line) => line.trim())
      .filter(Boolean)
  }
  return pruned
}

export function ArtifactStorageSetting(props: { disabled?: boolean }) {
  const { t } = useTranslation()
  const form = useFormContext<ChannelFormValues>()
  const settings = parseSettingsObject(form.watch('settings'))
  const config = readArtifactStorageConfig(settings)
  const enabled = config.enabled === true
  const [override, setOverride] = useState(() => Boolean(config.endpoint?.trim()))

  const write = (next: ChannelArtifactStorageConfig) => {
    const pruned = pruneConfig(next)
    const merged = { ...settings }
    if (pruned) {
      merged.artifact_storage = pruned
    } else {
      delete merged.artifact_storage
    }
    form.setValue('settings', JSON.stringify(merged), {
      shouldDirty: true,
      shouldTouch: true,
    })
  }

  const patch = (partial: ChannelArtifactStorageConfig) => {
    write({ ...config, ...partial })
  }

  return (
    <div className='space-y-5'>
      <div className='flex items-start justify-between gap-4'>
        <div className='space-y-0.5'>
          <Label htmlFor='channel-artifact-storage' className='text-sm'>
            {t('Keep generated artifacts')}
          </Label>
          <p className='text-muted-foreground text-xs'>
            {t(
              'Copies this channel\u2019s artifacts into object storage as soon as a task finishes. Off keeps the upstream link only.'
            )}
          </p>
        </div>
        <Switch
          id='channel-artifact-storage'
          checked={enabled}
          disabled={props.disabled}
          onCheckedChange={(checked) => {
            setOverride(false)
            write(checked ? { enabled: true } : {})
          }}
        />
      </div>

      {enabled ? (
        <>
          <div className='flex items-start justify-between gap-4'>
            <div className='space-y-0.5'>
              <Label
                htmlFor='channel-artifact-storage-override'
                className='text-sm'
              >
                {t('Use this channel\u2019s own storage')}
              </Label>
              <p className='text-muted-foreground text-xs'>
                {override
                  ? t('This channel ignores the gateway-wide target.')
                  : t(
                      'Inherits the endpoint, bucket, and credentials from the gateway-wide object storage settings.'
                    )}
              </p>
            </div>
            <Switch
              id='channel-artifact-storage-override'
              checked={override}
              disabled={props.disabled}
              onCheckedChange={(checked) => {
                setOverride(checked)
                if (!checked) {
                  patch({
                    endpoint: '',
                    bucket: '',
                    region: '',
                    access_key: '',
                    secret_key: '',
                    prefix: '',
                    path_style: undefined,
                  })
                }
              }}
            />
          </div>

          {override ? (
            <div className='grid grid-cols-1 gap-4 pt-1 sm:grid-cols-2'>
              <div className='space-y-2'>
                <Label>{t('Provider')}</Label>
                <Select
                  items={ARTIFACT_STORAGE_PROVIDERS.map((provider) => ({
                    value: provider.value,
                    label: provider.label,
                  }))}
                  value={config.provider ?? 's3'}
                  disabled={props.disabled}
                  onValueChange={(value) => {
                    const next = providerDefaults(value ?? undefined)
                    patch({
                      provider: next.value,
                      // Follow the provider's addressing style; MinIO needs
                      // path style while every cloud store rejects it.
                      path_style: next.pathStyle,
                      region: config.region || next.regionHint,
                    })
                  }}
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent alignItemWithTrigger={false}>
                    <SelectGroup>
                      {ARTIFACT_STORAGE_PROVIDERS.map((provider) => (
                        <SelectItem key={provider.value} value={provider.value}>
                          {provider.label}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  </SelectContent>
                </Select>
              </div>
              <div className='space-y-2'>
                <Label htmlFor='channel-artifact-endpoint'>
                  {t('Endpoint')}
                </Label>
                <Input
                  id='channel-artifact-endpoint'
                  placeholder='http://minio:9000'
                  disabled={props.disabled}
                  value={config.endpoint ?? ''}
                  onChange={(event) => patch({ endpoint: event.target.value })}
                />
              </div>
              <div className='space-y-2'>
                <Label htmlFor='channel-artifact-bucket'>{t('Bucket')}</Label>
                <Input
                  id='channel-artifact-bucket'
                  disabled={props.disabled}
                  value={config.bucket ?? ''}
                  onChange={(event) => patch({ bucket: event.target.value })}
                />
              </div>
              <div className='space-y-2'>
                <Label htmlFor='channel-artifact-region'>{t('Region')}</Label>
                <Input
                  id='channel-artifact-region'
                  placeholder='us-east-1'
                  disabled={props.disabled}
                  value={config.region ?? ''}
                  onChange={(event) => patch({ region: event.target.value })}
                />
              </div>
              <div className='space-y-2'>
                <Label htmlFor='channel-artifact-prefix'>
                  {t('Key prefix')}
                </Label>
                <Input
                  id='channel-artifact-prefix'
                  disabled={props.disabled}
                  value={config.prefix ?? ''}
                  onChange={(event) => patch({ prefix: event.target.value })}
                />
              </div>
              <div className='space-y-2'>
                <Label htmlFor='channel-artifact-access-key'>
                  {t('Access key')}
                </Label>
                <Input
                  id='channel-artifact-access-key'
                  autoComplete='off'
                  disabled={props.disabled}
                  value={config.access_key ?? ''}
                  onChange={(event) =>
                    patch({ access_key: event.target.value })
                  }
                />
              </div>
              <div className='space-y-2'>
                <Label htmlFor='channel-artifact-secret-key'>
                  {t('Secret key')}
                </Label>
                <Input
                  id='channel-artifact-secret-key'
                  type='password'
                  autoComplete='new-password'
                  disabled={props.disabled}
                  value={config.secret_key ?? ''}
                  onChange={(event) =>
                    patch({ secret_key: event.target.value })
                  }
                />
                <p className='text-muted-foreground text-xs'>
                  {t(
                    'Leave blank to reuse the gateway-wide credentials for this endpoint.'
                  )}
                </p>
              </div>

              <div className='space-y-2 sm:col-span-2'>
                <Label htmlFor='channel-artifact-rewrite-paths'>
                  {t('Match paths')}
                </Label>
                <Textarea
                  id='channel-artifact-rewrite-paths'
                  rows={3}
                  className='font-mono text-xs'
                  spellCheck={false}
                  placeholder={'$.data[*].url'}
                  value={(config.rewrite_paths ?? []).join('\n')}
                  disabled={props.disabled}
                  onChange={(event) =>
                    patch({ rewrite_paths: event.target.value.split('\n') })
                  }
                />
                <p className='text-muted-foreground text-xs'>
                  {t(
                    'One selector per line. Setting any here replaces the gateway default for this channel; leave empty to inherit it.'
                  )}
                </p>
              </div>
            </div>
          ) : null}
        </>
      ) : null}
    </div>
  )
}

/**
 * Card wrapper for the channel drawer.
 *
 * It exists so the drawer gains four lines instead of a whole block: that file
 * is one of the most frequently edited upstream files, and every line added
 * there is a future merge conflict. The heading is rendered here rather than
 * passed in because the drawer's CardHeading is private to it.
 */
export function ArtifactStorageBlock(props: {
  className?: string
  locked?: boolean
  disabled?: boolean
}) {
  const { t } = useTranslation()
  return (
    <div
      role='group'
      aria-label={t('Object storage')}
      data-apiduo={apiduoBlockMark('channel:artifact-storage')}
      className={props.className}
    >
      <div className='flex items-center gap-3'>
        <HardDrive className='text-muted-foreground size-4' />
        <h3 className='min-w-0 flex-1 text-sm font-semibold tracking-tight'>
          {t('Object storage')}
        </h3>
      </div>
      <fieldset disabled={props.locked} className='space-y-4 disabled:opacity-60'>
        <ArtifactStorageSetting disabled={props.disabled} />
      </fieldset>
    </div>
  )
}
