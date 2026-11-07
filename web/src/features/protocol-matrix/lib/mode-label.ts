import {
  ADVANCED_CUSTOM_CONVERTER_OPTIONS,
} from '@/features/channels/lib/advanced-custom'
import type { AdvancedCustomConverter } from '@/features/channels/types'

import type { GridCellMode } from './grid'

/**
 * 转换器的三字母标记。
 *
 * 两个格子都带 `⇄` 时，光看图标分不出转去了哪个协议，所以用目标协议缩写标出来
 * ——这是矩阵里唯一需要「一眼看出差异」的信息。
 */
const CONVERTER_TAGS: Record<string, string> = {
  openai_chat_completions: 'OAI',
  openai_responses: 'RES',
  anthropic_messages: 'ANT',
  gemini_generate_content: 'GEM',
}

export function converterTag(converter: AdvancedCustomConverter): string {
  const target = converter.slice(converter.indexOf('_to_') + 4)
  for (const [key, tag] of Object.entries(CONVERTER_TAGS)) {
    if (target === key) return tag
  }
  return 'CVT'
}

/** 转换器的可读名字。上游也不翻译这套名字，这里保持一致。 */
export function converterLabel(converter: AdvancedCustomConverter): string {
  return (
    ADVANCED_CUSTOM_CONVERTER_OPTIONS.find(
      (option) => option.value === converter
    )?.label ?? converter
  )
}

/** 格子的模式 → 给人看的一句话。 */
export function modeLabel(mode: GridCellMode, offLabel: string): string {
  return mode === 'off' ? offLabel : converterLabel(mode)
}
