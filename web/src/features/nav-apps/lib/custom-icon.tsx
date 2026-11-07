import { useState, type ComponentType } from 'react'

/**
 * The layout contract types `NavItem.icon` as `React.ElementType`, so an image
 * icon can be supplied without touching any upstream file. Every call site
 * passes only `className`, which is all this component needs.
 */
export type NavAppIconComponent = ComponentType<{ className?: string }>

// Keyed by the chosen preset as well as the URL: the same image with two
// different fallbacks must not share one component identity.
const customIcons = new WeakMap<
  NavAppIconComponent,
  Map<string, NavAppIconComponent>
>()

/**
 * Builds the component that renders an administrator-supplied icon image.
 *
 * Caching keeps the component identity stable across re-renders: a new function
 * each time would remount the image on every scroll or reorder and restart the
 * request. When the image fails, the *selected preset* takes over — the URL and
 * the preset are two compatible ways of configuring the same slot, not two
 * unrelated features.
 *
 * `no-referrer` matters here: the console URL can carry a deployment's private
 * path, and the image host has no reason to receive it.
 */
export function customNavAppIcon(
  url: string,
  fallback: NavAppIconComponent
): NavAppIconComponent {
  let byUrl = customIcons.get(fallback)
  if (!byUrl) {
    byUrl = new Map()
    customIcons.set(fallback, byUrl)
  }
  const cached = byUrl.get(url)
  if (cached) return cached

  const Fallback = fallback
  const CustomIcon = ({ className }: { className?: string }) => {
    const [failed, setFailed] = useState(false)
    return (
      <span
        data-apiduo-custom-icon=''
        className={`inline-flex size-4 shrink-0 items-center justify-center ${className ?? ''}`}
      >
        {failed ? (
          <Fallback className='size-4' />
        ) : (
          <img
            src={url}
            alt=''
            aria-hidden='true'
            loading='lazy'
            decoding='async'
            referrerPolicy='no-referrer'
            className='size-4 object-contain'
            onError={() => setFailed(true)}
          />
        )}
      </span>
    )
  }
  CustomIcon.displayName = 'NavAppCustomIcon'
  byUrl.set(url, CustomIcon)
  return CustomIcon
}
