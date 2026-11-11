import {
  ArrowRight,
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

// 顺序与 IMAGE_PROTOCOL_UPSTREAMS 一一对应（解构命名即文档）。
// 每张卡只说两件事：图生图走哪、用什么编码——这正是两种布局的全部差别。
const [IMAGE_SHAPE_SINGLE, IMAGE_SHAPE_DUAL] = IMAGE_PROTOCOL_UPSTREAMS

// 每张卡只回答一个问题：主端点在上游落成几条路径。
// 不复述下游请求类型——下游只认主端点，那是另一层的事（见入口卡的「入口去向」）。
const IMAGE_SHAPE_CARDS = [
  {
    value: IMAGE_SHAPE_SINGLE.value,
    title: 'One upstream path',
    subtitle: IMAGE_SHAPE_SINGLE.label,
    rows: [{ label: 'Every request', target: 'primary', encoding: 'JSON' }],
    note: 'Image references stay in the JSON body; mask is not forwarded.',
    dual: false,
  },
  {
    value: IMAGE_SHAPE_DUAL.value,
    title: 'Two upstream paths',
    subtitle: IMAGE_SHAPE_DUAL.label,
    rows: [
      { label: 'Without an image', target: 'primary', encoding: 'JSON' },
      { label: 'With an image', target: 'edit', encoding: 'multipart' },
    ],
    note: 'Image and mask are uploaded as files.',
    dual: true,
  },
] as const

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
  /** 渠道的 base_url。数据流的第三段（上游）要它才讲得完整。 */
  baseUrl?: string
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

  // 数据流的第三段：上游是谁。只去掉协议头，保留 host 后面的路径段——
  // 有些渠道的 base_url 带 /v1，砍掉就会让人以为请求打到别的目录。
  const upstreamTarget = upstreamEndpointLabel(props.baseUrl)

  // 图生图的落点预览：显式覆盖优先，否则按后端 `deriveEditPath` 的同一规则算，
  // 卡片上写的路径就是请求真的会去的路径。前端只做预览，保存后仍以后端为准。
  const editPathPreview =
    (props.imageProtocol?.edit_path || '').trim() || deriveEditPathPreview(primary)

  return (
    <div
      className='flex flex-col gap-3'
      data-apiduo={apiduoBlockMark('channel:primary-endpoint')}
    >
      <section className='overflow-hidden rounded-lg border'>
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
                ? t(
                    'However many endpoints the upstream has, they all converge here; every downstream entry is converted from it.'
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
          <div className='space-y-1.5 border-t px-4 py-3'>
            {/* 收敛契约，读法是一条从左到右的句子：
                  上游（无论几个端点）──收口──▶ 主端点 ──转换──▶ 下游（n 个入口）
                顺序刻意不是请求方向：用户要理解的是「一个契约两头派生」，
                写成 下游→主端点→上游 会让人以为下游才是源头。 */}
            <div className='grid grid-cols-1 gap-x-2 gap-y-1.5 lg:grid-cols-[minmax(0,1fr)_auto_minmax(0,1.3fr)_auto_minmax(0,1fr)] lg:items-end'>
              {/* 上游 */}
              <div className='min-w-0'>
                <p className='text-muted-foreground text-[11px] leading-4'>
                  {t('Upstream')}
                </p>
                <div className='flex h-8 min-w-0 items-center gap-1.5'>
                  <p
                    className={cn(
                      'min-w-0 truncate font-mono text-xs',
                      upstreamTarget ? '' : 'text-muted-foreground italic'
                    )}
                    title={props.baseUrl || undefined}
                  >
                    {upstreamTarget || t('Not set')}
                  </p>
                  {/* 上游格直接标出「几个端点」：它们全部收口到主端点。
                      点一下展开协议细节去改形态。 */}
                  {protocolOn ? (
                    <button
                      type='button'
                      className='focus-visible:ring-ring/50 shrink-0 rounded-md outline-none focus-visible:ring-3'
                      title={t(
                        'Open the protocol details to change how image-to-image reaches the upstream.'
                      )}
                      onClick={() => setAdvancedOpen(true)}
                    >
                      <Badge variant='outline' className='font-normal'>
                        {dualEndpoint
                          ? t('Two endpoints')
                          : t('One endpoint')}
                      </Badge>
                    </button>
                  ) : null}
                </div>
              </div>

              <div className='flex flex-col items-center justify-end lg:pb-2'>
                <span className='text-muted-foreground/80 text-[10px] leading-3'>
                  {t('Converge')}
                </span>
                <ArrowRight
                  className='text-muted-foreground/50 size-4 rotate-90 lg:rotate-0'
                  aria-hidden='true'
                />
              </div>

              {/* 主端点：唯一契约 */}
              <div className='min-w-0'>
                <p className='text-muted-foreground text-[11px] leading-4'>
                  {t('Primary endpoint')}
                </p>
                <Input
                  value={declared}
                  spellCheck={false}
                  placeholder={derived || '/v1/images/generations'}
                  className='mt-1 h-8 font-mono'
                  onChange={(event) => setPath(event.target.value)}
                />
              </div>

              <div className='flex flex-col items-center justify-end lg:pb-2'>
                <span className='text-muted-foreground/80 text-[10px] leading-3'>
                  {t('Convert')}
                </span>
                <ArrowRight
                  className='text-muted-foreground/50 size-4 rotate-90 lg:rotate-0'
                  aria-hidden='true'
                />
              </div>

              {/* 下游 */}
              <div className='min-w-0'>
                <p className='text-muted-foreground text-[11px] leading-4'>
                  {t('Downstream')}
                </p>
                <p className='flex h-8 min-w-0 items-center truncate text-xs font-medium'>
                  {t('{{count}} entries', { count: derivations.length })}
                </p>
              </div>
            </div>
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

        {enabled ? (
          <Collapsible open={advancedOpen} onOpenChange={setAdvancedOpen}>
            <CollapsibleTrigger
              render={
                <button
                  type='button'
                  className='hover:bg-muted/50 focus-visible:ring-ring/50 flex min-h-11 w-full items-center justify-between gap-3 border-t px-4 py-2.5 text-left outline-none focus-visible:ring-3'
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
                  <div className='space-y-3'>
                    {/* 两个布局并排画成请求线，而不是一个只写名字的下拉：
                        「单 / 双端点」本身不含信息，真正的差别只有一行——图生图
                        走哪个路径、用什么编码。两卡同时可见才比得出来。 */}
                    <div className='space-y-1.5'>
                      <p className='text-xs'>
                        {t('How does the primary endpoint land upstream?')}
                      </p>
                      <div
                        role='radiogroup'
                        aria-label={t('Upstream layout')}
                        className='grid grid-cols-1 gap-2 sm:grid-cols-2'
                      >
                        {IMAGE_SHAPE_CARDS.map((card) => {
                          const checked = upstreamLayout === card.value
                          return (
                            <button
                              key={card.value}
                              type='button'
                              role='radio'
                              aria-checked={checked}
                              onClick={() => {
                                props.onImageProtocolChange?.({
                                  ...props.imageProtocol,
                                  enabled: true,
                                  upstream: card.value,
                                })
                              }}
                              className={cn(
                                'focus-visible:ring-ring/50 rounded-lg border p-3 text-left outline-none transition-colors focus-visible:ring-3',
                                checked
                                  ? 'border-primary bg-primary/5'
                                  : 'hover:border-foreground/25'
                              )}
                            >
                              <span className='flex items-start gap-2'>
                                <span
                                  className={cn(
                                    'mt-1 flex size-3.5 shrink-0 items-center justify-center rounded-full border',
                                    checked
                                      ? 'border-primary'
                                      : 'border-muted-foreground/50'
                                  )}
                                  aria-hidden='true'
                                >
                                  {checked ? (
                                    <span className='bg-primary size-1.5 rounded-full' />
                                  ) : null}
                                </span>
                                <span className='min-w-0'>
                                  <span className='block text-xs font-medium'>
                                    {t(card.title)}
                                  </span>
                                  <span className='text-muted-foreground block text-[11px]'>
                                    {t(card.subtitle)}
                                  </span>
                                </span>
                              </span>

                              {/* 一行一条上游路径。标签说「什么时候走它」，
                                  不是「客户端调了哪个接口」——后者属于下游那一层。 */}
                              <span className='mt-2.5 block space-y-1'>
                                {card.rows.map((row) => {
                                  const target =
                                    row.target === 'edit'
                                      ? editPathPreview
                                      : primary
                                  return (
                                    <span
                                      key={row.label}
                                      className='flex items-center gap-1.5 text-[11px]'
                                    >
                                      <span className='text-muted-foreground w-16 shrink-0 truncate'>
                                        {t(row.label)}
                                      </span>
                                      <ArrowRight
                                        className='text-muted-foreground/60 size-3 shrink-0'
                                        aria-hidden='true'
                                      />
                                      <span
                                        className={cn(
                                          'min-w-0 truncate rounded border px-1 py-0.5 font-mono text-[10px]',
                                          row.target === 'edit'
                                            ? 'bg-primary/10'
                                            : 'bg-muted'
                                        )}
                                        title={target || undefined}
                                      >
                                        {target || t('Not set')}
                                      </span>
                                      <span className='text-muted-foreground shrink-0 text-[10px]'>
                                        {row.encoding}
                                      </span>
                                    </span>
                                  )
                                })}
                              </span>

                              <span className='text-muted-foreground mt-2 block text-[11px] leading-relaxed'>
                                {t(card.note)}
                              </span>
                            </button>
                          )
                        })}
                      </div>
                    </div>

                    {dualEndpoint ? (
                      <div className='space-y-1'>
                        <div className='flex items-baseline justify-between gap-2'>
                          <p className='text-xs'>{t('Image-to-image path')}</p>
                          <span className='text-muted-foreground text-[11px]'>
                            {t('Derived automatically')}
                          </span>
                        </div>
                        <Input
                          value={props.imageProtocol?.edit_path || ''}
                          spellCheck={false}
                          className='font-mono'
                          placeholder={editPathPreview || t('Not set')}
                          onChange={(event) => {
                            props.onImageProtocolChange?.({
                              ...props.imageProtocol,
                              enabled: true,
                              edit_path: event.target.value,
                            })
                          }}
                        />
                        <p className='text-muted-foreground text-[11px] leading-relaxed'>
                          {t(
                            'Left empty, the last segment of the primary endpoint is swapped from generations to edits. Fill it in only when the upstream path differs.'
                          )}
                        </p>
                      </div>
                    ) : null}
                  </div>
                ) : null}
              </div>
            </CollapsibleContent>
          </Collapsible>
        ) : null}
      </section>

      {enabled ? (
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
                  protocolOn={protocolOn}
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

          {/* 逐入口写明「这一条被怎么处理」。原来只有一句静态提示、且只提 responses，
              读的人没法回答「chat 走哪」——而 chat 恰恰是最容易想错的一条：
              它会被转成出图任务，带图时和图生图同路。 */}
          {protocolOn ? (
            <div className='border-t px-4 py-2.5'>
              <p className='text-muted-foreground text-[11px]'>
                {t('Where each entry goes')}
              </p>
              <ul className='mt-1 space-y-0.5'>
                {derivations.map((row) => (
                  <li
                    key={row.incomingPath}
                    className='flex items-baseline gap-1.5 text-[11px]'
                  >
                    <span className='text-muted-foreground shrink-0 font-mono text-[10px]'>
                      {row.incomingPath}
                    </span>
                    <ArrowRight
                      className='text-muted-foreground/50 size-2.5 shrink-0 self-center'
                      aria-hidden='true'
                    />
                    <span className='min-w-0'>{t(entryRouteKey(row.incomingPath))}</span>
                  </li>
                ))}
              </ul>
            </div>
          ) : null}
        </section>

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

function isChatEntry(incomingPath: string): boolean {
  return incomingPath.replace(/\/+$/, '').endsWith('/v1/chat/completions')
}

/**
 * 每个入口被图片协议怎么处理。判据与后端 `prepareImageProtocol` 的 switch
 * 一一对应：generations / edits / chat 三个分支接管，其余走 default 原样转发。
 */
function entryRouteKey(incomingPath: string): string {
  const path = incomingPath.replace(/\/+$/, '')
  if (path.endsWith('/v1/images/generations') || path.endsWith('/v1/images/edits')) {
    return 'Converted from the primary endpoint'
  }
  if (isChatEntry(incomingPath)) {
    return 'Turned into an image task, then converted from the primary endpoint'
  }
  return 'Not intercepted; keeps its own landing'
}

/**
 * 后端 `deriveEditPath` 的前端镜像：末段 `/generations` 换成 `/edits`。
 * 末段不是 generations 时返回空串——后端在这种配置下会直接报错，
 * 这里不猜路径，让上面的形状提示去说为什么。
 */
function deriveEditPathPreview(primary: string): string {
  const trimmed = (primary || '').trim().replace(/\/+$/, '')
  const suffix = '/generations'
  if (!trimmed.endsWith(suffix)) return ''
  return `${trimmed.slice(0, -suffix.length)}/edits`
}

/**
 * 把渠道的 base_url 压成「数据流第三段」能放下的样子：去掉协议头，保留路径段。
 *
 * 不直接用 host 是有原因的：有些上游的接口根在子路径上（`https://api.x.com/v1`），
 * 只显示 host 会让人以为请求打到站点根。解析失败时退回原串裁掉协议头——
 * 这一格是给人看的，不是给请求用的，宁可粗糙也不要空着。
 */
function upstreamEndpointLabel(baseUrl?: string): string {
  const raw = (baseUrl || '').trim()
  if (!raw) return ''
  const withoutScheme = raw.replace(/^[a-z][a-z0-9+.-]*:\/\//i, '')
  const [hostAndPath] = withoutScheme.split(/[?#]/)
  return hostAndPath.replace(/\/+$/, '')
}

/**
 * 图片协议自己接管的入口：两个图片入口 + 对话（对话会被转成出图任务）。
 * 与后端 `ImageProtocolConfig.ServesPath` 保持同一判断，chat 那一条来自
 * `prepareImageProtocol` 的 `RelayModeChatCompletions` 分支——它不看主端点是不是
 * 图片路径，只要协议开着就折叠。
 */
function isFoldedByImageProtocol(incomingPath: string): boolean {
  const path = incomingPath.replace(/\/+$/, '')
  return (
    path.endsWith('/v1/images/generations') ||
    path.endsWith('/v1/images/edits') ||
    path.endsWith('/v1/chat/completions')
  )
}

function DerivationRow(props: {
  row: RouteDerivation
  primary: string
  protocolOn: boolean
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
  // 图片协议一旦开启，它自己按 canonical 请求选端点：图片入口与对话入口的
  // upstream_path 全部不参与决策（端点来自主端点 + 从端点推导）。这时行里再显示
  // 一个「落点」，说的就是一件不会发生的事——端到端实测过：给 chat 配
  // /v1/chat/completions，请求仍然打在主端点上。
  const foldedByProtocol =
    props.protocolOn && isFoldedByImageProtocol(row.incomingPath)
  const overridden = !row.followsPrimary
  const showInput = !foldedByProtocol && (props.editing || overridden)
  // 转换器与透传收进菜单：这两个字段在「路由转发」页签里已经有一整行可编辑，
  // 在这里再摆两列，等于让同一份配置在页面上出现两次。
  const hasAdvanced = row.converter !== 'none' || row.passThrough

  // 落点单元格三态：协议接管 / 可编辑输入 / 跟随主端点。写成变量而不是嵌套三元，
  // 一是 lint 禁止，二是这三态各自有完整说明，塞进一个表达式读不出主次。
  const followCell = (
    <button
      type='button'
      className='text-muted-foreground hover:text-foreground hover:bg-muted/60 focus-visible:ring-ring/50 inline-flex min-w-0 items-center gap-1.5 rounded-md px-1.5 py-1 text-[11px] outline-none focus-visible:ring-3'
      title={t('Click to give this entry its own upstream.')}
      onClick={props.onStartEdit}
    >
      <ArrowRight className='size-3 shrink-0' aria-hidden='true' />
      <span className='truncate'>{t('Primary endpoint')}</span>
    </button>
  )

  const editCell = (
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
  )

  const foldedCell = (
    <span
      className='text-muted-foreground inline-flex min-w-0 items-center gap-1.5 text-[11px]'
      title={t(
        'The image protocol picks the endpoint itself; this entry does not use its own upstream.'
      )}
    >
      <ArrowRight className='size-3 shrink-0' aria-hidden='true' />
      <span className='truncate'>{t('Primary endpoint')}</span>
      {/* 两个徽标各说一件事：这条被怎么处理（转出图 / 协议），以及「存了覆盖值但
          不会生效」。不能让后者顶掉前者——恰好是存了失效覆盖的行，最需要知道它被
          协议接管了，否则看不懂为什么改不动。 */}
      <Badge variant='outline' className='shrink-0 font-normal'>
        {isChatEntry(row.incomingPath)
          ? t('Turned into an image task')
          : t('Protocol')}
      </Badge>
      {overridden ? (
        <Badge variant='outline' className='shrink-0'>
          {t('Override ignored')}
        </Badge>
      ) : null}
    </span>
  )

  let landingCell = followCell
  if (foldedByProtocol) landingCell = foldedCell
  else if (showInput) landingCell = editCell

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
        {landingCell}
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
