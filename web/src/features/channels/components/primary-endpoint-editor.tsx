import { ArrowRight, Plus, Trash2 } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { Input } from '@/components/ui/input'
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
import {
  ADVANCED_CUSTOM_INCOMING_PATH_OPTIONS,
  getAdvancedCustomConverterOptions,
  getAdvancedCustomIncomingPathLabel,
} from '@/features/channels/lib/advanced-custom'
import {
  type ImageProtocolSettings,
  IMAGE_PROTOCOL_UPSTREAMS,
} from '@/features/channels/lib/image-protocol-settings'
import {
  addDerivation,
  applyPrimaryEndpoint,
  buildDerivations,
  looksLikeImagePath,
  patchDerivation,
  removeDerivation,
  resolvePrimaryEndpoint,
  seedPrimaryRoute,
} from '@/features/channels/lib/primary-endpoint'
import type { AdvancedCustomRoute } from '@/features/channels/types'
import { converterLabel } from '@/features/protocol-matrix/lib/mode-label'

const ENTRY_PREVIEW = [
  { role: 'Text to image', path: '/v1/images/generations' },
  { role: 'Image to image', path: '/v1/images/edits' },
  { role: 'Chat', path: '/v1/chat/completions' },
  { role: 'Responses', path: '/v1/responses' },
]

/**
 * 主端点：一个渠道对上游只讲一种「母语」。
 *
 * 开关默认关闭——关着时这一页与 new-api 完全一致：四个入口各按 `advanced_routes`
 * 里的上游转发，报文原样，既不折叠也不改写。打开之后才声明 `primary_endpoint`，
 * 空 upstream 的路由跟随它，图片入口折叠成一份 canonical 请求。
 *
 * 这样上游同步永远不冲突：新版本合进来，关着的渠道行为一字不变。
 */
export function PrimaryEndpointEditor(props: {
  routes: AdvancedCustomRoute[]
  models?: string
  primaryEndpoint?: string
  onChange: (routes: AdvancedCustomRoute[]) => void
  onPrimaryEndpointChange?: (primary: string) => void
  imageProtocol?: ImageProtocolSettings | null
  onImageProtocolChange?: (next: ImageProtocolSettings | null) => void
}) {
  const { t } = useTranslation()
  const declared = (props.primaryEndpoint || '').trim()
  // 输入框允许清空后再填：清空时不能让整块（含开关）自己收起，
  // 否则用户删一个字，页面就跳回关闭态。
  const [keepOpen, setKeepOpen] = useState(false)
  const protocolOn = props.imageProtocol?.enabled === true
  const enabled = declared !== '' || protocolOn || keepOpen
  // 下拉触发器要显示可读标签，不能把存储值（seedream）直接摆在页面上：
  // base-ui 的 SelectValue 在选项是延迟挂载时拿不到 label，会退回原始 value。
  const upstreamLayout =
    props.imageProtocol?.upstream || IMAGE_PROTOCOL_UPSTREAMS[1].value
  const upstreamLayoutLabel =
    IMAGE_PROTOCOL_UPSTREAMS.find((option) => option.value === upstreamLayout)
      ?.label || upstreamLayout

  const primary = useMemo(
    () => (enabled ? declared : resolvePrimaryEndpoint(props.routes)),
    [declared, enabled, props.routes]
  )
  const derivations = useMemo(
    () => buildDerivations(props.routes, primary),
    [props.routes, primary]
  )

  const [pendingPath, setPendingPath] = useState('')
  const [advancedOpen, setAdvancedOpen] = useState(false)

  const availablePaths = useMemo(
    () =>
      ADVANCED_CUSTOM_INCOMING_PATH_OPTIONS.filter(
        (option) => !derivations.some((row) => row.incomingPath === option.value)
      ),
    [derivations]
  )

  const setEnabled = (next: boolean) => {
    setKeepOpen(next)
    if (next) {
      // 打开时优先沿用现有路由里出现最多的上游，避免凭空造一个陌生地址。
      const fallback =
        resolvePrimaryEndpoint(props.routes) || '/v1/images/generations'
      // 空路由表要先种一条派生，否则打开开关后保存会被「至少一条路由」拦住。
      const seeded = seedPrimaryRoute(props.routes, fallback)
      props.onPrimaryEndpointChange?.(fallback)
      props.onChange(
        applyPrimaryEndpoint(seeded, fallback, { deriveToEmpty: true })
      )
      if (!props.imageProtocol && looksLikeImagePath(fallback)) {
        // 布局从现有路由推断：edits 已经指向 /images/edits 说明上游是双端点，
        // 否则按单端点（图片内联进 JSON）处理。猜错的话在「高级」里能改。
        const editsRow = derivations.find(
          (row) => row.incomingPath === '/v1/images/edits'
        )
        const dual = /\/images\/edits$/.test(editsRow?.upstreamPath || '')
        props.onImageProtocolChange?.({
          enabled: true,
          upstream: dual
            ? IMAGE_PROTOCOL_UPSTREAMS[1].value
            : IMAGE_PROTOCOL_UPSTREAMS[0].value,
        })
      }
      return
    }
    // 关闭：先把「跟随主端点」的路由写实，再清掉声明，配置仍然可跑。
    props.onChange(applyPrimaryEndpoint(props.routes, declared))
    props.onPrimaryEndpointChange?.('')
    props.onImageProtocolChange?.(null)
  }

  const setPath = (value: string) => {
    setKeepOpen(true)
    const next = value.trim()
    props.onChange(
      applyPrimaryEndpoint(props.routes, next, { deriveToEmpty: true })
    )
    props.onPrimaryEndpointChange?.(next)
    // 以前这里在「输入值不像图片路径」时直接清掉 image_protocol —— 但输入过程会经过
    // 空串和半截路径（`/`、`/v1/im`），等于用户刚改一个字就把协议丢了，填回去也不会恢复。
    // 现在保留用户的输入：协议开不开由开关决定，页面只说明这种组合会折叠到哪里。
  }

  return (
    <div
      className='flex flex-col gap-4'
      data-apiduo={apiduoBlockMark('channel:primary-endpoint')}
    >
      <section className='rounded-lg border p-4'>
        <div className='flex items-start justify-between gap-4'>
          <div className='space-y-0.5'>
            <p className='text-sm font-medium'>{t('Primary endpoint')}</p>
            <p className='text-muted-foreground text-xs'>
              {enabled
                ? t(
                    'Empty upstreams follow this address; image protocol folds image and chat entries into one request.'
                  )
                : t(
                    'Off: every entry forwards on its own route, exactly like new-api.'
                  )}
            </p>
          </div>
          <Switch
            checked={enabled}
            aria-label={t('Primary endpoint')}
            onCheckedChange={setEnabled}
          />
        </div>

        {enabled ? (
          <div className='mt-3 space-y-1.5'>
            <Input
              value={declared}
              spellCheck={false}
              placeholder='/v1/images/generations'
              className='font-mono'
              onChange={(event) => setPath(event.target.value)}
            />
            <p className='text-muted-foreground text-xs'>
              {t(
                'Any prefix works (v1, api/v3, deployments/…); the last segment must be generations.'
              )}
            </p>
            {declared && !declared.startsWith('/') ? (
              <p className='text-destructive text-xs'>
                {t('Primary endpoint must be a path starting with /.')}
              </p>
            ) : declared && !declared.endsWith('/generations') ? (
              <p className='text-destructive text-xs'>
                {t(
                  'This path does not end with generations, so image requests will not land where the protocol expects.'
                )}
              </p>
            ) : null}
          </div>
        ) : null}
      </section>

      {enabled ? (
        <>
          <section className='rounded-lg border p-4'>
            <div className='mb-3 flex items-center justify-between gap-3'>
              <p className='text-sm font-medium'>{t('Flow')}</p>
              <Badge variant='outline'>
                {t('{{count}} derived', { count: derivations.length })}
              </Badge>
            </div>

            <div className='grid grid-cols-1 items-center gap-3 lg:grid-cols-[minmax(0,1.1fr)_auto_minmax(0,1fr)]'>
              <div className='flex flex-col gap-1.5'>
                {ENTRY_PREVIEW.map((entry) => (
                  <div
                    key={entry.path}
                    className='flex items-center justify-between gap-2 rounded-md border bg-background px-2.5 py-1.5'
                  >
                    <span className='text-xs whitespace-nowrap'>
                      {t(entry.role)}
                    </span>
                    <span className='text-muted-foreground truncate font-mono text-[11px]'>
                      {entry.path}
                    </span>
                  </div>
                ))}
              </div>

              <div className='text-muted-foreground flex items-center justify-center'>
                <ArrowRight
                  className='hidden size-4 lg:block'
                  aria-hidden='true'
                />
                <span className='text-xs lg:hidden'>↓</span>
              </div>

              <div className='flex flex-col justify-center gap-1.5 rounded-md border border-primary/40 bg-primary/5 px-3 py-2.5'>
                <p className='text-xs font-medium'>{t('Primary endpoint')}</p>
                <p className='text-primary font-mono text-xs break-all'>
                  {declared || '—'}
                </p>
                <p className='text-muted-foreground text-[11px] leading-relaxed'>
                  {protocolOn
                    ? t(
                        'Image entries are folded into one canonical request first; image-to-image may be rewritten internally by the upstream protocol.'
                      )
                    : t('Entries forward as-is to this address.')}
                </p>
              </div>
            </div>

            {/* APIDUO:primary-endpoint:BEGIN */}
            {/* 图上四个入口是「用户能看到的入口」，不是「协议都接管」。 */}
            <p className='text-muted-foreground mt-3 text-[11px] leading-relaxed'>
              {t(
                'Responses keeps its own route. With the image protocol on, chat entries are converted into image tasks, so keep that channel image-only.'
              )}
            </p>
            {/* APIDUO:primary-endpoint:END */}
          </section>

          <Collapsible open={advancedOpen} onOpenChange={setAdvancedOpen}>
            <section className='rounded-lg border'>
              <CollapsibleTrigger
                render={
                  <button
                    type='button'
                    className='hover:bg-muted/50 flex min-h-11 w-full items-center justify-between gap-3 rounded-t-lg px-4 py-2.5 text-left'
                  />
                }
              >
                <span className='text-sm font-medium'>{t('Advanced')}</span>
                <span className='text-muted-foreground text-xs'>
                  {t('Derived endpoints, protocol details')}
                </span>
              </CollapsibleTrigger>

              <CollapsibleContent className='border-t'>
                <div className='space-y-4 p-4'>
                  <div className='flex items-start justify-between gap-3'>
                    <div className='space-y-0.5'>
                      <p className='text-sm'>{t('Image protocol')}</p>
                      <p className='text-muted-foreground text-xs'>
                        {t(
                          'Fold image entries into one canonical request before sending.'
                        )}
                      </p>
                    </div>
                    <Switch
                      checked={protocolOn}
                      aria-label={t('Image protocol')}
                      disabled={!protocolOn && !looksLikeImagePath(declared)}
                      onCheckedChange={(checked) => {
                        props.onImageProtocolChange?.(
                          checked
                            ? {
                                enabled: true,
                                upstream: IMAGE_PROTOCOL_UPSTREAMS[1].value,
                              }
                            : null
                        )
                      }}
                    />
                  </div>

                  {protocolOn && declared !== '' && !looksLikeImagePath(declared) ? (
                    <p className='text-muted-foreground text-xs'>
                      {t(
                        'This endpoint is not an image path, so the protocol keeps folding image entries to /v1/images/generations; the other entries still forward here.'
                      )}
                    </p>
                  ) : null}

                  {protocolOn ? (
                    <div className='grid grid-cols-1 gap-3 sm:grid-cols-2'>
                      <div className='space-y-1'>
                        <p className='text-xs'>{t('Upstream layout')}</p>
                        <Select
                          value={upstreamLayout}
                          onValueChange={(value) => {
                            if (typeof value === 'string') {
                              props.onImageProtocolChange?.({
                                ...props.imageProtocol,
                                enabled: true,
                                upstream: value,
                              })
                            }
                          }}
                        >
                          <SelectTrigger size='sm'>
                            <span className='truncate'>
                              {t(upstreamLayoutLabel)}
                            </span>
                          </SelectTrigger>
                          <SelectContent alignItemWithTrigger={false}>
                            <SelectGroup>
                              {IMAGE_PROTOCOL_UPSTREAMS.map((option) => (
                                <SelectItem
                                  key={option.value}
                                  value={option.value}
                                >
                                  {t(option.label)}
                                </SelectItem>
                              ))}
                            </SelectGroup>
                          </SelectContent>
                        </Select>
                      </div>

                      <div className='space-y-1'>
                        <p className='text-xs'>{t('Edit path override')}</p>
                        <Input
                          value={props.imageProtocol?.edit_path || ''}
                          placeholder={t(
                            'Derived from the primary path when empty'
                          )}
                          onChange={(event) => {
                            props.onImageProtocolChange?.({
                              ...props.imageProtocol,
                              enabled: true,
                              edit_path: event.target.value,
                            })
                          }}
                        />
                      </div>
                    </div>
                  ) : null}

                  <div className='space-y-2 border-t pt-3'>
                    <div className='flex flex-wrap items-center justify-between gap-3'>
                      <p className='text-sm'>{t('Derived endpoints')}</p>
                      <div className='flex items-center gap-2'>
                        <Select
                          value={pendingPath}
                          onValueChange={(value) => {
                            if (typeof value === 'string') {
                              setPendingPath(value)
                            }
                          }}
                        >
                          <SelectTrigger size='sm'>
                            <SelectValue
                              placeholder={t('Add derived endpoint')}
                            />
                          </SelectTrigger>
                          <SelectContent
                            align='end'
                            alignItemWithTrigger={false}
                          >
                            <SelectGroup>
                              {availablePaths.map((option) => (
                                <SelectItem
                                  key={option.value}
                                  value={option.value}
                                >
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
                            props.onChange(
                              addDerivation(props.routes, pendingPath, declared)
                            )
                            setPendingPath('')
                          }}
                        >
                          <Plus data-icon='inline-start' />
                          {t('Add')}
                        </Button>
                      </div>
                    </div>

                    {derivations.length === 0 ? (
                      <p className='text-muted-foreground text-xs'>
                        {t('No derived endpoints yet.')}
                      </p>
                    ) : (
                      <div className='divide-y rounded-md border'>
                        {derivations.map((row) => (
                          <div
                            key={row.incomingPath}
                            className='grid grid-cols-1 items-center gap-2 px-3 py-2 lg:grid-cols-[minmax(0,1.1fr)_minmax(0,1.2fr)_minmax(0,0.9fr)_auto_2.25rem]'
                          >
                            <div className='min-w-0'>
                              <p className='truncate text-xs'>
                                {t(
                                  getAdvancedCustomIncomingPathLabel(
                                    row.incomingPath
                                  )
                                )}
                              </p>
                              <p className='text-muted-foreground truncate font-mono text-[11px]'>
                                {row.incomingPath}
                              </p>
                            </div>

                            <div className='flex min-w-0 items-center gap-1.5 text-[11px]'>
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
                                  <ArrowRight className='text-muted-foreground size-3 shrink-0' />
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
                                    patchDerivation(
                                      props.routes,
                                      row.incomingPath,
                                      {
                                        converter: value,
                                        passThrough:
                                          value === 'none'
                                            ? row.passThrough
                                            : false,
                                      }
                                    )
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
                                  {getAdvancedCustomConverterOptions(
                                    row.incomingPath
                                  ).map((option) => (
                                    <SelectItem
                                      key={option.value}
                                      value={option.value}
                                    >
                                      {t(option.label)}
                                    </SelectItem>
                                  ))}
                                </SelectGroup>
                              </SelectContent>
                            </Select>

                            <label className='flex items-center gap-1.5 text-[11px]'>
                              <Switch
                                checked={row.passThrough}
                                disabled={row.converter !== 'none'}
                                onCheckedChange={(checked) =>
                                  props.onChange(
                                    patchDerivation(
                                      props.routes,
                                      row.incomingPath,
                                      { passThrough: checked }
                                    )
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
                                  removeDerivation(
                                    props.routes,
                                    row.incomingPath
                                  )
                                )
                              }
                            >
                              <Trash2 className='size-4' />
                            </Button>
                          </div>
                        ))}
                      </div>
                    )}
                  </div>
                </div>
              </CollapsibleContent>
            </section>
          </Collapsible>
        </>
      ) : (
        <section className='rounded-lg border border-dashed p-4'>
          <p className='text-sm'>{t('Nothing is folded while this is off.')}</p>
          <p className='text-muted-foreground mt-1 text-xs leading-relaxed'>
            {t(
              'Every entry keeps its own upstream in the route table and requests are forwarded unchanged — the same behaviour as new-api. Turn the switch on to declare one address for all of them.'
            )}
          </p>
        </section>
      )}
    </div>
  )
}
