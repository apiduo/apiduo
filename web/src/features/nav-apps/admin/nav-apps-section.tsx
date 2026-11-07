import { ArrowDown, ArrowUp, Plus, Trash2 } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ROLE } from '@/lib/roles'

import { SettingsSection } from '@/features/system-settings/components/settings-section'
import { ApiduoGlow } from '@/features/apiduo-appearance'

import { classifyNavAppHref, isClientLink } from '../lib/href'
import {
  NAV_APP_ICON_OPTIONS,
  navAppIcon,
} from '../lib/icons'
import { isSameSite } from '../lib/render'
import { navAppUsesTemplate } from '../lib/template'
import {
  NAV_APP_MAX_COUNT,
  NAV_APP_ROLE_OPTIONS,
  navAppDisplayExternal,
  navAppDisplayIframe,
  navAppPlacementBoth,
  navAppPlacementSide,
  navAppPlacementTop,
} from '../constants'
import { useAllNavApps, useSaveNavApps } from '../hooks'
import type {
  NavApp,
  NavAppDisplayType,
  NavAppGroup,
  NavAppPlacement,
} from '../types'

const fieldClass =
  'h-8 w-full rounded-md border border-input bg-background px-2 text-sm outline-none transition-shadow focus-visible:ring-2 focus-visible:ring-ring'
const labelClass = 'text-[11px] font-medium text-muted-foreground'
const buttonClass =
  'inline-flex items-center gap-1 rounded-md border border-input px-2 py-1 text-xs transition-colors hover:bg-muted disabled:opacity-40'

const slugPattern = /^[a-z0-9][a-z0-9-]{0,63}$/

/**
 * Sidebar groups the application renders from code.
 *
 * They take part in the ordering list but cannot be renamed or deleted here:
 * their labels come from the locale files.
 */
const BUILTIN_NAV_GROUP_IDS = ['chat', 'general', 'personal', 'admin'] as const
const BUILTIN_NAV_GROUP_LABELS: Record<string, string> = {
  chat: 'Chat',
  general: 'General',
  personal: 'Personal',
  admin: 'Admin',
}

/**
 * Draft rows carry a client-only key.
 *
 * The slug cannot serve as the React key: it is edited in place, so using it
 * would remount the row on every keystroke and drop keyboard focus. The key is
 * stripped before saving and never reaches the server.
 */
type DraftEntry = NavApp & { clientKey: string }

const toDraft = (app: NavApp): DraftEntry => ({ ...app, clientKey: app.slug })

/** Mirrors the server-side same-origin rejection so the error appears inline. */
function targetsThisSite(href: string): boolean {
  try {
    return new URL(href).origin === window.location.origin
  } catch {
    return false
  }
}

function NextSlug(apps: NavApp[]): string {
  let index = apps.length + 1
  while (apps.some((app) => app.slug === `app-${index}`)) index += 1
  return `app-${index}`
}

export function NavAppsSection() {
  const { t } = useTranslation()
  const { data, isLoading } = useAllNavApps()
  const save = useSaveNavApps()

  // The form edits a local draft and writes the whole list on save, matching
  // the single-value option storage behind it.
  const [draft, setDraft] = useState<DraftEntry[]>([])
  // Heading of the default group (entries with no explicit group). Empty means
  // the translated default.
  const [groupTitle, setGroupTitle] = useState('')
  // Administrator-defined headings.
  const [groups, setGroups] = useState<NavAppGroup[]>([])
  // Top-level sidebar order, covering both custom and built-in groups.
  const [groupOrder, setGroupOrder] = useState<string[]>([])
  // Index of the row being dragged, or null.
  const [dragIndex, setDragIndex] = useState<number | null>(null)

  // Seed once. A background refetch (for example on window focus) must not
  // overwrite edits the admin has not saved yet.
  const seeded = useRef(false)
  // Revision captured when the form was seeded. Submitting it lets the server
  // reject a save that would overwrite someone else's edit.
  const revisionRef = useRef('')
  useEffect(() => {
    if (seeded.current) return
    if (data?.success && data.data) {
      setDraft(data.data.map(toDraft))
      setGroupTitle(data.group_title ?? '')
      const seededGroups = Array.isArray(data.groups) ? data.groups : []
      setGroups(seededGroups)
      // Any custom group missing from the stored order is appended, so a group
      // created by a script still appears in the editor.
      const storedOrder = Array.isArray(data.group_order) ? data.group_order : []
      const known = new Set([...storedOrder, ...BUILTIN_NAV_GROUP_IDS])
      const merged = [
        ...storedOrder,
        ...seededGroups.map((group) => group.slug).filter((slug) => !known.has(slug)),
      ]
      for (const builtin of BUILTIN_NAV_GROUP_IDS) {
        if (!merged.includes(builtin)) merged.push(builtin)
      }
      setGroupOrder(merged)
      revisionRef.current = data.revision ?? ''
      seeded.current = true
    }
  }, [data])

  // Slugs already committed cannot change: they are the identity the sidebar
  // and the frame route use.
  const persistedSlugs = useMemo(
    () => new Set((data?.success && data.data ? data.data : []).map((a) => a.slug)),
    [data]
  )

  const addGroup = () => {
    const existing = new Set(groups.map((group) => group.slug))
    let ordinal = groups.length + 1
    let slug = `group-${ordinal}`
    while (existing.has(slug)) {
      ordinal += 1
      slug = `group-${ordinal}`
    }
    setGroups((current) => [
      ...current,
      { slug, name: t('New group'), name_i18n: '', sort: current.length },
    ])
    setGroupOrder((current) => [...current, slug])
  }

  const renameGroup = (slug: string, name: string) => {
    setGroups((current) =>
      current.map((group) => (group.slug === slug ? { ...group, name } : group))
    )
  }

  // Removing a heading must also detach its entries, otherwise the save is
  // rejected for pointing at a group that no longer exists.
  const removeGroup = (slug: string) => {
    setGroups((current) => current.filter((group) => group.slug !== slug))
    setGroupOrder((current) => current.filter((id) => id !== slug))
    setDraft((current) =>
      current.map((entry) =>
        entry.group_slug === slug ? { ...entry, group_slug: undefined } : entry
      )
    )
  }

  const moveGroup = (from: number, to: number) => {
    if (from === to) return
    setGroupOrder((current) => {
      const next = [...current]
      const [moved] = next.splice(from, 1)
      if (moved === undefined) return current
      next.splice(to, 0, moved)
      return next
    })
  }

  const patch = (index: number, values: Partial<NavApp>) => {
    setDraft((current) =>
      current.map((app, position) =>
        position === index ? { ...app, ...values } : app
      )
    )
  }

  const move = (index: number, delta: number) => {
    setDraft((current) => {
      const target = index + delta
      if (target < 0 || target >= current.length) return current
      const next = [...current]
      ;[next[index], next[target]] = [next[target], next[index]]
      return next
    })
  }

  const remove = (index: number) => {
    const app = draft[index]
    if (!window.confirm(t('Delete {{name}}?', { name: app.name || app.slug }))) {
      return
    }
    setDraft((current) => current.filter((_, position) => position !== index))
  }

  const add = () => {
    if (draft.length >= NAV_APP_MAX_COUNT) {
      toast.error(
        t('Navigation entries are limited to {{max}}.', { max: NAV_APP_MAX_COUNT })
      )
      return
    }
    setDraft((current) => [
      ...current,
      {
        clientKey: `new-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
        slug: NextSlug(current),
        name: '',
        name_i18n: '',
        icon: 'globe',
        href: 'https://',
        display_type: navAppDisplayExternal,
        placement: navAppPlacementSide,
        min_role: ROLE.USER,
        sort: current.length,
        enabled: true,
      },
    ])
  }

  const onSave = async () => {
    const seen = new Set<string>()
    for (const app of draft) {
      if (!slugPattern.test(app.slug)) {
        toast.error(
          t(
            'Slug must use lowercase letters, digits and hyphens, and start with a letter or digit'
          )
        )
        return
      }
      if (!app.name.trim()) {
        toast.error(t('Every entry needs a name'))
        return
      }
      if (app.name_i18n.trim()) {
        let shapeOk = false
        try {
          const parsed = JSON.parse(app.name_i18n) as unknown
          shapeOk = Boolean(parsed) && typeof parsed === 'object' && !Array.isArray(parsed)
        } catch {
          shapeOk = false
        }
        if (!shapeOk) {
          toast.error(t('Localized names must be a JSON object of language tag to text'))
          return
        }
      }
      const hrefKind = classifyNavAppHref(app.href)
      if (!hrefKind) {
        toast.error(
          t(
            'Target address must be an absolute http(s) URL or a client link such as cherrystudio://'
          )
        )
        return
      }
      if (hrefKind === 'web' && targetsThisSite(app.href)) {
        toast.error(
          t('A navigation entry cannot point at this site. Use another host.')
        )
        return
      }
      // A deep link resolves to no document, so framing it could only ever show
      // an error. The server rejects this too; catching it here keeps the
      // message next to the row instead of arriving as a failed save.
      if (hrefKind === 'app' && app.display_type === navAppDisplayIframe) {
        toast.error(
          t('A client link cannot be embedded. Choose the new tab option instead')
        )
        return
      }
      // An embedded page loads before any click, so a placeholder could never be
      // filled by the viewer's own key — and a key in a frame URL would leak
      // through the target's logs. The server rejects this too.
      if (
        hrefKind === 'web' &&
        app.display_type === navAppDisplayIframe &&
        navAppUsesTemplate(app.href)
      ) {
        toast.error(
          t(
            'A template cannot be embedded. Choose the new tab option or a client link instead'
          )
        )
        return
      }
      if (seen.has(app.slug)) {
        toast.error(t('Slug {{slug}} is already in use', { slug: app.slug }))
        return
      }
      seen.add(app.slug)
    }

    // Sort is derived from list order, so the up/down buttons are the only
    // ordering control and no separate weight field can drift. Fields are
    // listed explicitly to keep the client-only key out of the request.
    const payload: NavApp[] = draft.map((entry, index) => ({
      slug: entry.slug,
      name: entry.name,
      name_i18n: entry.name_i18n,
      icon: entry.icon,
      href: entry.href,
      display_type: entry.display_type,
      placement: entry.placement,
      min_role: entry.min_role,
      sort: index,
      enabled: entry.enabled,
      parent_slug: entry.parent_slug || undefined,
    }))

    let result
    try {
      result = await save.mutateAsync({
        apps: payload,
        groups: groups.map((group, index) => ({ ...group, sort: index })),
        group_order: groupOrder,
        group_title: groupTitle.trim(),
        base_revision: revisionRef.current,
      })
    } catch {
      // The mutation's onError already surfaced the server message.
      return
    }
    if (result.success && Array.isArray(result.data)) {
      setDraft(result.data.map(toDraft))
      if (Array.isArray(result.groups)) setGroups(result.groups)
      if (Array.isArray(result.group_order)) setGroupOrder(result.group_order)
      revisionRef.current = result.revision ?? ''
    }
  }

  return (
    <SettingsSection title={t('Custom navigation entries')}>
      <p className='text-xs text-muted-foreground'>
        {t(
          'Add applications to the sidebar or header. Entries are stored in the existing option table, so no database migration is involved. A target is either an absolute http(s) address on another host, or a client link such as cherrystudio://providers/api-keys. Placeholders are substituted when the entry is clicked: {key} and the client payloads {cherryConfig} {aionuiConfig} {deepchatConfig} {aqbotConfig} use the signed-in user’s own API key, {address} and {base_url} use this deployment’s address. Never type a credential into the address itself, because everyone who can see the entry can read it.'
        )}
      </p>

      <div className='space-y-2'>
        <div className='flex flex-wrap items-center justify-between gap-2'>
          <span className='text-xs font-medium'>{t('Sidebar order')}</span>
          <button type='button' className={buttonClass} onClick={addGroup}>
            <Plus className='size-3' />
            {t('Add group')}
          </button>
        </div>
        <p className='text-muted-foreground text-xs'>
          {t(
            'Drag a row to change where that group appears in the sidebar. Built-in groups keep their own translated names.'
          )}
        </p>

        <div className='space-y-1'>
          {groupOrder.map((id, index) => {
            const custom = groups.find((group) => group.slug === id)
            const builtinLabel = BUILTIN_NAV_GROUP_LABELS[id]
            return (
              <div
                key={id}
                draggable
                onDragStart={() => setDragIndex(index)}
                onDragOver={(event) => event.preventDefault()}
                onDrop={() => {
                  if (dragIndex !== null) moveGroup(dragIndex, index)
                  setDragIndex(null)
                }}
                onDragEnd={() => setDragIndex(null)}
                className={`bg-background flex items-center gap-2 rounded-md border px-2 py-1.5 text-sm ${
                  dragIndex === index ? 'opacity-50' : ''
                }`}
              >
                <svg
                  width='10'
                  height='10'
                  viewBox='0 0 10 10'
                  className='text-muted-foreground shrink-0 cursor-grab'
                  aria-hidden='true'
                >
                  <path
                    d='M2 3h6M2 5h6M2 7h6'
                    stroke='currentColor'
                    strokeWidth='1.2'
                    strokeLinecap='round'
                  />
                </svg>

                {custom ? (
                  <input
                    className='border-input bg-background min-w-0 flex-1 rounded border px-1.5 py-0.5 text-sm'
                    value={custom.name}
                    maxLength={100}
                    onChange={(event) => renameGroup(id, event.target.value)}
                  />
                ) : (
                  <span className='min-w-0 flex-1 truncate'>
                    {builtinLabel ? t(builtinLabel) : id}
                  </span>
                )}

                <span className='text-muted-foreground shrink-0 text-[10px]'>
                  {custom ? custom.slug : t('Built-in')}
                </span>

                {custom ? (
                  <button
                    type='button'
                    className={buttonClass}
                    onClick={() => removeGroup(id)}
                    aria-label={t('Delete')}
                  >
                    <Trash2 className='size-3' />
                  </button>
                ) : null}
              </div>
            )
          })}
        </div>
      </div>

      <div className='space-y-1.5'>
        <label htmlFor='nav-app-group-title' className='text-xs font-medium'>
          {t('Default group name')}
        </label>
        <input
          id='nav-app-group-title'
          className='border-input bg-background h-8 w-full max-w-sm rounded-md border px-2 text-sm'
          placeholder={t('Apps')}
          value={groupTitle}
          onChange={(event) => setGroupTitle(event.target.value)}
        />
        <p className='text-muted-foreground text-xs'>
          {t(
            'Heading for entries that are not assigned to a group. Leave empty to use the default translation.'
          )}
        </p>
      </div>

      <div className='flex flex-wrap items-center gap-2'>
        <button type='button' className={buttonClass} onClick={add}>
          <Plus className='size-3' />
          {t('Add entry')}
        </button>
        <button
          type='button'
          className={buttonClass}
          onClick={onSave}
          disabled={save.isPending}
        >
          {save.isPending ? t('Saving...') : t('Save navigation entries')}
        </button>
        <span className='text-[11px] text-muted-foreground'>
          {t('{{count}} of {{max}} used', {
            count: draft.length,
            max: NAV_APP_MAX_COUNT,
          })}
        </span>
      </div>

      {isLoading && (
        <p className='text-xs text-muted-foreground'>{t('Loading...')}</p>
      )}

      {!isLoading && draft.length === 0 && (
        <p className='rounded-md border border-dashed px-3 py-6 text-center text-xs text-muted-foreground'>
          {t('No custom entries yet.')}
        </p>
      )}

      {draft.map((app, index) => {
        const Icon = navAppIcon(app.icon)
        const slugLocked = persistedSlugs.has(app.slug)
        const clientLink = isClientLink(app.href)
        const templated = navAppUsesTemplate(app.href)
        return (
          <div
            key={app.clientKey}
            data-ap-surface=''
            className='flex flex-col gap-3 rounded-lg border p-3'
          >
            <ApiduoGlow />
            <div className='flex items-center justify-between gap-2'>
              <div className='flex min-w-0 items-center gap-2'>
                <Icon className='size-4 shrink-0 text-muted-foreground' />
                <span className='truncate text-sm font-medium'>
                  {app.name || t('Untitled entry')}
                </span>
                <span className='shrink-0 rounded bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground'>
                  {app.display_type === navAppDisplayIframe
                    ? t('Embedded')
                    : clientLink
                      ? t('Client app')
                      : t('New tab')}
                </span>
              </div>
              <div className='flex shrink-0 items-center gap-1'>
                <button
                  type='button'
                  className={buttonClass}
                  onClick={() => move(index, -1)}
                  disabled={index === 0}
                  aria-label={t('Move up')}
                >
                  <ArrowUp className='size-3' />
                </button>
                <button
                  type='button'
                  className={buttonClass}
                  onClick={() => move(index, 1)}
                  disabled={index === draft.length - 1}
                  aria-label={t('Move down')}
                >
                  <ArrowDown className='size-3' />
                </button>
                <button
                  type='button'
                  className={buttonClass}
                  onClick={() => remove(index)}
                  aria-label={t('Delete')}
                >
                  <Trash2 className='size-3' />
                </button>
              </div>
            </div>

            <div className='grid gap-3 md:grid-cols-2'>
              <label className='flex flex-col gap-1'>
                <span className={labelClass}>{t('Name')}</span>
                <input
                  className={fieldClass}
                  value={app.name}
                  maxLength={100}
                  onChange={(event) => patch(index, { name: event.target.value })}
                />
              </label>

              <label className='flex flex-col gap-1'>
                <span className={labelClass}>{t('Slug')}</span>
                <input
                  className={fieldClass}
                  value={app.slug}
                  disabled={slugLocked}
                  // A prefilled slug is a starting point, not a suffix. Selecting
                  // it on focus means the first keystroke replaces it instead of
                  // producing values like "app-1langfuse".
                  onFocus={(event) => {
                    if (!slugLocked) event.currentTarget.select()
                  }}
                  onChange={(event) =>
                    patch(index, {
                      slug: event.target.value.toLowerCase().replaceAll(/[^a-z0-9-]/g, ''),
                    })
                  }
                />
              </label>

              <label className='flex flex-col gap-1'>
                <span className={labelClass}>{t('Group')}</span>
                <select
                  className={fieldClass}
                  value={app.group_slug ?? ''}
                  onChange={(event) =>
                    patch(index, { group_slug: event.target.value || undefined })
                  }
                >
                  <option value=''>{t('Default group')}</option>
                  {groups.map((group) => (
                    <option key={group.slug} value={group.slug}>
                      {group.name || group.slug}
                    </option>
                  ))}
                </select>
              </label>

              <label className='flex flex-col gap-1 md:col-span-2'>
                <span className={labelClass}>{t('Parent entry')}</span>
                <select
                  className={fieldClass}
                  value={app.parent_slug ?? ''}
                  onChange={(event) =>
                    patch(index, {
                      parent_slug: event.target.value || undefined,
                    })
                  }
                >
                  <option value=''>{t('None (top level)')}</option>
                  {draft
                    .filter(
                      (candidate) =>
                        candidate.slug &&
                        candidate.slug !== app.slug &&
                        !candidate.parent_slug
                    )
                    .map((candidate) => (
                      <option key={candidate.clientKey} value={candidate.slug}>
                        {candidate.name || candidate.slug}
                      </option>
                    ))}
                </select>
                <span className='text-muted-foreground text-xs'>
                  {t(
                    'Nested entries appear under the selected one. The parent must leave its target address empty, because it acts as a group heading.'
                  )}
                </span>
              </label>

              <label className='flex flex-col gap-1 md:col-span-2'>
                <span className={labelClass}>{t('Localized name (JSON)')}</span>
                <input
                  className={fieldClass}
                  value={app.name_i18n}
                  placeholder='{"zhCN":"显示名称"}'
                  onChange={(event) =>
                    patch(index, { name_i18n: event.target.value })
                  }
                />
              </label>

              <label className='flex flex-col gap-1 md:col-span-2'>
                <span className={labelClass}>{t('Address')}</span>
                <input
                  className={fieldClass}
                  value={app.href}
                  placeholder='https://example.com or cherrystudio://providers/api-keys?v=1&data={cherryConfig}'
                  onChange={(event) => {
                    const href = event.target.value
                    patch(index, {
                      href,
                      // A client link or a template has no document to frame.
                      // Switching the display type as the address changes keeps
                      // the row from holding a combination the server would
                      // reject on save.
                      ...((isClientLink(href) || navAppUsesTemplate(href)) &&
                      app.display_type === navAppDisplayIframe
                        ? { display_type: navAppDisplayExternal }
                        : {}),
                    })
                  }}
                />
              </label>

              <label className='flex flex-col gap-1'>
                <span className={labelClass}>{t('Icon')}</span>
                <select
                  className={fieldClass}
                  value={app.icon}
                  onChange={(event) => patch(index, { icon: event.target.value })}
                >
                  {NAV_APP_ICON_OPTIONS.map((name) => (
                    <option key={name} value={name}>
                      {name}
                    </option>
                  ))}
                </select>
              </label>

              <label className='flex flex-col gap-1'>
                <span className={labelClass}>{t('Open as')}</span>
                <select
                  className={fieldClass}
                  value={app.display_type}
                  onChange={(event) =>
                    patch(index, {
                      display_type: event.target.value as NavAppDisplayType,
                    })
                  }
                >
                  {/* A deep link opens the installed application, not a tab;
                      labelling it "new tab" would contradict the badge. */}
                  <option value={navAppDisplayExternal}>
                    {clientLink ? t('Open in the client app') : t('New tab')}
                  </option>
                  <option
                    value={navAppDisplayIframe}
                    disabled={clientLink || templated}
                  >
                    {t('Embedded page')}
                  </option>
                </select>
              </label>

              <label className='flex flex-col gap-1'>
                <span className={labelClass}>{t('Placement')}</span>
                <select
                  className={fieldClass}
                  value={app.placement}
                  onChange={(event) =>
                    patch(index, {
                      placement: event.target.value as NavAppPlacement,
                    })
                  }
                >
                  <option value={navAppPlacementSide}>{t('Sidebar')}</option>
                  <option value={navAppPlacementTop}>{t('Top bar')}</option>
                  <option value={navAppPlacementBoth}>{t('Both')}</option>
                </select>
              </label>

              <label className='flex flex-col gap-1'>
                <span className={labelClass}>{t('Visible to')}</span>
                <select
                  className={fieldClass}
                  value={app.min_role}
                  onChange={(event) =>
                    patch(index, { min_role: Number(event.target.value) })
                  }
                >
                  {NAV_APP_ROLE_OPTIONS.map((option) => (
                    <option key={option.value} value={option.value}>
                      {t(option.labelKey)}
                    </option>
                  ))}
                </select>
              </label>

              <label className='flex items-center gap-2 pt-4'>
                <input
                  type='checkbox'
                  checked={app.enabled}
                  onChange={(event) =>
                    patch(index, { enabled: event.target.checked })
                  }
                />
                <span className='text-xs'>{t('Enabled')}</span>
              </label>
            </div>

            {templated && (
              <p className='text-[11px] text-muted-foreground'>
                {t(
                  'Placeholders are filled when the entry is clicked, using the signed-in user’s own API key and this deployment’s address, so the stored address never carries a credential.'
                )}
              </p>
            )}

            {clientLink && (
              <p className='text-[11px] text-muted-foreground'>
                {t(
                  'The browser hands this address to the installed application. If nothing opens, the application is missing or the browser blocked the link.'
                )}
              </p>
            )}

            {app.display_type === navAppDisplayIframe && (
              <div className='space-y-1'>
                <p className='text-[11px] text-muted-foreground'>
                  {t(
                    'Embedding requires the target to allow being framed (no X-Frame-Options or frame-ancestors restriction). If it stays blank, open it in a new tab.'
                  )}
                </p>
                {isSameSite(app.href) ? (
                  <p className='text-[11px] text-muted-foreground'>
                    {t(
                      'Same site as the console: login cookies count as first-party, so the embedded app normally stays signed in.'
                    )}
                  </p>
                ) : (
                  <p className='text-[11px] text-amber-600 dark:text-amber-500'>
                    {t(
                      'Different site from the console: browsers block third-party cookies in frames, so the embedded app may not stay signed in. Put it under the same main domain, or open it in a new tab.'
                    )}
                  </p>
                )}
              </div>
            )}
          </div>
        )
      })}
    </SettingsSection>
  )
}
