import { ArrowRight, Plus, Trash2 } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { apiduoBlockMark } from '@/features/apiduo-marks/marks'
import { ImageProtocolSection } from './image-protocol-section'
import type { ImageProtocolSettings } from '@/features/channels/lib/image-protocol-settings'
import {
  ADVANCED_CUSTOM_INCOMING_PATH_OPTIONS,
  getAdvancedCustomConverterOptions,
  getAdvancedCustomIncomingPathLabel,
} from '@/features/channels/lib/advanced-custom'
import {
  type RoutePlanWarning,
  addDerivation,
  applyPrimaryEndpoint,
  buildDerivations,
  patchDerivation,
  previewRoutePlan,
  removeDerivation,
  resolvePrimaryEndpoint,
} from '@/features/channels/lib/primary-endpoint'
import { converterLabel } from '@/features/protocol-matrix/lib/mode-label'
import type { AdvancedCustomRoute } from '@/features/channels/types'

const WARNING_TEXT: Record<RoutePlanWarning, string> = {
  'no-route': 'No route matches this endpoint - the request is rejected.',
  'catch-all': 'Matched by the catch-all route, not an exact one.',
  'edits-passthrough-native':
    'Multipart is forwarded unchanged to the edits endpoint.',
  'edits-rebuilt-to-json': 'The form is rebuilt into JSON before forwarding.',
  'edits-passthrough-to-json-upstream':
    'Raw multipart would be sent to an endpoint that only accepts JSON.',
  'chat-to-image':
    'Chat is not converted into an image task without the image protocol.',
  converter: 'A converter rewrites the request body.',
}

const WARNING_STYLE: Partial<Record<RoutePlanWarning, string>> = {
  'no-route': 'text-destructive',
  'edits-passthrough-to-json-upstream': 'text-destructive',
  'chat-to-image': 'text-amber-600 dark:text-amber-400',
}

/**
 * 主端点 + 派生端点。
 *
 * 这里只做「读」和「改结构」：真正的路由存储仍是 `advanced_routes`，
 * 派生行是它的投影。换主端点时只改跟随主端点的那些行，手写的上游路径保留。
 */
export function PrimaryEndpointEditor(props: {
  routes: AdvancedCustomRoute[]
  models?: string
  /** 渠道声明的主端点（settings.primary_endpoint）；空表示从路由推导。 */
  primaryEndpoint?: string
  onChange: (routes: AdvancedCustomRoute[]) => void
  onPrimaryEndpointChange?: (primary: string) => void
  /** settings.image_protocol；不传就不渲染协议区块（其它调用方保持原样）。 */
  imageProtocol?: ImageProtocolSettings | null
  onImageProtocolChange?: (next: ImageProtocolSettings | null) => void
}) {
  const { t } = useTranslation()
  const primary = useMemo(
    () => resolvePrimaryEndpoint(props.routes, props.primaryEndpoint),
    [props.routes, props.primaryEndpoint]
  )
  const derivations = useMemo(
    () => buildDerivations(props.routes, primary),
    [props.routes, primary]
  )
  const modelList = useMemo(
    () =>
      (props.models || '')
        .split(',')
        .map((model) => model.trim())
        .filter(Boolean),
    [props.models]
  )

  const hubOptions = useMemo(() => {
    const paths = new Set<string>()
    for (const option of ADVANCED_CUSTOM_INCOMING_PATH_OPTIONS) {
      paths.add(option.value)
    }
    for (const row of derivations) paths.add(row.upstreamPath)
    if (primary) paths.add(primary)
    return [...paths]
  }, [derivations, primary])

  const availablePaths = useMemo(
    () =>
      ADVANCED_CUSTOM_INCOMING_PATH_OPTIONS.filter(
        (option) =>
          !derivations.some((row) => row.incomingPath === option.value)
      ),
    [derivations]
  )

  const [pendingPath, setPendingPath] = useState('')
  const [previewIncoming, setPreviewIncoming] = useState('')
  const [previewModel, setPreviewModel] = useState('')

  const plan = useMemo(
    () =>
      previewIncoming
        ? previewRoutePlan(props.routes, {
            incomingPath: previewIncoming,
            model: previewModel,
          })
        : null,
    [props.routes, previewIncoming, previewModel]
  )

  return (
    <div
      className='flex flex-col gap-4'
      data-apiduo={apiduoBlockMark('channel:primary-endpoint')}
    >
      <section className='rounded-lg border p-4'>
        <div className='mb-2 flex items-center gap-2'>
          <p className='text-sm font-medium'>{t('Primary endpoint')}</p>
          <Badge variant='outline'>
            {t('{{count}} derived', { count: derivations.length })}
          </Badge>
        </div>
        <p className='text-muted-foreground mb-3 text-xs'>
          {t(
            'The endpoint the upstream actually speaks. Every other endpoint is derived from it.'
          )}
        </p>
        {props.primaryEndpoint ? (
          <p className='text-muted-foreground mb-3 text-xs'>
            {t(
              'Stored as primary_endpoint: derived routes may leave their upstream path empty.'
            )}
          </p>
        ) : null}
        <Select
          value={primary}
          onValueChange={(value) => {
            if (typeof value === 'string') {
              // 页面支持声明式 primary 时，派生路由落库成空值（空 = 跟随主端点）。
              props.onChange(
                applyPrimaryEndpoint(props.routes, value, {
                  deriveToEmpty: Boolean(props.onPrimaryEndpointChange),
                })
              )
              props.onPrimaryEndpointChange?.(value)
            }
          }}
        >
          <SelectTrigger className='w-full'>
            {primary ? (
              <span className='flex min-w-0 items-baseline gap-2'>
                <span className='truncate'>
                  {t(getAdvancedCustomIncomingPathLabel(primary))}
                </span>
                {/* 未知端点的「友好名」就是路径本身，别把同一串显示两遍。 */}
                {t(getAdvancedCustomIncomingPathLabel(primary)) === primary ? null : (
                  <span className='text-muted-foreground truncate font-mono text-xs'>
                    {primary}
                  </span>
                )}
              </span>
            ) : (
              <SelectValue placeholder={t('Choose the primary endpoint')} />
            )}
          </SelectTrigger>
          <SelectContent alignItemWithTrigger={false}>
            <SelectGroup>
              {hubOptions.map((path) => (
                <SelectItem key={path} value={path}>
                  <span>{t(getAdvancedCustomIncomingPathLabel(path))}</span>
                  {t(getAdvancedCustomIncomingPathLabel(path)) === path ? null : (
                    <span className='text-muted-foreground ml-2 font-mono text-xs'>
                      {path}
                    </span>
                  )}
                </SelectItem>
              ))}
            </SelectGroup>
          </SelectContent>
        </Select>
      </section>

      {props.onImageProtocolChange ? (
        <ImageProtocolSection
          config={props.imageProtocol ?? null}
          primaryEndpoint={primary}
          onChange={props.onImageProtocolChange}
        />
      ) : null}

      <section className='rounded-lg border'>
        <div className='flex items-center justify-between border-b px-4 py-3'>
          <p className='text-sm font-medium'>{t('Derived endpoints')}</p>
          <div className='flex items-center gap-2'>
            <Select
              value={pendingPath}
              onValueChange={(value) => {
                if (typeof value === 'string') setPendingPath(value)
              }}
            >
              <SelectTrigger size='sm'>
                <SelectValue placeholder={t('Add derived endpoint')} />
              </SelectTrigger>
              <SelectContent align='end' alignItemWithTrigger={false}>
                <SelectGroup>
                  {availablePaths.map((option) => (
                    <SelectItem key={option.value} value={option.value}>
                      {t(option.label)}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
            <Button
              type='button'
              variant='outline'
              size='sm'
              disabled={!pendingPath}
              onClick={() => {
                if (!pendingPath) return
                props.onChange(addDerivation(props.routes, pendingPath, primary))
                setPendingPath('')
              }}
            >
              <Plus data-icon='inline-start' />
              {t('Add')}
            </Button>
          </div>
        </div>

        {derivations.length === 0 ? (
          <p className='text-muted-foreground px-4 py-6 text-sm'>
            {t('No derived endpoints yet.')}
          </p>
        ) : (
          <div className='divide-y'>
            {derivations.map((row) => (
              <div
                key={row.incomingPath}
                className='grid grid-cols-1 items-center gap-3 px-4 py-3 lg:grid-cols-[minmax(0,1.1fr)_minmax(0,1.1fr)_minmax(0,0.9fr)_auto_2.5rem]'
              >
                <div className='min-w-0'>
                  <p className='truncate text-sm'>
                    {t(getAdvancedCustomIncomingPathLabel(row.incomingPath))}
                  </p>
                  <p className='text-muted-foreground truncate font-mono text-xs'>
                    {row.incomingPath}
                  </p>
                </div>

                <div className='flex min-w-0 items-center gap-2 text-xs'>
                  {row.followsPrimary ? (
                    <>
                      <Badge variant='outline' className='shrink-0'>
                        {t('Follows primary')}
                      </Badge>
                      <span className='text-muted-foreground truncate font-mono'>
                        {row.upstreamPath}
                      </span>
                    </>
                  ) : (
                    <>
                      <ArrowRight className='text-muted-foreground size-3.5 shrink-0' />
                      <span className='truncate font-mono'>
                        {row.upstreamPath}
                      </span>
                    </>
                  )}
                </div>

                <Select
                  value={row.converter}
                  onValueChange={(value) => {
                    if (typeof value === 'string') {
                      props.onChange(
                        patchDerivation(props.routes, row.incomingPath, {
                          converter: value,
                          passThrough: value === 'none' ? row.passThrough : false,
                        })
                      )
                    }
                  }}
                >
                  <SelectTrigger size='sm'>
                    <span className='truncate'>
                      {t(converterLabel(row.converter))}
                    </span>
                  </SelectTrigger>
                  <SelectContent alignItemWithTrigger={false}>
                    <SelectGroup>
                      {getAdvancedCustomConverterOptions(row.incomingPath).map(
                        (option) => (
                          <SelectItem key={option.value} value={option.value}>
                            {t(option.label)}
                          </SelectItem>
                        )
                      )}
                    </SelectGroup>
                  </SelectContent>
                </Select>

                <label className='flex items-center gap-2 text-xs'>
                  <Switch
                    checked={row.passThrough}
                    disabled={row.converter !== 'none'}
                    onCheckedChange={(checked) =>
                      props.onChange(
                        patchDerivation(props.routes, row.incomingPath, {
                          passThrough: checked,
                        })
                      )
                    }
                  />
                  {t('Pass through')}
                </label>

                <Button
                  type='button'
                  variant='ghost'
                  size='icon'
                  aria-label={t('Remove derived endpoint')}
                  onClick={() =>
                    props.onChange(
                      removeDerivation(props.routes, row.incomingPath)
                    )
                  }
                >
                  <Trash2 className='size-4' />
                </Button>
              </div>
            ))}
          </div>
        )}
      </section>

      <section className='rounded-lg border p-4'>
        <p className='mb-3 text-sm font-medium'>{t('Preview')}</p>
        <div className='grid grid-cols-1 gap-3 sm:grid-cols-2'>
          <Select
            value={previewIncoming}
            onValueChange={(value) => {
              if (typeof value === 'string') setPreviewIncoming(value)
            }}
          >
            <SelectTrigger size='sm'>
              <SelectValue placeholder={t('Incoming endpoint')} />
            </SelectTrigger>
            <SelectContent alignItemWithTrigger={false}>
              <SelectGroup>
                {ADVANCED_CUSTOM_INCOMING_PATH_OPTIONS.map((option) => (
                  <SelectItem key={option.value} value={option.value}>
                    {t(option.label)}
                  </SelectItem>
                ))}
              </SelectGroup>
            </SelectContent>
          </Select>
          <Select
            value={previewModel}
            onValueChange={(value) => {
              setPreviewModel(typeof value === 'string' ? value : '')
            }}
          >
            <SelectTrigger size='sm'>
              <SelectValue placeholder={t('Any model')} />
            </SelectTrigger>
            <SelectContent alignItemWithTrigger={false}>
              <SelectGroup>
                <SelectItem value=''>{t('Any model')}</SelectItem>
                {modelList.map((model) => (
                  <SelectItem key={model} value={model}>
                    {model}
                  </SelectItem>
                ))}
              </SelectGroup>
            </SelectContent>
          </Select>
        </div>

        {plan ? (
          <div className='bg-muted/40 mt-3 rounded-md px-3 py-2 text-xs'>
            {plan.matched ? (
              <p className='font-mono'>
                {plan.incomingPath || '*'} <ArrowRight className='inline size-3' />{' '}
                {plan.upstreamPath}
              </p>
            ) : (
              <p className='text-destructive'>{t('No matching route')}</p>
            )}
            <p className='text-muted-foreground mt-1'>
              {plan.carrier === 'multipart'
                ? t('Carrier: multipart file stream')
                : t('Carrier: JSON')}
              {' · '}
              {plan.passThrough ? t('Pass through') : t('Rebuilt')}
              {plan.converter !== 'none' ? ` · ${plan.converter}` : ''}
            </p>
            {plan.warnings.map((warning) => (
              <p
                key={warning}
                className={WARNING_STYLE[warning] ?? 'text-muted-foreground'}
              >
                {t(WARNING_TEXT[warning])}
              </p>
            ))}
          </div>
        ) : null}
      </section>
    </div>
  )
}
