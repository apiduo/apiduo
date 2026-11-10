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
 * The trace column definition, kept in its own module.
 *
 * Splitting it from the cell component is not cosmetic: a file that exports
 * both a hook and components breaks React Fast Refresh ("only-export-components"),
 * and this column is added to a table that is edited constantly.
 *
 * The hook returns null instead of a disabled column:
 *   - non-admin: the log API already strips admin_info, so there is nothing to
 *     show; an empty column would advertise data the user cannot have.
 *   - tracing off: same reasoning, one level up.
 */
import type { ColumnDef } from '@tanstack/react-table'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ApiduoTraceCell } from '../components/apiduo-trace-cell'
import { getTraceConfig } from '../api'
import type { UsageLog } from '../data/schema'
import type { TraceConfig } from '../lib/trace-link'

export function useApiduoTraceColumn(
  canView: boolean
): ColumnDef<UsageLog> | null {
  const { t } = useTranslation()
  const [config, setConfig] = useState<TraceConfig | null | undefined>(undefined)

  useEffect(() => {
    if (!canView) return
    let cancelled = false
    // One request for the whole table, not per row. Fails closed: no config,
    // no column — a dead column is worse than a missing one.
    getTraceConfig()
      .then((c) => {
        if (!cancelled) setConfig(c)
      })
      .catch(() => {
        if (!cancelled) setConfig(null)
      })
    return () => {
      cancelled = true
    }
  }, [canView])

  if (!canView || !config || !config.enabled) return null

  return {
    id: 'apiduo_trace',
    header: t('Trace'),
    cell: ({ row }) => (
      <ApiduoTraceCell log={row.original} traceConfig={config} />
    ),
    size: 64,
    maxSize: 72,
    enableSorting: false,
    enableHiding: true,
  }
}
