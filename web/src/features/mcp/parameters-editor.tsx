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
import { Plus, X } from 'lucide-react'
import { useMemo, useState } from 'react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { cn } from '@/lib/utils'

import { useMcpCopy } from './copy'

/**
 * 参数约束编辑器：表单与 JSON 双向切换。
 *
 * 后端的 ParamSpec 只有四个键（enum / min / max / default），但手写 JSON 时
 * 括号一多就会错位；反过来只给表单，又表达不了还没被表单覆盖的写法。
 * 所以两种视图都留着，共用同一份字符串：表单改完序列化回 JSON，JSON 合法时
 * 再解析回表单。不认识的键原样保留 —— 编辑器不该悄悄删掉别人的字段。
 */

type ParamSpec = {
  enum?: unknown[]
  min?: number
  max?: number
  default?: unknown
  [key: string]: unknown
}

type Specs = Record<string, ParamSpec>

// 与后端 paramNameRe 一致：这个名字会拼进 SQL 路径，形状不对直接挡住。
const NAME_RE = /^[a-z][a-z0-9_]{0,31}$/

function parseSpecs(raw: string): { ok: boolean; specs: Specs } {
  const text = raw.trim()
  if (!text) return { ok: true, specs: {} }
  try {
    const parsed = JSON.parse(text) as unknown
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
      return { ok: false, specs: {} }
    }
    return { ok: true, specs: parsed as Specs }
  } catch {
    return { ok: false, specs: {} }
  }
}

function serialize(specs: Specs): string {
  const keys = Object.keys(specs)
  if (keys.length === 0) return ''
  return JSON.stringify(specs, null, 2)
}

/** 一个值在 JSON 里的写法：字符串带引号，数字/布尔原样。 */
function valueLabel(v: unknown): string {
  return typeof v === 'string' ? v : JSON.stringify(v)
}

/** 输入框里的文本 → 值：纯数字按数字存，其余按字符串。 */
function toValue(text: string): unknown {
  const t = text.trim()
  if (t === '') return ''
  if (/^-?\d+(\.\d+)?$/.test(t)) return Number(t)
  if (t === 'true') return true
  if (t === 'false') return false
  return t
}

function EnumEditor(props: {
  spec: ParamSpec
  onChange: (next: ParamSpec) => void
  t: (key: string) => string
}) {
  const [draft, setDraft] = useState('')
  const values = props.spec.enum ?? []
  const current = props.spec.default

  const add = () => {
    const v = draft.trim()
    if (!v) return
    const next = toValue(v)
    if (values.some((x) => JSON.stringify(x) === JSON.stringify(next))) {
      setDraft('')
      return
    }
    props.onChange({ ...props.spec, enum: [...values, next] })
    setDraft('')
  }

  const remove = (index: number) => {
    const next = values.filter((_, i) => i !== index)
    const patch: ParamSpec = { ...props.spec, enum: next }
    // 删掉的正好是默认值时，默认值也要跟着去掉，否则会留下一个指向不存在取值的默认。
    if (JSON.stringify(values[index]) === JSON.stringify(current)) delete patch.default
    props.onChange(patch)
  }

  return (
    <div className='space-y-1.5'>
      <div className='flex flex-wrap items-center gap-1.5'>
        {values.map((v, i) => {
          const isDefault = JSON.stringify(v) === JSON.stringify(current)
          return (
            <span
              key={valueLabel(v)}
              className={cn(
                'inline-flex items-center gap-1 rounded-md border px-2 py-0.5 font-mono text-[11px]',
                isDefault && 'border-primary/50 bg-primary/5'
              )}
            >
              <button
                type='button'
                className='hover:underline'
                title={props.t('Set as default')}
                onClick={() => props.onChange({ ...props.spec, default: v })}
              >
                {valueLabel(v)}
              </button>
              {isDefault ? (
                <Badge variant='secondary' className='h-4 px-1 text-[10px]'>
                  {props.t('Default')}
                </Badge>
              ) : null}
              <button
                type='button'
                className='text-muted-foreground hover:text-foreground'
                onClick={() => remove(i)}
                aria-label={props.t('Remove value')}
              >
                <X className='size-3' />
              </button>
            </span>
          )
        })}
        <span className='flex items-center gap-1'>
          <Input
            className='h-7 w-28 font-mono text-[11px]'
            placeholder={props.t('Add value')}
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault()
                add()
              }
            }}
          />
          <Button
            type='button'
            size='sm'
            variant='ghost'
            className='h-7 px-1'
            onClick={add}
          >
            <Plus className='size-3.5' />
          </Button>
        </span>
      </div>
    </div>
  )
}

function ParamRow(props: {
  name: string
  spec: ParamSpec
  onChange: (name: string, next: ParamSpec | null) => void
  t: (key: string) => string
}) {
  const isEnum = Array.isArray(props.spec.enum)
  return (
    <div className='space-y-2 rounded-md border p-2.5'>
      <div className='flex items-center gap-2'>
        <Input
          className='h-7 w-40 font-mono text-xs'
          value={props.name}
          aria-invalid={!NAME_RE.test(props.name)}
          onChange={(e) => props.onChange(e.target.value, props.spec)}
        />
        {!NAME_RE.test(props.name) ? (
          <span className='text-destructive text-[11px]'>
            {props.t('Lowercase letters, digits and _, starting with a letter')}
          </span>
        ) : null}
        <span className='ml-auto flex items-center gap-1 text-[11px]'>
          <Badge variant='outline'>{isEnum ? props.t('Enum') : props.t('Range')}</Badge>
          <Button
            type='button'
            size='sm'
            variant='ghost'
            className='h-7 px-1'
            onClick={() => props.onChange(props.name, null)}
            aria-label={props.t('Remove parameter')}
          >
            <X className='size-3.5' />
          </Button>
        </span>
      </div>

      {isEnum ? (
        <EnumEditor
          spec={props.spec}
          t={props.t}
          onChange={(next) => props.onChange(props.name, next)}
        />
      ) : (
        <div className='flex items-center gap-2'>
          <span className='text-muted-foreground text-[11px]'>{props.t('Min')}</span>
          <Input
            className='h-7 w-24 text-xs'
            type='number'
            value={props.spec.min ?? ''}
            onChange={(e) => {
              const next = { ...props.spec }
              if (e.target.value === '') delete next.min
              else next.min = Number(e.target.value)
              props.onChange(props.name, next)
            }}
          />
          <span className='text-muted-foreground text-[11px]'>{props.t('Max')}</span>
          <Input
            className='h-7 w-24 text-xs'
            type='number'
            value={props.spec.max ?? ''}
            onChange={(e) => {
              const next = { ...props.spec }
              if (e.target.value === '') delete next.max
              else next.max = Number(e.target.value)
              props.onChange(props.name, next)
            }}
          />
          <span className='text-muted-foreground text-[11px]'>
            {props.t('Default')}
          </span>
          <Input
            className='h-7 w-24 text-xs'
            value={
              props.spec.default === undefined ? '' : String(props.spec.default)
            }
            onChange={(e) => {
              const next = { ...props.spec }
              if (e.target.value === '') delete next.default
              else next.default = toValue(e.target.value)
              props.onChange(props.name, next)
            }}
          />
        </div>
      )}
    </div>
  )
}

export function ParametersEditor(props: {
  value: string
  onChange: (next: string) => void
}) {
  const t = useMcpCopy()
  const parsed = useMemo(() => parseSpecs(props.value), [props.value])
  const [mode, setMode] = useState<'form' | 'json'>('form')
  // JSON 视图用本地文本，避免每敲一个字符就被父组件回写打断光标。
  const [text, setText] = useState(props.value)

  const effectiveMode = parsed.ok ? mode : 'json'
  const specs = parsed.specs

  const emit = (next: Specs) => {
    const text = serialize(next)
    setText(text)
    props.onChange(text)
  }

  const renameParam = (from: string, to: string, spec: ParamSpec) => {
    const next: Specs = {}
    for (const [k, v] of Object.entries(specs)) {
      if (k === from) next[to] = spec
      else next[k] = v
    }
    emit(next)
  }

  const putParam = (name: string, spec: ParamSpec | null) => {
    const next: Specs = { ...specs }
    if (spec === null) delete next[name]
    else next[name] = spec
    emit(next)
  }

  const addParam = () => {
    let i = 0
    let name = 'param'
    while (specs[name]) name = `param_${++i}`
    emit({ ...specs, [name]: { enum: [], default: '' } })
  }

  return (
    <div className='space-y-3'>
      <div className='flex items-center gap-1'>
        <Button
          type='button'
          size='sm'
          variant={effectiveMode === 'form' ? 'secondary' : 'ghost'}
          className='h-7 px-2 text-xs'
          onClick={() => setMode('form')}
          disabled={!parsed.ok}
        >
          {t('Form')}
        </Button>
        <Button
          type='button'
          size='sm'
          variant={effectiveMode === 'json' ? 'secondary' : 'ghost'}
          className='h-7 px-2 text-xs'
          onClick={() => {
            setText(serialize(specs))
            setMode('json')
          }}
        >
          JSON
        </Button>
        {!parsed.ok ? (
          <span className='text-destructive ml-2 text-[11px]'>
            {t('Not valid JSON — switch back to Form after fixing it')}
          </span>
        ) : null}
      </div>

      {effectiveMode === 'json' ? (
        <Textarea
          className='min-h-28 font-mono text-xs'
          value={text}
          onChange={(e) => {
            setText(e.target.value)
            props.onChange(e.target.value)
          }}
        />
      ) : (
        <div className='space-y-2'>
          {Object.keys(specs).length === 0 ? (
            <p className='text-muted-foreground text-xs'>
              {t('No constraints yet — add one, or switch to JSON.')}
            </p>
          ) : null}
          {Object.entries(specs).map(([name, spec]) => (
            <ParamRow
              key={name}
              name={name}
              spec={spec}
              t={t}
              onChange={(to, next) => {
                if (to !== name && next) renameParam(name, to, next)
                else putParam(name, next)
              }}
            />
          ))}
          <Button type='button' size='sm' variant='outline' onClick={addParam}>
            <Plus className='size-3.5' />
            {t('Add parameter')}
          </Button>
        </div>
      )}
    </div>
  )
}
