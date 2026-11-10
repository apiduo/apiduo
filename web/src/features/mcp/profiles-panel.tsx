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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Loader2 } from 'lucide-react'
import { useMemo, useState, type ReactNode } from 'react'
import { toast } from 'sonner'

import { ComboboxInput } from '@/components/ui/combobox-input'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { FormNavigationGuard } from '@/features/system-settings/components/form-navigation-guard'
import { cn } from '@/lib/utils'

import { mcpApi, mcpKeys } from './api'
import { useMcpCopy } from './copy'
import { ParametersEditor } from './parameters-editor'
import {
  EMPTY_DRAFT,
  toDraft,
  toPayload,
  type McpKind,
  type McpProtocol,
  type ModelProfile,
  type ProtocolHint,
  type ProfileDraft,
} from './types'

type ProfilesPanelProps = {
  profiles: ModelProfile[]
  loading: boolean
  /** 右侧当前编辑的是哪条；null 表示没选。用来驱动其它标签页。 */
  onActiveModelChange: (modelId: string | null) => void
  onDataChanged: () => void
}

const MODE_LABEL: Record<
  'unset' | 'zero' | 'n',
  (t: (key: string) => string) => string
> = {
  unset: (t) => t('Undeclared'),
  zero: (t) => t('Unsupported'),
  n: (t) => t('Limit'),
}

/** 三态：null = 未声明，0 = 明确不支持，其余 = 上限。 */
function triMode(value: number | null): 'unset' | 'zero' | 'n' {
  if (value === null) return 'unset'
  if (value === 0) return 'zero'
  return 'n'
}

function TriField(props: {
  label: string
  value: number | null
  onChange: (next: number | null) => void
}) {
  const t = useMcpCopy()
  const mode = triMode(props.value)
  const numeric = props.value !== null && props.value > 0 ? props.value : 1

  const handleMode = (next: string | null) => {
    if (next === 'unset') props.onChange(null)
    else if (next === 'zero') props.onChange(0)
    else props.onChange(numeric)
  }

  return (
    <div className='space-y-1.5'>
      <Label className='text-xs'>{props.label}</Label>
      <div className='flex gap-2'>
        <Select value={mode} onValueChange={handleMode}>
          <SelectTrigger className='w-[112px]'>
            <SelectValue>{MODE_LABEL[mode](t)}</SelectValue>
          </SelectTrigger>
          <SelectContent>
            <SelectItem value='unset'>{t('Undeclared')}</SelectItem>
            <SelectItem value='zero'>{t('Unsupported')}</SelectItem>
            <SelectItem value='n'>{t('Limit')}</SelectItem>
          </SelectContent>
        </Select>
        <Input
          type='number'
          min={1}
          className='flex-1'
          disabled={mode !== 'n'}
          value={mode === 'n' ? String(props.value) : ''}
          onChange={(e) => {
            const n = Number(e.target.value)
            props.onChange(Number.isFinite(n) && n > 0 ? n : 1)
          }}
        />
      </div>
    </div>
  )
}

const KIND_LABEL: Record<McpKind, (t: (key: string) => string) => string> = {
  image: (t) => t('Image'),
  video: (t) => t('Video'),
  search: (t) => t('Search'),
}

// 候选项的分类来自服务端 mcp/suggest.go，前端不再自带正则：
// 规则只能有一份，两份一旦漂移，标签和选中后实际填的值就会打架。
//
// 置信度决定提示口径：
//   high / medium → 标「图片」「视频」
//   low           → 标「图片?」「视频?」，问号表示有线索但不可靠
//   none          → 「未识别?」，选中后不自动改 kind，由人手动选
//   conflict      → 结论按模型名给，但其它判据（渠道/端点）说的是另一类，
//                   同样加问号：medium 不代表没有争议。
function candidateShort(
  hint: ProtocolHint | undefined,
  t: (key: string) => string
): string {
  const unsure = hint?.confidence === 'low' || hint?.conflict === true
  if (hint?.kind === 'video') {
    return unsure ? `${t('Video')}?` : t('Video')
  }
  if (hint?.kind === 'image') {
    return unsure ? `${t('Image')}?` : t('Image')
  }
  if (hint?.kind === 'search') return t('Search')
  return t('Short unknown')
}

// ComboboxInput 只渲染 label 与 icon（description 会被忽略），所以提示做成
// 一个带 title 的小标签挂在选项前，完整措辞留在悬停提示里。
function candidateMark(
  hint: ProtocolHint | undefined,
  t: (key: string) => string
): ReactNode {
  const short = candidateShort(hint, t)
  return (
    <span
      className='text-muted-foreground shrink-0 text-[10px]'
      title={hint?.reason ? `${short} · ${hint.reason}` : short}
    >
      {short}
    </span>
  )
}

export function ProfilesPanel(props: ProfilesPanelProps) {
  const t = useMcpCopy()
  const queryClient = useQueryClient()

  // 只读、零出站：候选来自本地 abilities 表，失败时静默降级为「可手输」。
  const candidates = useQuery({
    queryKey: mcpKeys.candidates(),
    queryFn: mcpApi.candidates,
    retry: false,
  })
  const candidateItems = candidates.data?.items ?? []
  // 旧版后端没有 suggestions，按空对象降级：候选照样可选，只是没有类型提示。
  const suggestions = candidates.data?.suggestions ?? {}
  const candidateOptions = candidateItems.map((id) => ({
    value: id,
    label: id,
    icon: candidateMark(suggestions[id], t),
  }))

  const [keyword, setKeyword] = useState('')
  const [selectedId, setSelectedId] = useState<number | null>(null)
  const [creating, setCreating] = useState(false)
  const [deleteOpen, setDeleteOpen] = useState(false)

  // 草稿 = 「服务端那份 + 本次编辑」，两者用键绑在一起而不是用 effect 同步：
  // 保存或启停让 updated_at 变了之后键就对不上，表单自动落回最新值 ——
  // 既不用手写同步，也不会出现「保存成功但表单还显示旧值」。
  const [override, setOverride] = useState<{
    key: string
    draft: ProfileDraft
  } | null>(null)

  const selected = useMemo(
    () => props.profiles.find((p) => p.id === selectedId) ?? null,
    [props.profiles, selectedId]
  )

  let base: ProfileDraft | null = null
  if (creating) base = { ...EMPTY_DRAFT }
  else if (selected) base = toDraft(selected)

  let baseKey = 'none'
  if (creating) baseKey = 'new'
  else if (selected) baseKey = `${selected.id}:${selected.updated_at}`

  const draft = override && override.key === baseKey ? override.draft : base

  const dirty = useMemo(() => {
    if (!draft) return false
    const reference = base ?? EMPTY_DRAFT
    return JSON.stringify(draft) !== JSON.stringify(reference)
  }, [draft, base])

  const activeModelId = selected ? selected.model_id : null
  const reportedRef = useMemo<{ value: string | null }>(
    () => ({ value: null }),
    []
  )
  if (reportedRef.value !== activeModelId) {
    reportedRef.value = activeModelId
    props.onActiveModelChange(activeModelId)
  }

  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: mcpKeys.profiles() })
    // 刚声明的模型要从候选里消失，已删除的要重新出现。
    void queryClient.invalidateQueries({ queryKey: mcpKeys.candidates() })
    props.onDataChanged()
  }

  const save = useMutation({
    mutationFn: async (next: ProfileDraft) => {
      if (!next.model_id.trim()) throw new Error(t('Model ID is required'))
      if (next.parameters.trim()) {
        try {
          JSON.parse(next.parameters)
        } catch {
          throw new Error(t('Parameter constraints is not valid JSON'))
        }
      }
      const body = toPayload(next)
      return next.id > 0
        ? mcpApi.update(next.id, body)
        : mcpApi.create(body)
    },
    onSuccess: (saved) => {
      setOverride(null)
      setCreating(false)
      setSelectedId(saved.id)
      invalidate()
    },
    // 校验类错误（模型 ID 为空、参数不是 JSON）与后端拒绝都从这里出声；
    // 不接这个回调的话，点保存看起来毫无反应。
    onError: (error) =>
      toast.error(error instanceof Error ? error.message : String(error)),
  })

  const toggle = useMutation({
    mutationFn: (p: ModelProfile) => mcpApi.toggle(p.id, !p.enabled),
    onSuccess: () => {
      setOverride(null)
      invalidate()
    },
    onError: (error) =>
      toast.error(error instanceof Error ? error.message : String(error)),
  })

  const remove = useMutation({
    mutationFn: (id: number) => mcpApi.remove(id),
    onSuccess: () => {
      setOverride(null)
      setCreating(false)
      setSelectedId(null)
      setDeleteOpen(false)
      invalidate()
    },
    onError: (error) =>
      toast.error(error instanceof Error ? error.message : String(error)),
  })

  const rows = useMemo(() => {
    const kw = keyword.trim().toLowerCase()
    return kw
      ? props.profiles.filter((p) => p.model_id.toLowerCase().includes(kw))
      : props.profiles
  }, [props.profiles, keyword])

  // 一次改多个字段必须走这里：setField 连续调用时每次都用同一份旧 draft，
  // 后一次会把前一次覆盖掉（选模型要同时改 model_id / kind / protocol）。
  const setFields = (patch: Partial<ProfileDraft>) => {
    if (!draft) return
    setOverride({ key: baseKey, draft: { ...draft, ...patch } })
  }

  const setField = <K extends keyof ProfileDraft>(
    key: K,
    value: ProfileDraft[K]
  ) => {
    setFields({ [key]: value } as Partial<ProfileDraft>)
  }

  const isNew = draft !== null && draft.id === 0

  let emptyHint = t('No declarations yet — click New model to start')
  if (props.profiles.length > 0) emptyHint = t('No models match')
  else if (candidateItems.length > 0) {
    emptyHint = t(
      'No declarations yet — {{count}} enabled models are available to declare',
      { count: candidateItems.length }
    )
  }

  // 空态里点候选 = 新建并预填 ID；仍要人工填完 kind/协议再保存，
  // 不因为「点了一下」就往库里写声明。
  const startCreateWith = (modelId: string) => {
    setSelectedId(null)
    setCreating(true)
    setOverride({ key: 'new', draft: { ...EMPTY_DRAFT, model_id: modelId } })
  }

  return (
    <div className='grid gap-4 lg:grid-cols-[260px_minmax(0,1fr)] lg:items-start'>
      <FormNavigationGuard when={dirty} />

      <Card>
        <CardHeader className='gap-2 pb-3'>
          <div className='flex items-center justify-between gap-2'>
            <CardTitle className='text-sm'>{t('Models')}</CardTitle>
            <Button
              size='sm'
              variant='outline'
              onClick={() => startCreateWith('')}
            >
              {t('New model')}
            </Button>
          </div>
          <Input
            placeholder={t('Search models')}
            value={keyword}
            onChange={(e) => setKeyword(e.target.value)}
          />
        </CardHeader>
        <CardContent className='max-h-[60vh] overflow-auto pb-4'>
          {props.loading ? (
            <div className='text-muted-foreground flex items-center gap-2 py-6 text-xs'>
              <Loader2 className='size-3.5 animate-spin' />
              {t('Loading')}
            </div>
          ) : null}

          {!props.loading && rows.length === 0 ? (
            <div className='space-y-2 py-6'>
              <p className='text-muted-foreground text-xs'>{emptyHint}</p>
              {props.profiles.length === 0 && candidateItems.length > 0 ? (
                <div className='space-y-1.5'>
                  <p className='text-muted-foreground text-[11px]'>
                    {t('Enabled models you can declare')}
                  </p>
                  <ul className='space-y-1'>
                    {candidateItems.slice(0, 5).map((id) => (
                      <li key={id}>
                        <button
                          type='button'
                          onClick={() => startCreateWith(id)}
                          className='hover:bg-accent flex w-full items-center justify-between gap-2 rounded-md px-2 py-1.5 text-left transition-colors'
                        >
                          <span className='truncate font-mono text-[11px]'>
                            {id}
                          </span>
                          <span
                            className='text-muted-foreground shrink-0 text-[10px]'
                            title={
                              suggestions[id]?.reason ??
                              t('Type not detected · confirm before declaring')
                            }
                          >
                            {candidateShort(suggestions[id], t)}
                          </span>
                        </button>
                      </li>
                    ))}
                  </ul>
                  {candidateItems.length > 5 ? (
                    <p className='text-muted-foreground text-[11px]'>
                      {t('{{count}} more to declare', {
                        count: candidateItems.length - 5,
                      })}
                    </p>
                  ) : null}
                </div>
              ) : null}
            </div>
          ) : null}

          {rows.length > 0 ? (
            <ul className='space-y-1'>
              {rows.map((p) => {
                const active = p.id === selectedId
                return (
                  <li key={p.id}>
                    <button
                      type='button'
                      onClick={() => {
                        setCreating(false)
                        setOverride(null)
                        setSelectedId(p.id)
                      }}
                      className={cn(
                        'hover:bg-accent w-full rounded-md px-2.5 py-2 text-left transition-colors',
                        active && 'bg-accent'
                      )}
                    >
                      <div className='flex items-center justify-between gap-2'>
                        <span className='truncate font-mono text-xs'>
                          {p.model_id}
                        </span>
                        {p.enabled ? null : (
                          <Badge variant='secondary' className='shrink-0'>
                            {t('Disable')}
                          </Badge>
                        )}
                      </div>
                      <div className='text-muted-foreground mt-0.5 text-[11px]'>
                        {p.kind} · {p.async ? 'async' : 'sync'} · {p.source}
                      </div>
                    </button>
                  </li>
                )
              })}
            </ul>
          ) : null}
        </CardContent>
      </Card>

      <div className='min-w-0 space-y-4'>
        {draft === null ? (
          <Card>
            <CardContent className='text-muted-foreground py-10 text-center text-sm'>
              {t('Select a model on the left, or click New model')}
            </CardContent>
          </Card>
        ) : null}

        {draft !== null ? (
          <>
            <Card>
              <CardHeader className='flex-row flex-wrap items-center justify-between gap-3'>
                <CardTitle className='truncate font-mono text-sm'>
                  {isNew ? t('New model') : draft.model_id}
                </CardTitle>
                <div className='flex shrink-0 items-center gap-2'>
                  {isNew ? null : (
                    <>
                      <Button
                        size='sm'
                        variant='outline'
                        disabled={toggle.isPending}
                        onClick={() => selected && toggle.mutate(selected)}
                      >
                        {draft.enabled ? t('Disable') : t('Enable')}
                      </Button>
                      <Button
                        size='sm'
                        variant='outline'
                        onClick={() => setDeleteOpen(true)}
                      >
                        {t('Delete')}
                      </Button>
                    </>
                  )}
                  <Button
                    size='sm'
                    disabled={save.isPending || !dirty}
                    onClick={() => save.mutate(draft)}
                  >
                    {save.isPending ? (
                      <Loader2 className='size-3.5 animate-spin' />
                    ) : (
                      t('Save')
                    )}
                  </Button>
                </div>
              </CardHeader>
              <CardContent className='space-y-4'>
                <p className='text-muted-foreground text-xs'>
                  {t(
                    'Basic · three required, the rest empty means no enforcement'
                  )}
                </p>

                {isNew ? (
                  <p className='text-muted-foreground text-[11px]'>
                    {t(
                      'Candidates come from enabled models; MCP serves image and video only'
                    )}
                  </p>
                ) : null}

                <div className='grid gap-3 sm:grid-cols-3'>
                  <div className='space-y-1.5'>
                    <Label className='text-xs'>{t('Model ID')}</Label>
                    {isNew ? (
                      <ComboboxInput
                        id='mcp-model-id'
                        options={candidateOptions}
                        value={draft.model_id}
                        onValueChange={(v) => {
                          const hint = suggestions[v]
                          // 有建议就一次带出三个字段；没有建议时只改模型 ID，
                          // 保留管理员已经选好的 kind —— 那是他手动判断的结果。
                          if (hint?.kind && hint.protocol) {
                            setFields({
                              model_id: v,
                              kind: hint.kind,
                              protocol: hint.protocol,
                            })
                            return
                          }
                          setField('model_id', v)
                        }}
                        allowCustomValue
                        placeholder={t(
                          'Pick an enabled model, or type a custom ID'
                        )}
                        emptyText={
                          candidates.isError
                            ? t('Candidate list unavailable — type an ID manually')
                            : t('No enabled model found — type an ID manually')
                        }
                        className='font-mono'
                        aria-label={t('Model ID')}
                      />
                    ) : (
                      <Input className='font-mono' value={draft.model_id} readOnly />
                    )}
                    {isNew && suggestions[draft.model_id]?.reason ? (
                      <p className='text-muted-foreground text-[11px]'>
                        {suggestions[draft.model_id].reason}
                      </p>
                    ) : null}
                  </div>
                  <div className='space-y-1.5'>
                    <Label className='text-xs'>{t('Output kind')}</Label>
                    <Select
                      value={draft.kind}
                      onValueChange={(v) => {
                        const kind = (v ?? 'image') as McpKind
                        // 换产物类型就换协议：视频只认 openai_video，
                        // 留一个对不上的协议只会在调用时失败。
                        // 两个字段必须一次改 —— 连续 setField 会互相覆盖，
                        // 结果是 kind=image 配 openai_video 这种错配。
                        setFields({
                          kind,
                          protocol: (kind === 'video'
                            ? 'openai_video'
                            : 'openai_image') as McpProtocol,
                        })
                      }}
                    >
                      <SelectTrigger>
                        <SelectValue>{KIND_LABEL[draft.kind](t)}</SelectValue>
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value='image'>{t('Image')}</SelectItem>
                        <SelectItem value='video'>{t('Video')}</SelectItem>
                        <SelectItem value='search'>{t('Search')}</SelectItem>
                      </SelectContent>
                    </Select>
                  </div>
                  <div className='space-y-1.5'>
                    <Label className='text-xs'>{t('Protocol')}</Label>
                    <Select
                      value={draft.protocol}
                      onValueChange={(v) =>
                        setField(
                          'protocol',
                          (v ?? 'openai_image') as McpProtocol
                        )
                      }
                    >
                      <SelectTrigger>
                        <SelectValue>{draft.protocol}</SelectValue>
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value='openai_image'>
                          openai_image
                        </SelectItem>
                        <SelectItem value='openai_video'>
                          openai_video
                        </SelectItem>
                      </SelectContent>
                    </Select>
                  </div>
                </div>

                <div className='grid gap-3 sm:grid-cols-2'>
                  <div className='space-y-1.5'>
                    <Label className='text-xs'>{t('Execution')}</Label>
                    <Select
                      value={draft.async ? 'true' : 'false'}
                      onValueChange={(v) => setField('async', v === 'true')}
                    >
                      <SelectTrigger>
                        <SelectValue>
                          {draft.async
                            ? t('Async task · task.get')
                            : t('Sync response')}
                        </SelectValue>
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value='false'>
                          {t('Sync response')}
                        </SelectItem>
                        <SelectItem value='true'>
                          {t('Async task · task.get')}
                        </SelectItem>
                      </SelectContent>
                    </Select>
                  </div>
                  <div className='space-y-1.5'>
                    <Label className='text-xs'>
                      {t('Display name (optional)')}
                    </Label>
                    <Input
                      value={draft.display_name}
                      onChange={(e) => setField('display_name', e.target.value)}
                    />
                  </div>
                </div>
              </CardContent>
            </Card>

            <Card>
              <CardHeader className='pb-3'>
                <CardTitle className='text-sm'>{t('Input limits')}</CardTitle>
                <p className='text-muted-foreground text-xs'>
                  {t(
                    'Undeclared is not 0 — treating it as 0 locks image-to-image models'
                  )}
                </p>
              </CardHeader>
              <CardContent className='grid gap-3 sm:grid-cols-3'>
                <TriField
                  label={t('Ref image limit')}
                  value={draft.max_ref_images}
                  onChange={(v) => setField('max_ref_images', v)}
                />
                <TriField
                  label={t('Ref video limit')}
                  value={draft.max_ref_videos}
                  onChange={(v) => setField('max_ref_videos', v)}
                />
                <div className='space-y-1.5'>
                  <Label className='text-xs'>{t('Prompt limit')}</Label>
                  <Input
                    type='number'
                    min={1}
                    value={
                      draft.max_prompt_chars === null
                        ? ''
                        : String(draft.max_prompt_chars)
                    }
                    onChange={(e) => {
                      const raw = e.target.value
                      setField(
                        'max_prompt_chars',
                        raw === '' ? null : Math.max(1, Number(raw) || 1)
                      )
                    }}
                  />
                </div>
              </CardContent>
            </Card>

            <Card>
              <CardHeader className='pb-3'>
                <CardTitle className='text-sm'>
                  {t('Parameter constraints')}
                </CardTitle>
                <p className='text-muted-foreground text-xs'>
                  {t(
                    'Enum or range, per parameter. Both views write the same JSON.'
                  )}
                </p>
              </CardHeader>
              <CardContent>
                <ParametersEditor
                  value={draft.parameters}
                  onChange={(next) => setField('parameters', next)}
                />
              </CardContent>
            </Card>

            <Card>
              <CardHeader className='pb-3'>
                <CardTitle className='text-sm'>
                  {t('Capability description')}
                </CardTitle>
                <p className='text-muted-foreground text-xs'>
                  {t('Shown in tools/list and models.list')}
                </p>
              </CardHeader>
              <CardContent className='grid gap-3 sm:grid-cols-2'>
                <div className='space-y-1.5'>
                  <Label className='text-xs'>{t('Summary')}</Label>
                  <Input
                    value={draft.summary}
                    onChange={(e) => setField('summary', e.target.value)}
                  />
                </div>
                <div className='space-y-1.5'>
                  <Label className='text-xs'>
                    {t('Tags (comma separated)')}
                  </Label>
                  <Input
                    value={draft.tags}
                    onChange={(e) => setField('tags', e.target.value)}
                  />
                </div>
              </CardContent>
            </Card>
          </>
        ) : null}
      </div>

      <ConfirmDialog
        open={deleteOpen}
        onOpenChange={setDeleteOpen}
        title={t('Delete this declaration?')}
        desc={t('The agent loses its parameter constraints for this model.')}
        confirmText={t('Delete')}
        destructive
        isLoading={remove.isPending}
        handleConfirm={() => selected && remove.mutate(selected.id)}
      />
    </div>
  )
}
