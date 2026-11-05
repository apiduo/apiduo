import { AlertTriangle, ExternalLink, RefreshCw } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { useNavAppsQuery } from './hooks'
import { canEmbed, isSameSite, navAppTitle } from './lib/render'
import { navAppIcon } from './lib/icons'

/**
 * Frame policy for embedded entries.
 *
 * allow-same-origin is granted on purpose: the target is required to be a
 * different origin (checked in the browser by `canEmbed` and again on the
 * server), so the token only restores the target's own storage and script
 * context. Omitting it breaks most real applications without buying any
 * isolation we do not already have from the origin boundary.
 *
 * allow-top-navigation is deliberately absent, including the
 * by-user-activation variant used by the custom home page: an embedded
 * third-party application must never be able to navigate this console
 * elsewhere, which is a phishing vector rather than a convenience feature.
 * Downloads, modals, pointer lock and presentation are likewise not granted.
 *
 * allow-storage-access-by-user-activation is granted so a target on another
 * site can call document.requestStorageAccess() and ask the browser for its
 * own cookies after a user gesture. It is the only sanctioned escape from the
 * third-party cookie rules, it requires cooperation from the target, and it
 * never gives that target access to this console.
 */
const FRAME_SANDBOX = [
  'allow-forms',
  'allow-popups',
  'allow-popups-to-escape-sandbox',
  'allow-same-origin',
  'allow-scripts',
  'allow-storage-access-by-user-activation',
].join(' ')

const actionClass =
  'inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs text-muted-foreground transition-colors hover:bg-muted hover:text-foreground'

function FrameNotice({
  title,
  description,
  href,
}: {
  title: string
  description: string
  href?: string
}) {
  const { t } = useTranslation()
  return (
    <div className='flex h-full flex-col items-center justify-center gap-3 p-8 text-center'>
      <AlertTriangle className='size-6 text-muted-foreground' />
      <div className='space-y-1'>
        <p className='text-sm font-medium'>{title}</p>
        <p className='max-w-md text-xs text-muted-foreground'>{description}</p>
      </div>
      {href && (
        <a
          href={href}
          target='_blank'
          rel='noopener noreferrer'
          className='inline-flex items-center gap-1 text-xs text-primary hover:underline'
        >
          <ExternalLink className='size-3' />
          {t('Open in a new tab')}
        </a>
      )}
    </div>
  )
}

export function NavAppFrame({ slug }: { slug: string }) {
  const { i18n, t } = useTranslation()
  const { data, isLoading, isError } = useNavAppsQuery()
  const [reloadKey, setReloadKey] = useState(0)

  const app = useMemo(
    () => (data?.success && data.data ? data.data : []).find((item) => item.slug === slug),
    [data, slug]
  )

  if (isLoading) {
    return (
      <div className='flex h-full items-center justify-center text-xs text-muted-foreground'>
        {t('Loading...')}
      </div>
    )
  }

  if (isError || !app) {
    return (
      <FrameNotice
        title={t('Application not found')}
        description={t(
          'This entry does not exist, is disabled, or is not available to your role.'
        )}
      />
    )
  }

  const title = navAppTitle(app, i18n.languages)

  if (app.display_type !== 'iframe') {
    return (
      <FrameNotice
        title={title}
        description={t('This entry opens as an external link.')}
        href={app.href}
      />
    )
  }

  // The server already rejects same-origin targets, but it cannot know every
  // hostname this deployment answers on. Re-checking here closes that gap.
  if (!canEmbed(app.href)) {
    return (
      <FrameNotice
        title={t('This page cannot be embedded')}
        description={t(
          'The target must be an http(s) address on another host. Open it in a new tab instead.'
        )}
        href={app.href}
      />
    )
  }

  const Icon = navAppIcon(app.icon)
  const sameSite = isSameSite(app.href)

  return (
    <div className='flex h-full min-h-0 flex-col'>
      <div className='flex shrink-0 items-center justify-between gap-3 border-b px-3 py-1.5'>
        <div className='flex min-w-0 items-center gap-2'>
          <Icon className='size-4 shrink-0 text-muted-foreground' />
          <span className='truncate text-sm font-medium'>{title}</span>
        </div>
        <div className='flex shrink-0 items-center gap-1'>
          <button
            type='button'
            className={actionClass}
            onClick={() => setReloadKey((key) => key + 1)}
          >
            <RefreshCw className='size-3' />
            {t('Reload')}
          </button>
          <a
            href={app.href}
            target='_blank'
            rel='noopener noreferrer'
            className={actionClass}
          >
            <ExternalLink className='size-3' />
            {t('Open in a new tab')}
          </a>
        </div>
      </div>
      {!sameSite && (
        <div className='shrink-0 border-b bg-muted/50 px-3 py-1.5 text-[11px] leading-relaxed text-muted-foreground'>
          {t(
            'This target is on another site. Browsers block third-party cookies in frames, so it may not stay signed in. Serving it under the same main domain avoids this; opening it in a new tab always works.'
          )}
        </div>
      )}
      <iframe
        key={reloadKey}
        src={app.href}
        title={title}
        className='min-h-0 w-full flex-1 border-0 bg-background'
        sandbox={FRAME_SANDBOX}
        allow='storage-access'
        referrerPolicy='no-referrer'
      />
    </div>
  )
}
