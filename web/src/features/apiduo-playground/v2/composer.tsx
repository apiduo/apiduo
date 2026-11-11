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
import {
  ArrowUpIcon,
  CheckIcon,
  ChevronDownIcon,
  ImageIcon,
  MessageSquareIcon,
  PaperclipIcon,
  PlusIcon,
  Trash2Icon,
  VideoIcon,
  XIcon,
} from 'lucide-react'
import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import type { ModelOption } from '@/features/playground/types'

import type { PlaygroundMode } from '../types'

import type { RefImage } from './types'

import {
  capabilityFor,
  RATIOS,
  RATIO_SECTIONS,
  ratioOf,
  RESOLUTIONS,
  sizeFor,
} from './capabilities'
import { FluidPopover } from './popover'
import { ProviderBadge, ProviderMark } from './provider-mark'

const MODES: Array<{
  value: PlaygroundMode
  label: string
  icon: typeof MessageSquareIcon
}> = [
  { value: 'chat', label: 'Chat', icon: MessageSquareIcon },
  { value: 'image', label: 'Image', icon: ImageIcon },
  { value: 'video', label: 'Video', icon: VideoIcon },
]

interface ComposerProps {
  models: ModelOption[]
  model: string
  onModelChange: (model: string) => void
  /** 当前工作区。模型下拉头部用它画三档切换。 */
  mode: PlaygroundMode
  onModeChange: (mode: PlaygroundMode) => void
  /** 每个模式下有几个模型，标签上的数字。 */
  modeCounts: Record<PlaygroundMode, number>
  /** 计费分组：价格 chip 点开就是它，原型也是把分组挂在价格上。 */
  groups: Array<{ label: string; value: string; ratio: number | string }>
  onGroupChange: (group: string) => void
  /* The next four only exist for video: a clip is measured in seconds and
     lines, so those two choices fit inline as segments. */
  videoDurations?: ReadonlyArray<number>
  videoDuration?: number
  onVideoDurationChange?: (value: number) => void
  videoResolutions?: ReadonlyArray<string>
  videoResolution?: string
  onVideoResolutionChange?: (value: string) => void
  /** 流式生成中：主按钮变成「停止」。 */
  onStop?: () => void
  /** 聊天/视频暂时不收参考图：入口整块不出现，而不是给个点了没用的框。 */
  allowRefs?: boolean
  /** 覆盖参考图容量：聊天模型不在图片能力表里，由工作区声明。 */
  refCap?: number
  /** 运营把模型标成「新」时，行里加一个 NEW 角标。 */
  isNew?: (model: string) => boolean
  /* A clip's references are positional, not a bag: the first image opens the
     shot and the second closes it, so they get two named slots instead of a
     row of interchangeable tiles. */
  videoFrames?: { first: RefImage | null; last: RefImage | null }
  onVideoFrame?: (slot: 'first' | 'last', file: File | null) => void
  /* Chat has one shape knob the prototype keeps on the bar: how literal the
     answer should be. */
  temperatures?: ReadonlyArray<number>
  temperature?: number
  onTemperatureChange?: (value: number) => void
  /* The other chat knob: how much of the conversation goes on the wire. */
  contexts?: ReadonlyArray<number>
  context?: number
  onContextChange?: (value: number) => void
  /** 价格的单位与写法：图片按张、视频按条、聊天按百万 token。 */
  priceUnit?: string
  priceApprox?: boolean
  prompt: string
  onPromptChange: (prompt: string) => void
  ratio: string
  onRatioChange: (ratio: string) => void
  resolution: string
  onResolutionChange: (resolution: string) => void
  count: number
  onCountChange: (count: number) => void
  busy: boolean
  onSubmit: () => void
  /** Formatted per-image price, already group- and discount-adjusted. */
  priceLabel: string | null
  /** Billing group the price was quoted for. */
  group: string
  /** Unit price of any model in the list, for the picker's own receipt. */
  priceOf: (model: string) => string | null
  /** What N images of the current model cost, formatted the same way. */
  totalOf: (count: number) => string | null
  /** The operator's one-liner for a model, or null when none was written. */
  descOf: (model: string) => string | null
  /** 参考图袋子：composer 只渲染与收集，持有与撤销在 stage。 */
  refs: RefImage[]
  /** 换模型裁掉参考图时的说明；非空时压住底部提示行。 */
  refNote: string
  onAddRefs: (files: File[]) => void
  onRemoveRef: (id: string) => void
  placeholder: string
  /** Generations in flight and the ceiling for this mode. */
  running: number
  cap: number
  /** Clear only makes sense once something has been generated. */
  hasResults: boolean
  onClear: () => void
}

/* The box the ratio chip and the popover tiles share. A 21:9 frame cannot be
   drawn literally at this size, so the long side is compressed: the point is
   which side is longer, not the exact proportion. */
function shapeOf(ar: number, auto?: boolean, size = 15) {
  return {
    width: ar >= 1 ? size : Math.max(4, Math.round(size * ar)),
    height: ar >= 1 ? Math.max(4, Math.round(size / ar)) : size,
    borderStyle: auto ? 'dashed' : 'solid',
  } as const
}

/* Two facts a name cannot carry: how many shapes the model accepts and how
   large it can go. Both come from the same table the pickers read, so a row
   cannot promise a ratio the chip beside it will not offer. */
function factsOf(model: string): { ratios: number; sizes: string; refs: number } {
  const capability = capabilityFor(model)
  const offered = RESOLUTIONS.filter((item) =>
    capability.resolutions.includes(item.id)
  )
  return {
    // `auto` is a state, not a shape: counting it would make every model that
    // offers 1:1 and 16:9 claim one more ratio than it has.
    ratios: capability.ratios.filter((id) => id !== 'auto').length,
    sizes:
      offered.length > 1
        ? `${offered[0].label}–${offered.at(-1)?.label ?? ''}`
        : (offered[0]?.label ?? ''),
    refs: capability.maxRefs,
  }
}

interface TileProps {
  active: boolean
  /** Frame to draw above the label. Omitted where the value is not a shape. */
  ar?: number
  label: string
  /** Second line: "follow the prompt", or the pixel count of a resolution. */
  sub?: string
  /** `auto` is a row of its own, not a cell in the shape grid. */
  auto?: boolean
  supported?: boolean
  /** Why the tile is dim, so the tooltip answers instead of the click. */
  why?: string
  onClick: () => void
}

function Tile({
  active,
  ar,
  label,
  sub,
  auto,
  supported = true,
  why,
  onClick,
}: TileProps) {
  return (
    <button
      aria-disabled={!supported}
      aria-pressed={active}
      className={auto ? 'apg-tile apg-tile-auto' : 'apg-tile'}
      onClick={() => {
        if (supported) onClick()
      }}
      title={supported ? label : why}
      type='button'
    >
      {ar == null ? null : (
        <span aria-hidden='true' className='apg-tile-box'>
          <i
            className={auto ? 'dash' : undefined}
            style={shapeOf(ar, auto, auto ? 18 : 20)}
          />
        </span>
      )}
      <span className='apg-tile-label'>{label}</span>
      {sub ? <span className='apg-tile-sub'>{sub}</span> : null}
    </button>
  )
}

export function Composer({
  models,
  model,
  onModelChange,
  mode,
  onModeChange,
  modeCounts,
  groups,
  onGroupChange,
  videoDurations,
  videoDuration,
  onVideoDurationChange,
  videoResolutions,
  videoResolution,
  onVideoResolutionChange,
  onStop,
  allowRefs = true,
  refCap,
  isNew,
  videoFrames,
  onVideoFrame,
  temperatures,
  temperature,
  onTemperatureChange,
  contexts,
  context,
  onContextChange,
  priceUnit,
  priceApprox = true,
  prompt,
  onPromptChange,
  ratio,
  onRatioChange,
  resolution,
  onResolutionChange,
  count,
  onCountChange,
  busy,
  onSubmit,
  priceLabel,
  group,
  priceOf,
  totalOf,
  descOf,
  refs,
  refNote,
  onAddRefs,
  onRemoveRef,
  placeholder,
  running,
  cap,
  hasResults,
  onClear,
}: ComposerProps) {
  const { t } = useTranslation()
  const textareaRef = useRef<HTMLTextAreaElement | null>(null)
  const surfaceRef = useRef<HTMLDivElement | null>(null)
  const [open, setOpen] = useState<
    'model' | 'ratio' | 'res' | 'count' | 'group' | 'temp' | 'ctx' | null
  >(null)
  const [anchor, setAnchor] = useState<HTMLElement | null>(null)

  const capability = capabilityFor(model)
  const supports = (list: string[], id: string) => list.includes(id)
  // Unsupported entries are dimmed rather than dropped: the shape space has to
  // stay the same size when the model changes, so the reader can see what was
  // lost instead of watching the grid rearrange itself.
  const dimmedRatios = RATIOS.filter(
    (item) => !item.auto && !supports(capability.ratios, item.id)
  ).length
  const maxCount = capability.maxCount
  const size = sizeFor(ratio, resolution)
  /* The request needs `1024x1024`; the screen needs `1024×1024`. One is a wire
     format and the other is typography, so the split happens here rather than
     at every call site. */
  const shownSize = size?.replace('x', '×')
  const currentRatio = ratioOf(ratio)
  const autoSupported = supports(capability.ratios, 'auto')
  const currentRes = RESOLUTIONS.find((item) => item.id === resolution)

  const openAt = useCallback(
    (
      kind: 'model' | 'ratio' | 'res' | 'count' | 'group' | 'temp' | 'ctx',
      node: HTMLElement
    ) => {
      setAnchor(node)
      setOpen((current) => (current === kind ? null : kind))
    },
    []
  )

  const submit = useCallback(() => {
    if (busy || !prompt.trim()) return
    onSubmit()
  }, [busy, onSubmit, prompt])

  /* 参考图。文件从三个方向进来：点加号（file input）、剪贴板粘贴、
     拖到整个 composer 上。前两个是显式入口，第三个没有可视的落点，
     所以拖拽期间整个 composer 变成目标（drop-hint 蒙层）。 */
  const maxRefs = allowRefs ? (refCap ?? capability.maxRefs) : 0
  const hasFrameSlots = mode === 'video' && Boolean(onVideoFrame)
  const frameCount =
    (videoFrames?.first ? 1 : 0) + (videoFrames?.last ? 1 : 0)
  const framePicker = useRef<'first' | 'last' | null>(null)
  /* Files land in the first empty slot, in arrival order, so pasting two
     images fills 首帧 and 尾帧 without a second gesture. */
  const fillFrames = useCallback(
    (files: File[]) => {
      if (!onVideoFrame) return
      const images = files.filter((file) => file.type.startsWith('image/'))
      if (images.length === 0) return
      const empty: Array<'first' | 'last'> = []
      if (!videoFrames?.first) empty.push('first')
      if (!videoFrames?.last) empty.push('last')
      images.slice(0, empty.length).forEach((file, index) => {
        const slot = empty[index]
        if (slot) onVideoFrame(slot, file)
      })
    },
    [onVideoFrame, videoFrames]
  )
  const [dropping, setDropping] = useState(false)
  const dragDepth = useRef(0)
  const fileInputRef = useRef<HTMLInputElement | null>(null)
  const [freshId, setFreshId] = useState<string | null>(null)

  const attachFiles = useCallback(
    (files: File[]) => {
      if (hasFrameSlots) {
        fillFrames(files)
        return
      }
      const images = files.filter((file) => file.type.startsWith('image/'))
      if (images.length === 0 || maxRefs <= 0) return
      setFreshId(null)
      onAddRefs(images.slice(0, Math.max(0, maxRefs - refs.length)))
    },
    [fillFrames, hasFrameSlots, maxRefs, onAddRefs, refs.length]
  )
  // 最新一片要有一个入场动画的落点：不知道哪片是新来的，46px 的格子里
  // 「什么都没发生」就是用户学到的第一课。
  useEffect(() => {
    if (refs.length === 0) {
      setFreshId(null)
      return
    }
    const last = refs.at(-1)
    if (!last) return
    setFreshId((current) =>
      current && refs.some((item) => item.id === current) ? current : last.id
    )
  }, [refs])

  useEffect(() => {
    if (maxRefs <= 0 && !hasFrameSlots) return
    const onPaste = (event: ClipboardEvent) => {
      const files: File[] = []
      for (const item of event.clipboardData?.items ?? []) {
        if (item.kind === 'file') {
          const file = item.getAsFile()
          if (file && file.type.startsWith('image/')) files.push(file)
        }
      }
      if (files.length) {
        event.preventDefault()
        attachFiles(files)
      }
    }
    document.addEventListener('paste', onPaste)
    return () => document.removeEventListener('paste', onPaste)
  }, [attachFiles, hasFrameSlots, maxRefs])

  const onDrop = useCallback(
    (event: React.DragEvent) => {
      event.preventDefault()
      dragDepth.current = 0
      setDropping(false)
      const files: File[] = []
      if (event.dataTransfer.items) {
        for (const item of event.dataTransfer.items) {
          if (item.kind === 'file') {
            const file = item.getAsFile()
            if (file) files.push(file)
          }
        }
      }
      if (files.length === 0) files.push(...event.dataTransfer.files)
      attachFiles(files)
    },
    [attachFiles]
  )

  const currentModel = models.find((item) => item.value === model)
  const total = totalOf(count)
  // 三个状态一句话：满了说满，有图报数，空袋子教入口。嵌套三元在这里
  // 会把三种读者塞进一行表达式，读的人要自己展开。
  /* One sentence per mode, assembled before the JSX: the nesting a ternary
     needed here was three levels deep and read as one long line. */
  const enterLine = `${t('Enter')} ${t('Generate')}`
  const shiftLine = `Shift + Enter ${t('Line break')}`
  let hintFacts: string[]
  if (mode === 'chat') {
    hintFacts = [
      refs.length > 0 ? t('Image chat') : t('Text chat'),
      t('Model and group follow this message'),
      `${t('Enter')} ${t('Send')}`,
      shiftLine,
    ]
  } else if (mode === 'video') {
    // 0 / 1 / 2 张帧是三种不同的请求，提示行要说准是哪一种。
    let videoOp = t('Text to video')
    if (frameCount >= 2) videoOp = t('First and last frame')
    else if (frameCount === 1) videoOp = t('Image to video')
    hintFacts = [
      videoOp,
      t('Async render · lands in history when done'),
      enterLine,
      shiftLine,
    ]
  } else {
    hintFacts = [
      refs.length > 0 ? t('Image to image') : t('Text to image'),
      t('Parameters apply to this request only'),
      enterLine,
      shiftLine,
    ]
  }

  // 三种模式下「按什么计费」不是同一句话，标题也不能沿用图片模式那一句。
  let modelListTitle = t('Model · billed per image')
  if (mode === 'chat') modelListTitle = t('Chat models · per million tokens')
  else if (mode === 'video') modelListTitle = t('Video models · billed per clip')

  /* Video rows say what a clip is measured in; there is no ratio table for a
     model that renders seconds of footage. */
  const firstDuration = videoDurations?.[0]
  const lastDuration = videoDurations?.at(-1)
  const videoSpan =
    firstDuration !== undefined && lastDuration !== undefined && firstDuration !== lastDuration
      ? `${firstDuration}s–${lastDuration}s`
      : ''
  const videoFact = [videoSpan, t('First and last frame')]
    .filter(Boolean)
    .join(' · ')

  let refCapTitle: string | undefined
  if (hasFrameSlots) {
    refCapTitle = t('You can fill just the first frame, or both')
  } else if (maxRefs > 0) {
    refCapTitle = t('Up to {{n}} reference images', { n: maxRefs })
  }

  let refCapText = ''
  if (hasFrameSlots) {
    refCapText = frameCount > 0 ? `${frameCount}/2` : t('Paste or drop images')
  }

  let refCaption: string
  if (maxRefs > 0 && refs.length >= maxRefs) {
    refCaption = t('Full · {{n}} images', { n: maxRefs })
  } else if (refs.length > 0) {
    refCaption = `${refs.length}/${maxRefs}`
  } else {
    refCaption = t('Paste or drop images')
  }

  return (
    <footer className='apg-composer'>
      <div
        className={`apg-composer-inner${dropping ? ' dropping' : ''}`}
        onDragEnter={() => {
          if (maxRefs <= 0 && !hasFrameSlots) return
          dragDepth.current += 1
          setDropping(true)
        }}
        onDragLeave={() => {
          if (maxRefs <= 0 && !hasFrameSlots) return
          dragDepth.current -= 1
          if (dragDepth.current <= 0) setDropping(false)
        }}
        onDragOver={(event) => {
          if (maxRefs <= 0) return
          event.preventDefault()
          event.dataTransfer.dropEffect = 'copy'
        }}
        onDrop={onDrop}
        ref={surfaceRef}
      >
        {/* Files arrive three ways — picker, clipboard, drag. The picker has a
            tile to aim at; a dragged screenshot does not, so the whole
            composer turns into the target while a file is over it. */}
        <div aria-hidden='true' className='apg-drop-hint'>
          {t('Drop to add references')}
        </div>
        <input
          accept='image/*'
          className='apg-ref-input'
          multiple={maxRefs > 1}
          onChange={(event) => {
            const picked = [...(event.target.files ?? [])]
            const target = framePicker.current
            framePicker.current = null
            if (target) {
              const file = picked.find((item) =>
                item.type.startsWith('image/')
              )
              if (file) onVideoFrame?.(target, file)
            } else {
              attachFiles(picked)
            }
            // 同名同尺寸的文件第二次选择也要触发：清空 value 是唯一办法。
            event.target.value = ''
          }}
          ref={fileInputRef}
          type='file'
        />
        {/* The reference row is part of the request, not part of how it is
            rendered: a file changes what the request *is* (文生图 → 图生图).
            Every chip below only changes how it is drawn, so the files sit
            above the text they belong to, not in the parameter row. */}
        {hasFrameSlots || (mode === 'chat' ? refs.length > 0 : maxRefs > 0) || refs.length > 0 ? (
          <div aria-label={t('References')} className='apg-refs'>
            <div className='apg-ref-row'>
              {hasFrameSlots ? (
                <>
                  {(['first', 'last'] as const).map((slot) => {
                    const frame = videoFrames?.[slot] ?? null
                    const label =
                      slot === 'first' ? t('First frame') : t('Last frame')
                    if (!frame) {
                      return (
                        <button
                          className='apg-ref-add'
                          key={slot}
                          onClick={() => {
                            framePicker.current = slot
                            fileInputRef.current?.click()
                          }}
                          title={t('Choose {{slot}}, or paste it', {
                            slot: label,
                          })}
                          type='button'
                        >
                          <PlusIcon />
                          <span>{label}</span>
                        </button>
                      )
                    }
                    return (
                      <div className='apg-ref apg-ref-slot' key={slot}>
                        {/* A filled slot is its own replace button: with both
                            frames in place there is no "+" left to aim at. */}
                        <button
                          aria-label={label}
                          className='apg-ref-pick'
                          onClick={() => {
                            framePicker.current = slot
                            fileInputRef.current?.click()
                          }}
                          title={t('Replace {{slot}}', { slot: label })}
                          type='button'
                        >
                          <img alt='' src={frame.url} />
                          <span className='apg-ref-slot-label'>{label}</span>
                        </button>
                        <button
                          aria-label={t('Remove reference')}
                          className='apg-ref-x'
                          onClick={() => onVideoFrame?.(slot, null)}
                          title={t('Remove reference')}
                          type='button'
                        >
                          <XIcon />
                        </button>
                      </div>
                    )
                  })}
                </>
              ) : null}
              {hasFrameSlots ? null : refs.map((ref) => (
                <div
                  className={`apg-ref${ref.id === freshId ? ' fresh' : ''}`}
                  key={ref.id}
                >
                  <img alt='' src={ref.url} />
                  <button
                    aria-label={t('Remove reference')}
                    className='apg-ref-x'
                    onClick={() => onRemoveRef(ref.id)}
                    title={t('Remove reference')}
                    type='button'
                  >
                    <XIcon />
                  </button>
                </div>
              ))}
              {!hasFrameSlots &&
              mode !== 'chat' &&
              maxRefs > 0 &&
              refs.length < maxRefs ? (
                <button
                  className='apg-ref-add'
                  onClick={() => fileInputRef.current?.click()}
                  title={t('Add references')}
                  type='button'
                >
                  <PlusIcon />
                  <span>{t('References')}</span>
                </button>
              ) : null}
            </div>
            <span
              className={`apg-ref-cap${
              !hasFrameSlots && maxRefs > 0 && refs.length >= maxRefs
                ? ' full'
                : ''
              }`}
              title={refCapTitle}
            >
              {hasFrameSlots ? refCapText : refCaption}
            </span>
          </div>
        ) : null}
        <textarea
          aria-label={t('Prompt')}
          className='apg-prompt'
          onChange={(event) => onPromptChange(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === 'Enter' && !event.shiftKey) {
              event.preventDefault()
              submit()
            }
          }}
          placeholder={placeholder}
          ref={textareaRef}
          rows={2}
          spellCheck={false}
          value={prompt}
        />

        <div className='apg-bar'>
          {mode === 'chat' && maxRefs > 0 ? (
            <button
              aria-label={t('Attach images')}
              className='apg-attach'
              onClick={() => fileInputRef.current?.click()}
              title={t('Attach images')}
              type='button'
            >
              <PaperclipIcon />
            </button>
          ) : null}
          <button
            aria-haspopup='dialog'
            aria-label={t('Model')}
            className='apg-chip apg-chip-model'
            onClick={(event) => openAt('model', event.currentTarget)}
            title={model}
            type='button'
          >
            <ProviderMark fallback={<ImageIcon />} model={model} size={13} />
            <span className='name'>{currentModel?.label ?? model}</span>
            <ChevronDownIcon className='chev' />
          </button>

          {mode === 'chat' && temperatures && temperatures.length > 0 ? (
            <button
              aria-haspopup='dialog'
              aria-label={t('Temperature')}
              className='apg-chip'
              onClick={(event) => openAt('temp', event.currentTarget)}
              type='button'
            >
              <span className='apg-chip-dim'>{t('Temperature')}</span>
              <span className='name mono'>{(temperature ?? 0).toFixed(1)}</span>
              <ChevronDownIcon className='chev' />
            </button>
          ) : null}

          {mode === 'chat' && contexts && contexts.length > 0 ? (
            <button
              aria-haspopup='dialog'
              aria-label={t('Context')}
              className='apg-chip'
              onClick={(event) => openAt('ctx', event.currentTarget)}
              type='button'
            >
              <span className='apg-chip-dim'>{t('Context')}</span>
              <span className='name mono'>
                {Math.round((context ?? contexts[0]) / 1024)}k
              </span>
              <ChevronDownIcon className='chev' />
            </button>
          ) : null}

          {mode === 'video' ? (
            <>
              <div className='apg-seg' role='group'>
                {(videoDurations ?? []).map((value) => (
                  <button
                    aria-pressed={value === videoDuration}
                    className='apg-seg-btn'
                    key={value}
                    onClick={() => onVideoDurationChange?.(value)}
                    type='button'
                  >
                    {value}s
                  </button>
                ))}
              </div>
              <div className='apg-seg' role='group'>
                {(videoResolutions ?? []).map((value) => (
                  <button
                    aria-pressed={value === videoResolution}
                    className='apg-seg-btn'
                    key={value}
                    onClick={() => onVideoResolutionChange?.(value)}
                    type='button'
                  >
                    {value}
                  </button>
                ))}
              </div>
            </>
          ) : null}

          {mode !== 'image' ? null : (
          <>
          <button
            aria-haspopup='dialog'
            aria-label={t('Aspect ratio')}
            className='apg-chip'
            onClick={(event) => openAt('ratio', event.currentTarget)}
            type='button'
          >
            {/* A 16px picture of the frame: 1:1 and 16:9 are one character
                apart in text and nothing alike as shapes. */}
            <span aria-hidden='true' className='apg-shape'>
              <i style={shapeOf(currentRatio.ar, currentRatio.auto)} />
            </span>
            <span className='name mono'>
              {ratio === 'auto' ? t('Auto') : ratio}
            </span>
            <ChevronDownIcon className='chev' />
          </button>

          <button
            aria-haspopup='dialog'
            aria-label={t('Resolution')}
            className='apg-chip'
            onClick={(event) => openAt('res', event.currentTarget)}
            type='button'
          >
            <span className='name mono'>{currentRes?.label}</span>
            <ChevronDownIcon className='chev' />
          </button>
          </>
          )}

          {/* Price and the primary action travel together: once the chips wrap
              on a narrow screen they stay right-aligned as one block instead
              of stranding the amount at the far left under the avatar. */}
          <div className='apg-bar-end'>
            {/* "how many" is a multiplier on "how much", so it belongs on the
                receipt side of the row — and it keeps the price from being the
                only thing pushed onto a second line on a narrow screen. */}
            {mode === 'image' ? (
            <button
              aria-haspopup='dialog'
              aria-label={t('How many')}
              className='apg-chip'
              onClick={(event) => openAt('count', event.currentTarget)}
              type='button'
            >
              <span className='name mono'>×{count}</span>
              <ChevronDownIcon className='chev' />
            </button>
            ) : null}

            {priceLabel ? (
              /* Unit price, always. Quoting the total with a "/image" suffix
                 prints a number that is neither the price of one nor of the
                 batch; the count chip sits next to it, so the multiplication
                 is on screen. The group rides the number because the price is
                 the only place it has a visible consequence. */
              <button
                aria-haspopup='dialog'
                aria-label={t('Billing group')}
                className='apg-price'
                onClick={(event) => openAt('group', event.currentTarget)}
                title={t('Billing group')}
                type='button'
              >
                {priceApprox ? <span aria-hidden='true'>≈</span> : null}
                <b>{priceLabel}</b>
                <span>/{priceUnit ?? t('per image')}</span>
                <span className='apg-price-grp'>· {group}</span>
                <ChevronDownIcon className='chev' />
              </button>
            ) : null}

            {/* The prototype keeps "clear" for the image shelf only: a chat
                turn is deleted one message at a time, and a clip has its own
                delete on the card. */}
            {mode === 'image' ? (
              <button
                aria-label={t('Clear results')}
                className='apg-clear'
                disabled={!hasResults}
                onClick={onClear}
                title={t('Clear results')}
                type='button'
              >
                <Trash2Icon />
              </button>
            ) : null}

            {/* A chat turn holds the composer while it streams, so there is
                nothing to count; the pill exists where jobs run in parallel. */}
            {mode === 'chat' ? null : (
              <span
                className='apg-conc'
                data-live={running > 0}
                role='status'
                title={t('{{running}} running · this mode allows {{cap}} at once', {
                  running,
                  cap,
                })}
              >
                <i aria-hidden='true' />
                <b>
                  {running}/{cap}
                </b>
              </span>
            )}

            {/* Never disabled for an empty prompt, only for a saturated
                engine: a 42 % blue button in the default state reads as "this
                page cannot do anything", and the click already no-ops. */}
            {busy && onStop ? (
              <button className='apg-send apg-stop' onClick={onStop} type='button'>
                {t('Stop')}
              </button>
            ) : (
              <button
                className='apg-send'
                disabled={busy}
                onClick={submit}
                type='button'
              >
                {mode === 'chat' ? t('Send') : t('Generate')}
                <ArrowUpIcon />
              </button>
            )}
          </div>
        </div>
      </div>

      <p className={`apg-hint${refNote ? ' warn' : ''}`} role={refNote ? 'status' : undefined}>
        {refNote || hintFacts.join(' · ')}
      </p>

      <FluidPopover
        anchor={anchor}
        boundary={surfaceRef.current}
        label={t('Model')}
        onClose={() => setOpen(null)}
        open={open === 'model'}
        width={332}
      >
        {/* Type and model are one decision made twice, so the switch sits on
            top of the list it filters instead of one screen away. */}
        <div className='apg-pop-tabbar'>
          <div className='apg-pop-tabs' role='tablist'>
            <span
              aria-hidden='true'
              className='apg-pop-thumb'
              style={{
                transform: `translateX(calc(${MODES.findIndex((item) => item.value === mode)} * (100% + 2px)))`,
              }}
            />
            {MODES.map((item) => {
              const Icon = item.icon
              return (
                <button
                  aria-selected={item.value === mode}
                  className='apg-pop-tab'
                  key={item.value}
                  onClick={() => onModeChange(item.value)}
                  role='tab'
                  type='button'
                >
                  <Icon aria-hidden='true' />
                  <span>{t(item.label)}</span>
                  <span className='n'>{modeCounts[item.value]}</span>
                </button>
              )
            })}
          </div>
        </div>
        <div className='apg-pop-body'>
          {/* The list is about one kind of model and one way of paying for it;
              a bare "model" heading leaves the reader to work both out. */}
          <div className='apg-pop-label'>{modelListTitle}</div>
          {models.map((item) => {
            const facts = factsOf(item.value)
            const price = priceOf(item.value)
            let fallback = ''
            if (mode === 'image') {
              fallback = `${t('{{n}} ratios', { n: facts.ratios })}${
                facts.sizes ? ` · ${facts.sizes}` : ''
              }`
            } else if (mode === 'video') {
              fallback = videoFact
            }
            return (
              <button
                aria-selected={item.value === model}
                className='apg-row'
                key={item.value}
                onClick={() => {
                  onModelChange(item.value)
                  setOpen(null)
                }}
                role='option'
                type='button'
              >
                {/* A well, not a picture: at 20 px a bare glyph is a smudge
                    and a full-colour logo is four colours of noise. The tint
                    is what tells two rows from different labs apart before
                    either name is read. */}
                <ProviderBadge model={item.value} />
                <span className='apg-row-main'>
                  <span className='apg-row-which'>
                    <span className='apg-row-name'>{item.value}</span>
                    {/* `label` is the display name and equals `value` for most
                        providers; repeating it just makes every row look
                        doubled, so it only rides along when it adds a name the
                        id does not already spell out. */}
                    {item.label && item.label !== item.value ? (
                      <span className='apg-row-cap'>{item.label}</span>
                    ) : null}
                    {/* Absence is meaningful: a row without this tag cannot
                        take a file, which is the one thing the list has to
                        answer before the model is chosen. Video rows say what
                        the frames are (首尾帧), not how many there are. */}
                    {mode === 'image' && facts.refs > 0 ? (
                      <span className='apg-row-cap apg-row-refs'>
                        {t('{{n}} refs', { n: facts.refs })}
                      </span>
                    ) : null}
                    {mode === 'video' ? (
                      <span className='apg-row-cap apg-row-refs'>
                        {t('First and last frame')}
                      </span>
                    ) : null}
                    {isNew?.(item.value) ? (
                      <span className='apg-row-new'>NEW</span>
                    ) : null}
                  </span>
                  {/* The second line answers what a name cannot. An operator's
                      line says *why*; until one is written, the capability
                      facts say *what*, and a list of bare ids would say
                      nothing at all. */}
                  <span className='apg-row-desc'>
                    {descOf(item.value) ?? (fallback || null)}
                  </span>
                </span>
                {/* The list is a receipt too: a row that names a model but not
                    what it costs makes the user try it to find out. */}
                {price ? <span className='apg-row-price'>{price}</span> : null}
                {item.value === model ? (
                  <CheckIcon className='apg-check' />
                ) : null}
              </button>
            )
          })}
        </div>
      </FluidPopover>

      <FluidPopover
        anchor={anchor}
        boundary={surfaceRef.current}
        label={t('Aspect ratio')}
        onClose={() => setOpen(null)}
        open={open === 'ratio'}
        width={306}
      >
        {/* The count is the honest one: how many this model actually takes,
            not how many the grid is showing. */}
        <div className='apg-pop-head'>
          {t('Aspect ratio · {{n}}', { n: capability.ratios.length })}
        </div>
        <div className='apg-pop-body'>
          {/* "auto" answers a different question than the rest, so it gets a
              row of its own instead of a cell in the shape grid. */}
          {autoSupported ? (
            <Tile
              active={ratio === 'auto'}
              ar={1}
              auto
              label={t('Auto')}
              onClick={() => {
                onRatioChange('auto')
                setOpen(null)
              }}
              sub={t('Follow the prompt')}
            />
          ) : null}
          {RATIO_SECTIONS.map(([section, label]) => {
            const items = RATIOS.filter(
              (item) => !item.auto && item.section === section
            )
            if (items.length === 0) return null
            return (
              <div className='apg-enum-sec' key={section}>
                <div className='apg-enum-title'>{t(label)}</div>
                <div className='apg-tiles'>
                  {items.map((item) => {
                    const ok = supports(capability.ratios, item.id)
                    return (
                      <Tile
                        active={item.id === ratio}
                        ar={item.ar}
                        key={item.id}
                        label={item.id}
                        onClick={() => {
                          onRatioChange(item.id)
                          setOpen(null)
                        }}
                        supported={ok}
                        why={t('{{model}} does not support {{value}}', {
                          model,
                          value: item.id,
                        })}
                      />
                    )
                  })}
                </div>
              </div>
            )
          })}
        </div>
        {/* The footer says the two things the grid cannot: the pixels this
            combination actually produces, and why some tiles are dim. */}
        <div className='apg-pop-foot'>
          <span>{t('Output {{size}}', { size: shownSize ?? t('Auto') })}</span>
          {dimmedRatios ? (
            <span className='r'>{t('{{n}} dimmed', { n: dimmedRatios })}</span>
          ) : null}
        </div>
      </FluidPopover>

      <FluidPopover
        anchor={anchor}
        boundary={surfaceRef.current}
        label={t('Resolution')}
        onClose={() => setOpen(null)}
        open={open === 'res'}
        width={232}
      >
        <div className='apg-pop-head'>{t('Output resolution')}</div>
        <div className='apg-pop-body'>
          <div className='apg-tiles apg-tiles-2'>
            {RESOLUTIONS.map((item) => {
              const ok = supports(capability.resolutions, item.id)
              return (
                <Tile
                  active={item.id === resolution}
                  key={item.id}
                  label={item.label}
                  onClick={() => {
                    onResolutionChange(item.id)
                    setOpen(null)
                  }}
                  sub={`${t(item.note)} · ${Math.round(Math.sqrt(item.area))}px`}
                  supported={ok}
                  why={t('{{model}} does not support {{value}}', {
                    model,
                    value: item.label,
                  })}
                />
              )
            })}
          </div>
        </div>
        <div className='apg-pop-foot'>
          <span>{t('Output {{size}}', { size: shownSize ?? t('Auto') })}</span>
          {priceLabel ? (
            <span className='r'>
              {priceLabel} / {t('per image')}
            </span>
          ) : null}
        </div>
      </FluidPopover>

      <FluidPopover
        anchor={anchor}
        boundary={surfaceRef.current}
        label={t('How many')}
        onClose={() => setOpen(null)}
        open={open === 'count'}
        width={214}
      >
        <div className='apg-pop-head'>{t('How many images')}</div>
        <div className='apg-pop-body'>
          <div className='apg-picks'>
            {Array.from({ length: maxCount }, (_, index) => index + 1).map(
              (value) => (
                <button
                  aria-pressed={value === count}
                  className='apg-pick'
                  key={value}
                  onClick={() => {
                    onCountChange(value)
                    setOpen(null)
                  }}
                  type='button'
                >
                  ×{value}
                </button>
              )
            )}
          </div>
        </div>
        {/* The chip quotes the unit price, so the popover is the one place the
            multiplication is spelled out. */}
        <div className='apg-pop-foot'>
          <span>{t('{{n}} images', { n: count })}</span>
          {total ? <span className='r'>{t('Total {{amount}}', { amount: total })}</span> : null}
        </div>
      </FluidPopover>

      <FluidPopover
        anchor={anchor}
        boundary={surfaceRef.current}
        label={t('Temperature')}
        onClose={() => setOpen(null)}
        open={open === 'temp'}
        width={214}
      >
        <div className='apg-pop-head'>{t('Temperature')}</div>
        <div className='apg-pop-body'>
          <div className='apg-picks'>
            {(temperatures ?? []).map((value) => (
              <button
                aria-pressed={value === temperature}
                className='apg-pick'
                key={value}
                onClick={() => {
                  onTemperatureChange?.(value)
                  setOpen(null)
                }}
                type='button'
              >
                {value.toFixed(1)}
              </button>
            ))}
          </div>
        </div>
      </FluidPopover>

      <FluidPopover
        anchor={anchor}
        boundary={surfaceRef.current}
        label={t('Context')}
        onClose={() => setOpen(null)}
        open={open === 'ctx'}
        width={214}
      >
        <div className='apg-pop-head'>{t('Context')}</div>
        <div className='apg-pop-body'>
          <div className='apg-picks'>
            {(contexts ?? []).map((value) => (
              <button
                aria-pressed={value === context}
                className='apg-pick'
                key={value}
                onClick={() => {
                  onContextChange?.(value)
                  setOpen(null)
                }}
                type='button'
              >
                {Math.round(value / 1024)}k
              </button>
            ))}
          </div>
        </div>
      </FluidPopover>

      {/* The group is not a sibling of the model: it never changes what gets
          generated, only what it costs, so it opens from the number whose one
          visible consequence it is. */}
      <FluidPopover
        anchor={anchor}
        boundary={surfaceRef.current}
        label={t('Billing group')}
        onClose={() => setOpen(null)}
        open={open === 'group'}
        width={236}
      >
        <div className='apg-pop-head'>{t('Billing group')}</div>
        <div className='apg-pop-body'>
          {groups.map((item) => {
            const ratio = Number(item.ratio)
            return (
              <button
                aria-selected={item.value === group}
                className='apg-row'
                key={item.value}
                onClick={() => {
                  onGroupChange(item.value)
                  setOpen(null)
                }}
                role='option'
                type='button'
              >
                <CheckIcon className='apg-check apg-check-lead' />
                <span className='apg-row-main'>
                  <span className='apg-row-which'>
                    <span className='apg-row-name'>{item.label || item.value}</span>
                  </span>
                </span>
                <span className='apg-row-price'>
                  {ratio === 1 ? t('Standard price') : `×${ratio.toFixed(2)}`}
                </span>
              </button>
            )
          })}
        </div>
      </FluidPopover>
    </footer>
  )
}
