import {
  ChevronRight,
  CornerDownRight,
  MoreHorizontal,
  Trash2,
} from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { Combobox } from '@/components/ui/combobox'
import { Input } from '@/components/ui/input'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
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
  type RouteDerivation,
  looksLikeImagePath,
  patchDerivation,
  removeDerivation,
  resolvePrimaryEndpoint,
  seedPrimaryRoute,
} from '@/features/channels/lib/primary-endpoint'
import type { AdvancedCustomRoute } from '@/features/channels/types'
import { converterLabel } from '@/features/protocol-matrix/lib/mode-label'
import { cn } from '@/lib/utils'

/**
 * 主端点：一个渠道对上游只讲一种「母语」。
 *
 * 开关默认关闭——关着时这一页与 new-api 完全一致：每个入口各按 `advanced_routes`
 * 里的上游转发，报文原样，既不折叠也不改写。打开之后才声明 `primary_endpoint`，
 * 空 upstream 的路由跟随它。
 *
 * 页面只允许「主端点」被声明一次：入口表里的落点默认写「跟随主端点」而不是把
 * 那个地址再抄一遍，否则同一个 URL 会在标题、输入框、每行落点里各出现一次，
 * 用户看不出哪一处才是源头。同理，转换器与透传收进行尾的菜单——它们与
 * 「路由转发」页签编辑的是同一份数据，摆成两列只是让同一字段看上去像两件事。
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
  // 从端点只存在于双端点布局：单端点没有第二个地址可覆盖，
  // 摆一个永远填不进去的输入框等于让用户怀疑自己配漏了。
  const dualEndpoint = upstreamLayout === IMAGE_PROTOCOL_UPSTREAMS[1].value

  // 未声明时输入框用路由推导值当 placeholder：地址只出现一次，
  // 且用户能立刻看出「现在实际发到哪里」。
  const derived = useMemo(
    () => resolvePrimaryEndpoint(props.routes),
    [props.routes]
  )
  // 🔴 不能用 `enabled ? declared : derived` 作中心值：图片协议开着而主端点没声明时
  // enabled 为 true、declared 为空串，于是每条路由的 upstream 都不等于中心，
  // 整张表被判成「全部显式覆盖」——地址会在每行重复一遍，正是这次要消掉的冗余。
  // 中心值只取决于「有没有声明」，开关只管界面展开。
  const primary = declared || derived
  const derivations = useMemo(
    () => buildDerivations(props.routes, primary),
    [props.routes, primary]
  )

  const [advancedOpen, setAdvancedOpen] = useState(false)
  // 落点单元格平时是「跟随主端点」，点了才变成输入框。用入口路径当键，
  // 同一入口的多条路由本来就共享一个落点。
  const [editingPath, setEditingPath] = useState<string | null>(null)

  const availablePaths = useMemo(
    () =>
      ADVANCED_CUSTOM_INCOMING_PATH_OPTIONS.filter(
        (option) => !derivations.some((row) => row.incomingPath === option.value)
      ),
    [derivations]
  )

  const setEnabled = (next: boolean) => {
    setKeepOpen(next)
    setEditingPath(null)
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
        // 否则按单端点（图片内联进 JSON）处理。猜错的话在「协议细节」里能改。
        const editsRow = derivations.find(
          (row) => row.incomingPath === '/v1/images/edits'
        )
        const editsUpstream = (editsRow?.upstreamPath || '').replace(
          /\/+$/,
          ''
        )
        const dual = editsUpstream.endsWith('/v1/images/edits')
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

  // 形状错误只报第一条：两条同时出现时，用户会以为要同时满足两个条件。
  let pathError: string | null = null
  if (declared && !declared.startsWith('/')) {
    pathError = t('Primary endpoint must be a path starting with /.')
  } else if (declared && !declared.endsWith('/generations')) {
    pathError = t(
      'This path does not end with generations, so image requests will not land where the protocol expects.'
    )
  }

  const protocolSummary = protocolOn
    ? `${t('Image protocol on')} · ${t(upstreamLayoutLabel)}`
    : ''

  return (
    <div
      className='flex flex-col gap-3'
      data-apiduo={apiduoBlockMark('channel:primary-endpoint')}
    >
      <section className='rounded-lg border'>
        <div className='flex items-start justify-between gap-4 px-4 py-3'>
          <div className='space-y-0.5'>
            <p
              className='apiduo-text text-sm font-medium'
              data-apiduo={apiduoBlockMark('channel:primary-endpoint')}
            >
              {t('Primary endpoint')}
            </p>
            <p className='text-muted-foreground text-xs'>
              {enabled
                ? t('Empty upstreams follow this address.')
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
          <div className='space-y-1.5 border-t px-4 py-3'>
            <Input
              value={declared}
              spellCheck={false}
              placeholder={derived || '/v1/images/generations'}
              className='font-mono'
              onChange={(event) => setPath(event.target.value)}
            />
            {declared ? null : (
              <p className='text-muted-foreground text-xs'>
                {t(
                  'Not pinned yet: the value above is derived from the route table. Typing here pins it for this channel.'
                )}
              </p>
            )}
            {pathError ? (
              <p className='text-destructive text-xs'>{pathError}</p>
            ) : null}
          </div>
        ) : null}
      </section>

      {enabled ? (
        <>
          <section className='rounded-lg border'>
            <div className='flex flex-wrap items-center justify-between gap-2 px-4 py-3'>
              <div className='flex items-center gap-2'>
                <p className='text-sm font-medium'>{t('Entries')}</p>
                <Badge variant='outline'>{derivations.length}</Badge>
              </div>
              <Combobox
                options={availablePaths.map((option) => ({
                  value: option.value,
                  label: t(option.label),
                }))}
                value=''
                placeholder={t('Add route')}
                disabled={availablePaths.length === 0}
                className='h-8 w-full sm:w-[13rem]'
                onValueChange={(incomingPath) => {
                  if (typeof incomingPath !== 'string') return
                  props.onChange(
                    addDerivation(props.routes, incomingPath, primary)
                  )
                }}
              />
            </div>

            {derivations.length === 0 ? (
              <p className='text-muted-foreground border-t px-4 py-3 text-xs'>
                {t('No derived endpoints yet.')}
              </p>
            ) : (
              <div className='divide-y border-t'>
                {derivations.map((row) => (
                  <DerivationRow
                    key={row.incomingPath}
                    row={row}
                    primary={primary}
                    editing={editingPath === row.incomingPath}
                    onStartEdit={() => setEditingPath(row.incomingPath)}
                    onStopEdit={() => setEditingPath(null)}
                    onPatch={(patch) =>
                      props.onChange(
                        patchDerivation(props.routes, row.incomingPath, patch)
                      )
                    }
                    onRemove={() =>
                      props.onChange(
                        removeDerivation(props.routes, row.incomingPath)
                      )
                    }
                  />
                ))}
              </div>
            )}

            <p className='text-muted-foreground border-t px-4 py-2 text-[11px] leading-relaxed'>
              {t(
                'Responses keeps its own route. With the image protocol on, chat entries are converted into image tasks, so keep that channel image-only.'
              )}
            </p>
          </section>

          <Collapsible open={advancedOpen} onOpenChange={setAdvancedOpen}>
            <section className='rounded-lg border'>
              <CollapsibleTrigger
                render={
                  <button
                    type='button'
                    className='hover:bg-muted/50 focus-visible:ring-ring/50 flex min-h-11 w-full items-center justify-between gap-3 rounded-lg px-4 py-2.5 text-left outline-none focus-visible:ring-3'
                  />
                }
              >
                <span className='inline-flex items-center gap-2'>
                  <ChevronRight
                    className={cn(
                      'text-muted-foreground size-4 transition-transform',
                      advancedOpen && 'rotate-90'
                    )}
                    aria-hidden='true'
                  />
                  <span className='text-sm font-medium'>
                    {t('Protocol details')}
                  </span>
                </span>
                <span className='text-muted-foreground text-xs'>
                  {protocolSummary}
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
                        <p className='text-muted-foreground text-[11px] leading-relaxed'>
                          {dualEndpoint
                            ? t(
                                'Image-to-image switches to the edit endpoint and is encoded as multipart files.'
                              )
                            : t(
                                'Image-to-image stays on the same endpoint; image references travel in the JSON body and mask is not forwarded.'
                              )}
                        </p>
                      </div>

                      {dualEndpoint ? (
                        <div className='space-y-1'>
                          <p className='text-xs'>{t('Edit path override')}</p>
                          <Input
                            value={props.imageProtocol?.edit_path || ''}
                            placeholder={t(
                              'Derived from the image primary path when empty'
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
                      ) : null}
                    </div>
                  ) : null}
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

function DerivationRow(props: {
  row: RouteDerivation
  primary: string
  editing: boolean
  onStartEdit: () => void
  onStopEdit: () => void
  onPatch: (patch: {
    upstreamPath?: string
    converter?: string
    passThrough?: boolean
  }) => void
  onRemove: () => void
}) {
  const { t } = useTranslation()
  const { row } = props
  const converterOptions = getAdvancedCustomConverterOptions(row.incomingPath)
  const overridden = !row.followsPrimary
  const showInput = props.editing || overridden
  // 转换器与透传收进菜单：这两个字段在「路由转发」页签里已经有一整行可编辑，
  // 在这里再摆两列，等于让同一份配置在页面上出现两次。
  const hasAdvanced = row.converter !== 'none' || row.passThrough

  return (
    <div className='grid grid-cols-1 items-center gap-2 px-4 py-2 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.05fr)_auto]'>
      <div className='min-w-0'>
        <p className='flex items-center gap-1.5 truncate text-xs font-medium'>
          {t(getAdvancedCustomIncomingPathLabel(row.incomingPath))}
          {row.routeIndexes.length > 1 ? (
            <Badge variant='outline' className='shrink-0'>
              ×{row.routeIndexes.length}
            </Badge>
          ) : null}
        </p>
        <p className='text-muted-foreground truncate font-mono text-[11px]'>
          {row.incomingPath || '—'}
        </p>
      </div>

      <div className='flex min-w-0 items-center gap-1.5'>
        {showInput ? (
          <>
            <Input
              autoFocus={props.editing}
              value={overridden ? row.upstreamPath : ''}
              placeholder={props.primary || '/v1/images/generations'}
              className='h-8 min-w-0 font-mono text-xs'
              onChange={(event) =>
                props.onPatch({ upstreamPath: event.target.value })
              }
              onKeyDown={(event) => {
                if (event.key === 'Enter') props.onStopEdit()
                if (event.key === 'Escape') props.onStopEdit()
              }}
            />
            <Button
              type='button'
              variant='ghost'
              size='icon'
              className='text-muted-foreground size-8 shrink-0'
              aria-label={t('Follows primary')}
              onClick={() => {
                props.onPatch({ upstreamPath: '' })
                props.onStopEdit()
              }}
            >
              <CornerDownRight className='size-3.5' aria-hidden='true' />
            </Button>
          </>
        ) : (
          <button
            type='button'
            className='text-muted-foreground hover:text-foreground hover:bg-muted/60 focus-visible:ring-ring/50 inline-flex min-w-0 items-center gap-1.5 rounded-md px-1.5 py-1 text-[11px] outline-none focus-visible:ring-3'
            onClick={props.onStartEdit}
          >
            <CornerDownRight className='size-3 shrink-0' aria-hidden='true' />
            <span className='truncate'>{t('Follows primary')}</span>
          </button>
        )}
        {hasAdvanced ? (
          <Badge variant='secondary' className='shrink-0'>
            {row.converter !== 'none'
              ? t(converterLabel(row.converter))
              : t('Pass through')}
          </Badge>
        ) : null}
      </div>

      <div className='flex items-center justify-end gap-1'>
        <Popover>
          <PopoverTrigger
            render={
              <Button
                type='button'
                variant='ghost'
                size='icon'
                className='text-muted-foreground size-7'
                aria-label={t('Actions')}
              />
            }
          >
            <MoreHorizontal className='size-4' aria-hidden='true' />
          </PopoverTrigger>
          <PopoverContent
            align='end'
            side='bottom'
            sideOffset={6}
            className='w-[min(18rem,calc(100vw-2rem))] space-y-3 p-3'
          >
            <div className='space-y-1'>
              <p className='text-xs font-medium'>{t('Converter')}</p>
              <Select
                value={row.converter}
                onValueChange={(value) => {
                  if (typeof value !== 'string') return
                  props.onPatch({
                    converter: value,
                    passThrough: value === 'none' ? row.passThrough : false,
                  })
                }}
              >
                <SelectTrigger size='sm' className='w-full'>
                  <span className='truncate'>
                    {t(converterLabel(row.converter))}
                  </span>
                </SelectTrigger>
                <SelectContent alignItemWithTrigger={false}>
                  <SelectGroup>
                    {converterOptions.map((option) => (
                      <SelectItem key={option.value} value={option.value}>
                        {t(option.label)}
                      </SelectItem>
                    ))}
                  </SelectGroup>
                </SelectContent>
              </Select>
            </div>
            <label className='flex items-center justify-between gap-2 text-xs'>
              <span>{t('Pass through')}</span>
              <Switch
                checked={row.passThrough}
                disabled={row.converter !== 'none'}
                onCheckedChange={(checked) =>
                  props.onPatch({ passThrough: checked })
                }
              />
            </label>
            <p className='text-muted-foreground text-[11px] leading-relaxed'>
              {t(
                'The same fields are editable in the Forwarding Routes tab.'
              )}
            </p>
          </PopoverContent>
        </Popover>

        <Button
          type='button'
          variant='ghost'
          size='icon'
          className='text-muted-foreground size-7'
          aria-label={t('Remove derived endpoint')}
          onClick={props.onRemove}
        >
          <Trash2 className='size-3.5' aria-hidden='true' />
        </Button>
      </div>
    </div>
  )
}
