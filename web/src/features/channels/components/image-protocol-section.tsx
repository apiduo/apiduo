import { useTranslation } from 'react-i18next'

import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import {
  type ImageProtocolSettings,
  IMAGE_PROTOCOL_UPSTREAMS,
} from '@/features/channels/lib/image-protocol-settings'

/**
 * 图片协议：把「三个入口折叠成 canonical、由脚本选上游端点」这件事放到页面上。
 *
 * 主端点留空即跟随渠道主端点（仅当中心是图片端点），所以这里不重复填一遍
 * 默认值——placeholder 显示实际会用的地址，用户想覆盖才输入。
 */
export function ImageProtocolSection(props: {
  config: ImageProtocolSettings | null
  primaryEndpoint: string
  onChange: (next: ImageProtocolSettings | null) => void
}) {
  const { t } = useTranslation()
  const enabled = props.config?.enabled === true

  const patch = (partial: Partial<ImageProtocolSettings>) => {
    props.onChange({ ...props.config, enabled: true, ...partial })
  }

  return (
    <section className='rounded-lg border p-4'>
      <div className='flex items-start justify-between gap-4'>
        <div className='space-y-0.5'>
          <p className='text-sm font-medium'>{t('Image protocol')}</p>
          <p className='text-muted-foreground text-xs'>
            {t(
              'Folds the image entries into one canonical request and lets the upstream script pick the endpoint.'
            )}
          </p>
        </div>
        <Switch
          checked={enabled}
          onCheckedChange={(checked) => {
            if (!checked) {
              props.onChange(null)
              return
            }
            props.onChange({
              enabled: true,
              upstream: props.config?.upstream || IMAGE_PROTOCOL_UPSTREAMS[0].value,
              primary_path: props.config?.primary_path || '',
              edit_path: props.config?.edit_path || '',
            })
          }}
        />
      </div>

      {enabled ? (
        <div className='mt-3 grid grid-cols-1 gap-3 sm:grid-cols-2'>
          <div className='space-y-1'>
            <p className='text-sm'>{t('Upstream layout')}</p>
            <Select
              value={props.config?.upstream || IMAGE_PROTOCOL_UPSTREAMS[0].value}
              onValueChange={(value) => {
                if (typeof value === 'string') patch({ upstream: value })
              }}
            >
              <SelectTrigger size='sm'>
                <SelectValue />
              </SelectTrigger>
              <SelectContent alignItemWithTrigger={false}>
                <SelectGroup>
                  {IMAGE_PROTOCOL_UPSTREAMS.map((option) => (
                    <SelectItem key={option.value} value={option.value}>
                      {t(option.label)}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
          </div>

          <div className='space-y-1'>
            <p className='text-sm'>{t('Primary path')}</p>
            <Input
              value={props.config?.primary_path || ''}
              placeholder={props.primaryEndpoint || '/v1/images/generations'}
              onChange={(event) => patch({ primary_path: event.target.value })}
            />
          </div>

          <div className='space-y-1 sm:col-span-2'>
            <p className='text-sm'>{t('Edit path override')}</p>
            <Input
              value={props.config?.edit_path || ''}
              placeholder={t('Derived from the primary path when empty')}
              onChange={(event) => patch({ edit_path: event.target.value })}
            />
          </div>

          <p className='text-muted-foreground text-xs sm:col-span-2'>
            {t(
              'Leave the primary path empty to follow the primary endpoint (only when it looks like an image endpoint).'
            )}
          </p>
        </div>
      ) : null}
    </section>
  )
}
