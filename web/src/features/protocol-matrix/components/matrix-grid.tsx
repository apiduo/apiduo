import {
  ArrowLeftRight,
  ArrowRight,
  Check,
  ChevronDown,
  Minus,
} from 'lucide-react'
import { Fragment, type ReactNode, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Popover,
  PopoverContent,
  PopoverTitle,
  PopoverTrigger,
} from '@/components/ui/popover'
import {
  getAdvancedCustomConverterDefaults,
  parseAdvancedCustomRouteModels,
} from '@/features/channels/lib/advanced-custom'
import type { AdvancedCustomRoute } from '@/features/channels/types'
import { cn } from '@/lib/utils'

import { collectEndpoints } from '../lib/coverage'
import {
  type Grid,
  type GridCellMode,
  type GridConflictCode,
  buildGrid,
  cellAt,
  converterOptionsForEndpoint,
  fillAll,
  fillEndpoint,
  gridToRoutes,
  isCellEditable,
  orphanCount,
  setCellMode,
  supportedModels,
  toggleCell,
} from '../lib/grid'
import { converterLabel, converterTag } from '../lib/mode-label'
import { type ModelGroupKey, groupModels } from '../lib/model-groups'
import {
  type StoredPreset,
  createPreset,
  deletePreset,
  listPresets,
} from '../lib/preset-api'
import {
  type Preset,
  presetToRoutes,
  routesToPresetEntries,
} from '../lib/presets'
import { PresetBar } from './preset-bar'

/**
 * 列按模态分组。判据在 `lib/model-groups.ts`（纯函数 + 测试），这里只管画：
 * 一行组标题、一行模型名，两组之间加竖线。颜色只是让「哪几列是图片」
 * 一眼看得出来，不承担语义。文案沿用上游已有的 Text / Image / Video / Other。
 */
const GROUP_LABEL: Record<ModelGroupKey, string> = {
  text: 'Text',
  image: 'Image',
  video: 'Video',
  other: 'Other',
}

const GROUP_STYLE: Record<ModelGroupKey, string> = {
  // 三组用同一种"淡色底 + 深色字"待遇。文本组别用 bg-muted + 灰字：
  // 那读起来像禁用，而它恰恰是渠道里最多的一类。
  text: 'bg-slate-500/10 text-slate-700 dark:text-slate-300',
  image: 'bg-sky-500/10 text-sky-700 dark:text-sky-300',
  video: 'bg-violet-500/10 text-violet-700 dark:text-violet-300',
  other: 'bg-amber-500/10 text-amber-700 dark:text-amber-300',
}

const CONFLICT_TEXT: Record<GridConflictCode, string> = {
  'regex-models':
    'Regex model rules cannot be shown per model — edit in list view',
  'unsupported-converter':
    'Its converter cannot serve this endpoint — edit in list view',
  'overlapping-routes': 'Two routes claim the same model — edit in list view',
}

export function MatrixGrid({
  routes,
  models,
  onChange,
}: {
  routes?: AdvancedCustomRoute[]
  /** 渠道声明的模型清单，逗号分隔。 */
  models?: string
  onChange: (routes: AdvancedCustomRoute[]) => void
}) {
  const { t } = useTranslation()
  const modelList = useMemo(
    () => parseAdvancedCustomRouteModels(models ?? ''),
    [models]
  )
  const endpoints = useMemo(() => collectEndpoints(routes), [routes])
  // 列按模态重排，但格子是按模型名查的，重排不影响任何读写。
  const groups = useMemo(() => groupModels(modelList), [modelList])
  const ordered = useMemo(
    () =>
      groups.flatMap((group) =>
        group.models.map((model) => ({ model, group: group.key }))
      ),
    [groups]
  )
  const grid = useMemo(
    () => buildGrid(routes, modelList, endpoints),
    [routes, modelList, endpoints]
  )

  const [customPresets, setCustomPresets] = useState<StoredPreset[]>([])

  const apply = (next: Grid) => onChange(gridToRoutes(next, routes))
  const applyPreset = (preset: Preset) =>
    onChange(presetToRoutes(grid, preset, routes))

  // 模板是管理员数据，接口也挂了 AdminAuth——这个对话框本来就只在管理员侧出现。
  useEffect(() => {
    let alive = true
    listPresets()
      .then((list) => {
        if (alive) setCustomPresets(list)
      })
      .catch(() => {
        // 内置那五条是代码里的，读不到库也只是少几片自定义芯片；但静默失败会让
        // 管理员以为模板被删了，所以还是说一句。
        if (alive) toast.error(t('Could not load the presets'))
      })
    return () => {
      alive = false
    }
  }, [])

  const captured = useMemo(() => routesToPresetEntries(routes), [routes])

  const savePreset = async (name: string) => {
    try {
      // 一条模板一行，增删各自落库——不再整份覆盖，两个人同时改不会互相吃掉。
      const created = await createPreset(name, captured)
      setCustomPresets((previous) => [...previous, created])
      toast.success(t('Preset saved'))
    } catch {
      toast.error(t('Could not save the preset'))
      throw new Error('save failed')
    }
  }

  const removePreset = async (id: string) => {
    try {
      await deletePreset(id)
      setCustomPresets((previous) =>
        previous.filter((preset) => preset.id !== id)
      )
    } catch {
      toast.error(t('Could not delete the preset'))
    }
  }

  const generated = useMemo(() => {
    const visible = new Set(grid.endpoints)
    return gridToRoutes(grid, routes).filter((route) =>
      visible.has((route.incoming_path ?? '').trim())
    )
  }, [grid, routes])

  const orphans = orphanCount(grid)

  if (modelList.length === 0) {
    return (
      <div className='text-muted-foreground rounded-md border border-dashed p-4 text-sm'>
        {t(
          'Fill in the channel model list first — the matrix needs to know which models exist.'
        )}
      </div>
    )
  }

  return (
    <div className='flex flex-col gap-3'>
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <p className='text-muted-foreground text-xs'>
          {t(
            'Click a cell to forward it as is or turn it off. Use the arrow to pick a converter.'
          )}
        </p>
        <div className='flex gap-2'>
          <Button
            type='button'
            variant='outline'
            size='sm'
            onClick={() => apply(fillAll(grid, 'none'))}
          >
            {t('Select all')}
          </Button>
          <Button
            type='button'
            variant='ghost'
            size='sm'
            onClick={() => apply(fillAll(grid, 'off'))}
          >
            {t('Clear all')}
          </Button>
        </div>
      </div>

      <PresetBar
        custom={customPresets}
        entries={captured}
        onApply={applyPreset}
        onSave={savePreset}
        onDelete={removePreset}
      />

      {/*
       * 表格是**旋转过的**：行是模型，列是端点。
       *
       * 因为两者的数量级不同——端点是有限的（默认 5 个，带自定义一般也不超过 10），
       * 模型可以几十个。横着摆几十列必须横向滚，而横向滚是最差的一种滚：
       * 表头跟着动、看不出自己在哪一行。竖着摆就变成普通的长表格，纵向滚动 +
       * 钉住表头是所有人都会用的模式。
       *
       * border-collapse 会让 sticky 单元格的边框被相邻单元格遮住，所以用
       * border-separate + 单元格自带边框。
       */}
      <div className='max-h-[440px] overflow-auto rounded-md border'>
        <table
          data-apiduo-matrix=''
          className='w-full border-separate border-spacing-0 text-xs'
        >
          <thead>
            <tr>
              <th className='bg-muted sticky top-0 left-0 z-30 w-[200px] min-w-[200px] border-r border-b p-2 text-left align-bottom font-medium'>
                {t('Models')}
                <span className='ml-1 font-normal opacity-60'>
                  {modelList.length}
                </span>
              </th>
              {grid.endpoints.map((endpoint) => {
                const conflict = grid.conflicts[endpoint]
                return (
                  <th
                    key={endpoint}
                    className='bg-muted sticky top-0 z-20 border-b px-1.5 py-2 text-center align-bottom font-medium'
                  >
                    {/* 路径按 `/` 折行：`/v1/chat/completion` + `s` 这种
                        从词中间劈开的样子像坏了。用 `<wbr>` 给出断点而不是
                        零宽空格——后者会被复制进剪贴板，粘到别处看不见却存在。 */}
                    <code
                      className='mx-auto line-clamp-3 block max-w-[136px] font-mono text-[11px] leading-snug break-words whitespace-normal'
                      title={endpoint}
                    >
                      {endpoint.split('/').map((part, index) => (
                        <Fragment key={`${part}-${index}`}>
                          {index > 0 ? '/' : ''}
                          {index > 0 ? <wbr /> : null}
                          {part}
                        </Fragment>
                      ))}
                    </code>
                    {conflict && (
                      <Badge
                        variant='outline'
                        className='mt-1'
                        title={t(CONFLICT_TEXT[conflict])}
                      >
                        {t('Edit in list view')}
                      </Badge>
                    )}
                  </th>
                )
              })}
            </tr>
          </thead>
          <tbody>
            {groups.map((group) => (
              <Fragment key={group.key}>
                <tr>
                  <th
                    colSpan={grid.endpoints.length + 1}
                    className={cn(
                      'border-b px-2 py-1 text-left text-[11px] font-medium',
                      GROUP_STYLE[group.key]
                    )}
                  >
                    {t(GROUP_LABEL[group.key])}
                    <span className='ml-1 opacity-60'>
                      {group.models.length}
                    </span>
                  </th>
                </tr>
                {group.models.map((model) => (
                  <tr key={model}>
                    <td className='bg-background sticky left-0 z-10 border-r border-b p-2'>
                      {/* 行高按单行算，名字超长用 title 补全——行里再折行会让
                          10 个模型的行高参差不齐，反而更难扫 */}
                      <code
                        className='block max-w-[184px] truncate font-mono'
                        title={model}
                      >
                        {model}
                      </code>
                    </td>
                    {grid.endpoints.map((endpoint) => (
                      <td key={endpoint} className='border-b p-1 text-center'>
                        <CellControl
                          endpoint={endpoint}
                          model={model}
                          mode={cellAt(grid, endpoint, model)?.mode ?? 'off'}
                          disabled={!isCellEditable(grid, endpoint)}
                          onToggle={() =>
                            apply(toggleCell(grid, endpoint, model))
                          }
                          onPick={(mode) =>
                            apply(setCellMode(grid, endpoint, model, mode))
                          }
                        />
                      </td>
                    ))}
                  </tr>
                ))}
              </Fragment>
            ))}
          </tbody>
          <tfoot>
            <tr>
              <td className='bg-muted text-muted-foreground sticky left-0 z-10 border-t border-r p-2 text-[11px]'>
                {t('Per endpoint')}
              </td>
              {grid.endpoints.map((endpoint) => {
                const chosen = supportedModels(grid, endpoint)
                const all = chosen.length >= ordered.length
                return (
                  <td
                    key={endpoint}
                    className='bg-muted border-t p-1 text-center'
                  >
                    <Button
                      type='button'
                      variant='ghost'
                      size='sm'
                      disabled={!isCellEditable(grid, endpoint)}
                      onClick={() =>
                        apply(
                          fillEndpoint(grid, endpoint, all ? 'off' : 'none')
                        )
                      }
                    >
                      {all ? t('None') : t('All')}
                    </Button>
                  </td>
                )
              })}
            </tr>
          </tfoot>
        </table>
      </div>

      <div className='text-muted-foreground flex flex-wrap items-center gap-x-4 gap-y-1 text-xs'>
        <span>
          {t('Routes generated')}: {generated.length}
        </span>
        <span className='inline-flex items-center gap-1'>
          <ArrowRight className='size-3' aria-hidden='true' />
          {t('Native forwarding')}
        </span>
        <span className='inline-flex items-center gap-1'>
          <span className='rounded border border-sky-600/50 px-1 text-[9px] leading-4 font-semibold text-sky-700 dark:text-sky-400'>
            ANT
          </span>
          {t('Converted — the tag names the target protocol')}
        </span>
        <span className='inline-flex items-center gap-1'>
          <Minus className='size-3' aria-hidden='true' />
          {t('Not supported')}
        </span>
        {orphans > 0 && (
          <span>
            {t('Kept from the current config')}: {orphans}
          </span>
        )}
      </div>
    </div>
  )
}

/**
 * 一格。
 *
 * 左半边是「点亮/关闭」，右半边小箭头选转换器——转换器是少数操作，
 * 不该让常用的一次点击变成两次。两个按钮是兄弟节点，不能嵌套（HTML 不允许）。
 */
function CellControl({
  endpoint,
  model,
  mode,
  disabled,
  onToggle,
  onPick,
}: {
  endpoint: string
  model: string
  mode: GridCellMode
  disabled: boolean
  onToggle: () => void
  onPick: (mode: GridCellMode) => void
}) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const isOff = mode === 'off'
  const on = !isOff
  const converted = on && mode !== 'none'
  // 选完立刻收起来。Popover 不会因为点了内容里的按钮就自动关，
  // 让用户再点一次外面是多余的。
  const pick = (next: GridCellMode) => {
    onPick(next)
    setOpen(false)
  }
  const converters = useMemo(
    () => converterOptionsForEndpoint(endpoint),
    [endpoint]
  )

  return (
    <div
      className={cn(
        'inline-flex items-stretch overflow-hidden rounded border transition-colors',
        !on && 'border-border',
        mode === 'none' && 'border-emerald-600/40',
        converted && 'border-sky-600/50',
        disabled && 'opacity-40'
      )}
    >
      <button
        type='button'
        disabled={disabled}
        aria-pressed={on}
        title={
          converted
            ? converterLabel(mode)
            : on
              ? `${t('Native forwarding')} · ${endpoint} · ${model}`
              : `${t('Not supported')} · ${endpoint} · ${model}`
        }
        onClick={onToggle}
        className={cn(
          'flex h-7 min-w-7 items-center justify-center px-1',
          mode === 'none' &&
            'bg-emerald-500/15 text-emerald-700 dark:text-emerald-400',
          converted && 'bg-sky-500/15 text-sky-700 dark:text-sky-400',
          !on && 'text-muted-foreground/30',
          disabled ? 'cursor-not-allowed' : 'cursor-pointer'
        )}
      >
        {converted ? (
          <span className='text-[9px] leading-none font-semibold tracking-tight'>
            {converterTag(mode)}
          </span>
        ) : on ? (
          <ArrowRight className='size-4' aria-hidden='true' />
        ) : (
          <Minus className='size-3' aria-hidden='true' />
        )}
      </button>

      {on && (
        <Popover open={open} onOpenChange={setOpen}>
          <PopoverTrigger
            render={
              <button
                type='button'
                disabled={disabled}
                aria-label={t('Converter')}
                className={cn(
                  'flex w-4 items-center justify-center border-l',
                  mode === 'none'
                    ? 'border-emerald-600/30 text-emerald-700 dark:text-emerald-400'
                    : 'border-sky-600/40 text-sky-700 dark:text-sky-400',
                  disabled ? 'cursor-not-allowed' : 'cursor-pointer'
                )}
              />
            }
          >
            <ChevronDown className='size-3' aria-hidden='true' />
          </PopoverTrigger>
          <PopoverContent align='center' side='bottom' className='w-72 p-1'>
            <PopoverTitle className='text-muted-foreground px-2 py-1.5 text-xs font-normal'>
              <code className='font-mono'>{endpoint}</code> ·{' '}
              <code className='font-mono'>{model}</code>
            </PopoverTitle>
            <div className='flex flex-col'>
              <ModeOption
                selected={mode === 'none'}
                icon={<ArrowRight className='size-3.5' aria-hidden='true' />}
                label={t('Native forwarding')}
                hint={t('Forward the request unchanged')}
                onSelect={() => pick('none')}
              />
              {converters.map((converter) => (
                <ModeOption
                  key={converter}
                  selected={mode === converter}
                  icon={
                    <ArrowLeftRight className='size-3.5' aria-hidden='true' />
                  }
                  label={converterLabel(converter)}
                  hint={
                    getAdvancedCustomConverterDefaults(converter, endpoint)
                      .upstream_path
                  }
                  onSelect={() => pick(converter)}
                />
              ))}
              {converters.length === 0 && (
                <p className='text-muted-foreground px-2 py-1.5 text-xs'>
                  {t('No converter serves this endpoint')}
                </p>
              )}
              <ModeOption
                selected={isOff}
                icon={<Minus className='size-3.5' aria-hidden='true' />}
                label={t('Not supported')}
                hint={t('No route is generated, requests get 404')}
                onSelect={() => pick('off')}
              />
            </div>
          </PopoverContent>
        </Popover>
      )}
    </div>
  )
}

function ModeOption({
  selected,
  icon,
  label,
  hint,
  onSelect,
}: {
  selected: boolean
  icon: ReactNode
  label: string
  hint: string
  onSelect: () => void
}) {
  return (
    <button
      type='button'
      onClick={onSelect}
      className={cn(
        'flex items-start gap-2 rounded px-2 py-1.5 text-left outline-none transition-colors',
        selected
          ? 'bg-primary/10 text-primary'
          : 'hover:bg-muted focus-visible:ring-1 focus-visible:ring-foreground/15'
      )}
    >
      <span className='mt-0.5 shrink-0'>{icon}</span>
      <span className='flex min-w-0 flex-col'>
        <span className='text-xs font-medium'>{label}</span>
        <span className='text-muted-foreground truncate font-mono text-[11px]'>
          {hint}
        </span>
      </span>
      {selected && (
        <Check className='mt-0.5 size-3.5 shrink-0' aria-hidden='true' />
      )}
    </button>
  )
}
