import type { Control } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import {
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
} from '@/components/ui/form'
import { Switch } from '@/components/ui/switch'

import type { ChannelFormValues } from '../../lib/channel-form'

/**
 * A channel remaps models through model_mapping. This flag decides whether the
 * mapping result is shown to clients or hidden behind the requested name.
 */
export function hasConfiguredModelMapping(value: unknown): boolean {
  if (typeof value !== 'string' || !value.trim()) return false
  try {
    const parsed = JSON.parse(value)
    return (
      !!parsed &&
      typeof parsed === 'object' &&
      !Array.isArray(parsed) &&
      Object.keys(parsed).length > 0
    )
  } catch {
    return false
  }
}

export function RemapHardeningField(props: {
  control: Control<ChannelFormValues>
  disabled: boolean
  modelMapping: unknown
}) {
  const { t } = useTranslation()
  const hasMapping = hasConfiguredModelMapping(props.modelMapping)

  return (
    <FormField
      control={props.control}
      name='remap_hardening'
      render={({ field }) => (
        <FormItem className='flex items-center justify-between gap-4 px-4 py-3'>
          <div className='space-y-0.5'>
            <FormLabel>{t('Hide upstream model from clients')}</FormLabel>
            <FormDescription>
              {hasMapping
                ? t(
                    'Responses report the requested model, and upstream error text is replaced by the gateway error catalog. Only models this channel remaps are affected.'
                  )
                : t('Configure a model mapping on this channel first.')}
            </FormDescription>
          </div>
          <FormControl>
            <Switch
              disabled={props.disabled || !hasMapping}
              checked={hasMapping && !!field.value}
              onCheckedChange={field.onChange}
            />
          </FormControl>
        </FormItem>
      )}
    />
  )
}
