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
along with this program. If not <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
/**
 * "Open in Langfuse" link for the log details dialog.
 *
 * Visibility is enforced by the backend: the user-scoped log API strips
 * admin_info (where trace_id lives), so a non-admin never receives the id
 * and this component renders nothing for them regardless of isAdmin. The
 * isAdmin prop is a second lock, not the first.
 *
 * The config request happens once per dialog mount and fails closed: any
 * error means no link, never a broken one.
 */
import { ExternalLink } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { getTraceConfig } from '../api'
import { buildTraceUrl, type TraceConfig } from '../lib/trace-link'

export function ApiduoTraceLink({
  traceId,
  isAdmin,
}: {
  traceId?: string
  isAdmin: boolean
}) {
  const { t } = useTranslation()
  const [config, setConfig] = useState<TraceConfig | null | undefined>(
    undefined
  )
  const hasId = Boolean(traceId?.trim())
  const eligible = isAdmin && hasId

  useEffect(() => {
    if (!eligible) return
    let cancelled = false
    getTraceConfig().then((c) => {
      if (!cancelled) setConfig(c)
    })
    return () => {
      cancelled = true
    }
  }, [eligible])

  if (!eligible || config === undefined || config === null) return null

  const url = buildTraceUrl(config, traceId)
  if (!url) return null

  return (
    <a
      href={url}
      target='_blank'
      rel='noopener noreferrer'
      data-apiduo='text'
      className='inline-flex items-center gap-1.5 text-sm font-medium underline-offset-4 hover:underline'
      style={{ color: 'var(--apiduo-ink)' }}
    >
      <ExternalLink className='size-3.5' aria-hidden />
      {t('View trace in Langfuse')}
    </a>
  )
}
