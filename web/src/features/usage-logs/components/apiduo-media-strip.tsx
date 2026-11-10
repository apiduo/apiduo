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
/**
 * Thumbnails for whatever images a trace payload carried.
 *
 * Why this exists at all: an image response is a 500 KB line of base64, and a
 * JSON tree can only ever say "something came back". Lifting the pixels out is
 * the difference between "the upstream returned 200" and "the upstream returned
 * a picture of a cat".
 *
 * Zoom is an inline overlay rather than a new tab, for a concrete reason:
 * browsers block top-level navigation to a `data:` URL, so the obvious "open
 * the image in a window" move silently does nothing for a b64 response.
 */
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import type { MediaItem } from '../lib/payload-media'

export function MediaStrip({ items }: { items: MediaItem[] }) {
  const { t } = useTranslation()
  const [zoom, setZoom] = useState<MediaItem | null>(null)
  const [broken, setBroken] = useState<Record<string, boolean>>({})

  useEffect(() => {
    if (!zoom) return
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setZoom(null)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [zoom])

  return (
    <div className='mb-2' data-apiduo='media-strip'>
      <div className='flex flex-wrap gap-2'>
        {items.map((item, index) => (
          <button
            key={item.key}
            type='button'
            onClick={() => setZoom(item)}
            title={item.label}
            aria-label={t('Image {{index}}', { index: index + 1 })}
            className='bg-muted/40 hover:border-foreground/30 relative size-20 shrink-0 overflow-hidden rounded-lg border transition-colors'
          >
            {broken[item.key] ? (
              <span className='text-muted-foreground flex size-full items-center justify-center px-1 text-center text-[10px] leading-tight'>
                {t('Preview unavailable')}
              </span>
            ) : (
              <img
                src={item.src}
                alt=''
                loading='lazy'
                // The CDN in front of a generated image may check the referer,
                // and a trace viewer has no business telling it where the
                // reader came from.
                referrerPolicy='no-referrer'
                onError={() =>
                  setBroken((prev) => ({ ...prev, [item.key]: true }))
                }
                className='size-full object-cover'
              />
            )}
            <span className='absolute inset-x-0 bottom-0 truncate bg-black/55 px-1 py-0.5 text-[9.5px] text-white'>
              {item.label}
            </span>
          </button>
        ))}
      </div>
      <p className='text-muted-foreground mt-1 text-[10.5px]'>
        {t('{{count}} image(s) · click to enlarge', { count: items.length })}
      </p>
      {zoom && (
        <div
          role='dialog'
          aria-modal='true'
          aria-label={zoom.label}
          onClick={() => setZoom(null)}
          className='fixed inset-0 z-[70] flex cursor-zoom-out items-center justify-center bg-black/75 p-6'
        >
          <img
            src={zoom.src}
            alt={zoom.label}
            referrerPolicy='no-referrer'
            onClick={(event) => event.stopPropagation()}
            className='max-h-full max-w-full cursor-default rounded-lg object-contain shadow-2xl'
          />
        </div>
      )}
    </div>
  )
}
