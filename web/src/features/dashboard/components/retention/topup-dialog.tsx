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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  roundTopUp,
  type RunwayResult,
} from '@/features/dashboard/lib/forecast'
import type { UserBalanceItem } from '@/features/dashboard/types'
import { adjustUserQuota } from '@/features/users/api'
import { getCurrencyLabel } from '@/lib/currency'
import {
  formatNumber,
  formatQuota,
  parseQuotaFromDollars,
  quotaUnitsToDollars,
} from '@/lib/format'
import { handleServerError } from '@/lib/handle-server-error'
import { cn } from '@/lib/utils'

interface TopUpDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  user: UserBalanceItem
  runway: RunwayResult
  onSuccess: () => void
}

/**
 * Tops a customer up from the balance health board.
 *
 * The suggested amount is the same number the table already shows, converted to
 * whatever unit the admin has configured. Rounding happens in display units so
 * the field is pre-filled with an amount a human would actually type ($50, not
 * $47.83).
 */
export function TopUpDialog(props: TopUpDialogProps) {
  const { t } = useTranslation()
  const [amount, setAmount] = useState(() => {
    const suggested = roundTopUp(quotaUnitsToDollars(props.runway.suggestedTopUp))
    return suggested > 0 ? String(suggested) : ''
  })
  const [loading, setLoading] = useState(false)

  const displayAmount = Number.parseFloat(amount) || 0
  const quotaValue = parseQuotaFromDollars(Math.abs(displayAmount))
  const afterQuota = props.user.quota + quotaValue
  const coveredDays =
    props.runway.dailyAverage > 0 ? afterQuota / props.runway.dailyAverage : null

  const presets = (() => {
    const suggested = roundTopUp(quotaUnitsToDollars(props.runway.suggestedTopUp))
    if (suggested <= 0) return []
    return [suggested, suggested * 2, suggested * 3]
  })()

  const canSubmit = quotaValue > 0 && !loading

  const handleConfirm = async () => {
    if (!canSubmit) return
    setLoading(true)
    try {
      const result = await adjustUserQuota({
        id: props.user.user_id,
        action: 'add_quota',
        mode: 'add',
        value: quotaValue,
      })
      if (result.success) {
        toast.success(t('Quota topped up'))
        props.onOpenChange(false)
        props.onSuccess()
      } else {
        handleServerError(result, t('Top-up failed'))
      }
    } catch (error) {
      handleServerError(error, t('Top-up failed'))
    } finally {
      setLoading(false)
    }
  }

  let preview = t('Enter an amount above to preview the new balance')
  if (quotaValue > 0 && coveredDays === null) {
    preview = t('New balance {{balance}}', { balance: formatQuota(afterQuota) })
  } else if (quotaValue > 0 && coveredDays !== null) {
    preview = t('New balance {{balance}}, covers {{days}} days', {
      balance: formatQuota(afterQuota),
      days: formatNumber(Math.floor(coveredDays)),
    })
  }

  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('Top up quota')}
      description={`${props.user.username || `#${props.user.user_id}`} · ${t(
        'Current balance'
      )} ${formatQuota(props.user.quota)} · ${t('Daily spend')} ${formatQuota(
        props.runway.dailyAverage
      )}`}
      contentHeight='auto'
      bodyClassName='space-y-4'
      footer={
        <>
          <Button variant='outline' onClick={() => props.onOpenChange(false)}>
            {t('Cancel')}
          </Button>
          <Button onClick={handleConfirm} disabled={!canSubmit}>
            {loading ? t('Processing...') : t('Confirm')}
          </Button>
        </>
      }
    >
      <div className='space-y-4'>
        <div className='space-y-2'>
          <Label htmlFor='topup-amount'>{t('Amount')}</Label>
          <Input
            id='topup-amount'
            type='number'
            min='0'
            step='any'
            inputMode='decimal'
            value={amount}
            autoFocus
            onChange={(event) => setAmount(event.target.value)}
            placeholder={t('Enter amount in {{currency}}', {
              currency: getCurrencyLabel(),
            })}
          />
        </div>

        {presets.length > 0 && (
          <div className='flex flex-wrap gap-1.5'>
            {presets.map((preset, index) => (
              <Button
                key={preset}
                type='button'
                size='sm'
                variant={displayAmount === preset ? 'secondary' : 'outline'}
                className={cn('h-7 px-2 font-mono text-xs tabular-nums')}
                onClick={() => setAmount(String(preset))}
              >
                {index === 0 ? `${t('Suggested')} ` : ''}
                {formatQuota(parseQuotaFromDollars(preset))}
              </Button>
            ))}
          </div>
        )}

        <div className='text-muted-foreground text-xs'>{preview}</div>
      </div>
    </Dialog>
  )
}
