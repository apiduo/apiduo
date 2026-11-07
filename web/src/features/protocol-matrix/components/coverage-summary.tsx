import { Grid3x3 } from 'lucide-react'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Popover,
  PopoverContent,
  PopoverDescription,
  PopoverHeader,
  PopoverTitle,
  PopoverTrigger,
} from '@/components/ui/popover'
import { parseAdvancedCustomRouteModels } from '@/features/channels/lib/advanced-custom'
import type { AdvancedCustomRoute } from '@/features/channels/types'
import { apiduoBlockMark } from '@/features/apiduo-marks/marks'
import { cn } from '@/lib/utils'

import {
  buildCoverage,
  collectEndpoints,
  collectModels,
  coveredEndpoints,
} from '../lib/coverage'

/**
 * 覆盖检查：把当前的 `advanced_routes` 投影成「端点 × 模型」的格子，
 * 标出哪些组合会 404。
 *
 * 模型这一维用**渠道声明的模型清单**，不是 routes 里的规则——因为
 * 「客户端 model 留空作为兜底」的那一行接住的是全部模型，矩阵必须按全部模型展开。
 * 渠道没填模型时才退回 routes 里声明过的规则。
 *
 * 只读——它不写任何配置，也不引入第二份数据。
 */
export function CoverageSummary({
  routes,
  models,
}: {
  /**
   * 直接传 `config.advanced_routes` 本身，不要在调用处写 `?? []`——
   * 那会在每次渲染时新建一个空数组，把下面的 useMemo 全部打穿。
   */
  routes?: AdvancedCustomRoute[]
  /** 渠道的模型清单，逗号分隔的原始串。 */
  models?: string
}) {
  const { t } = useTranslation()

  const { endpoints, modelList, coverage, covered } = useMemo(() => {
    const declared = parseAdvancedCustomRouteModels(models ?? '')
    const nextModels = declared.length > 0 ? declared : collectModels(routes)
    const nextEndpoints = collectEndpoints(routes)
    return {
      endpoints: nextEndpoints,
      modelList: nextModels,
      coverage: buildCoverage(routes, nextModels, nextEndpoints),
      covered: coveredEndpoints(routes),
    }
  }, [routes, models])

  const hasModels = modelList.length > 0
  const label = hasModels
    ? `${coverage.forwarded}/${coverage.cells.length}`
    : `${covered.size}/${endpoints.length}`

  return (
    <Popover>
      <PopoverTrigger
        render={
          <Button
            type='button'
            variant='outline'
            size='sm'
            title={
              hasModels
                ? t('Endpoint and model combinations that will be forwarded')
                : t('Endpoints that have at least one route')
            }
          />
        }
      >
        <Grid3x3 className='size-3.5' aria-hidden='true' />
        <span
          className='apiduo-text'
          data-apiduo={apiduoBlockMark('channel:route-coverage')}
        >
          {t('Coverage')} {label}
        </span>
      </PopoverTrigger>
      <PopoverContent
        align='end'
        side='bottom'
        sideOffset={8}
        className='w-[min(46rem,calc(100vw-2rem))] p-3'
      >
        <PopoverHeader className='gap-1'>
          <PopoverTitle>{t('Route coverage')}</PopoverTitle>
          <PopoverDescription className='text-xs leading-relaxed'>
            {t(
              'Derived from the routes above. Nothing here is stored separately.'
            )}
          </PopoverDescription>
        </PopoverHeader>

        {hasModels ? (
          <div className='mt-3 max-h-[60vh] overflow-auto'>
            <table className='w-full border-collapse text-xs'>
              <thead>
                <tr>
                  <th className='text-muted-foreground pb-1 pr-2 text-left font-normal whitespace-nowrap'>
                    {t('Endpoint')}
                  </th>
                  {modelList.map((model) => (
                    <th
                      key={model}
                      className='text-muted-foreground px-1 pb-1 text-center font-normal whitespace-nowrap'
                    >
                      <code className='font-mono'>{model}</code>
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {endpoints.map((endpoint, rowIndex) => (
                  <tr key={endpoint}>
                    <td className='py-0.5 pr-2 whitespace-nowrap'>
                      <code className='font-mono'>{endpoint}</code>
                    </td>
                    {modelList.map((model, columnIndex) => {
                      const outcome =
                        coverage.cells[rowIndex * modelList.length + columnIndex]
                          ?.outcome
                      const converter =
                        outcome?.kind === 'forwarded'
                          ? outcome.converter
                          : undefined
                      const forwarded = outcome?.kind === 'forwarded'

                      return (
                        <td key={model} className='p-0.5'>
                          <span
                            className={cn(
                              'inline-flex w-full justify-center rounded px-1.5 py-0.5',
                              forwarded
                                ? 'bg-emerald-500/10 text-emerald-700 dark:text-emerald-400'
                                : 'bg-destructive/10 text-destructive'
                            )}
                            title={
                              forwarded
                                ? (converter ?? t('Native forwarding'))
                                : t('No route matches this combination')
                            }
                          >
                            {forwarded ? (converter ? '⇄' : '→') : '404'}
                          </span>
                        </td>
                      )
                    })}
                  </tr>
                ))}
              </tbody>
            </table>
            <p className='text-muted-foreground mt-2 text-xs'>
              {coverage.forwarded} {t('forwarded')} · {coverage.rejected}{' '}
              {t('rejected')}
            </p>
          </div>
        ) : (
          <div className='mt-3 flex max-h-[60vh] flex-col gap-1 overflow-auto'>
            <p className='text-muted-foreground text-xs'>
              {t(
                'Fill in the channel model list to see a per-model view. Until then the matrix shows endpoint coverage only.'
              )}
            </p>
            {endpoints.map((endpoint) => (
              <div
                key={endpoint}
                className='flex items-center justify-between gap-3 text-xs'
              >
                <code className='font-mono'>{endpoint}</code>
                {covered.has(endpoint) ? (
                  <Badge variant='secondary'>{t('Configured')}</Badge>
                ) : (
                  <Badge variant='outline'>{t('Rejected')}</Badge>
                )}
              </div>
            ))}
          </div>
        )}
      </PopoverContent>
    </Popover>
  )
}
