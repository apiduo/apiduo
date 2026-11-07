/**
 * 模型按模态分组：文本 / 图片 / 视频 / 其他。
 *
 * 为什么靠名字判断，而不是查元数据：矩阵拿到的只有渠道里那串模型名
 * （`models` 字段，逗号分隔的纯文本）。定价目录里虽然有 `supported_endpoint_types`，
 * 但那是另一份数据、另一次请求，而且渠道里常常有目录里没有的自定义模型名
 * ——查不到就变成「未分类」，比按名字猜更糟。
 *
 * 名字判断是启发式，所以规则集中在这里、有测试固定，并且**只影响展示顺序**：
 * 表格的列可以任意排，单元格是按模型名查的（`cellAt(grid, endpoint, model)`），
 * 生成路由时用的仍是渠道原本的顺序。分错了组，格子内容不会错。
 */

export type ModelGroupKey = 'text' | 'image' | 'video' | 'other'

export type ModelGroup = {
  key: ModelGroupKey
  models: string[]
}

/** 顺序即展示顺序。`other` 放最后，避免向量/语音这类模型插在图文之间。 */
export const MODEL_GROUP_ORDER: readonly ModelGroupKey[] = [
  'text',
  'image',
  'video',
  'other',
]

/**
 * 判断顺序是**视频 → 图片 → 其他 → 文本**，因为名字会串：
 * - `wan2.1-t2v` 是视频，`wanx` 是图片，两者都含 `wan`
 * - `qwen-image` 是图片，`qwen-vl` 是文本（视觉理解不是生图）
 * - `gpt-4o-audio` 是语音，别因为 `gpt` 就归到文本
 * 越具体的先判，剩下的默认文本——大多数渠道里文本模型占绝大多数，
 * 让「没认出来」落在文本，比落到「其他」更接近事实。
 */
const VIDEO_HINTS = [
  'veo',
  'sora',
  'kling',
  'seedance',
  // 别用 `gen-3` / `gen-4` 这种短词：`imagen-4.0-generate-001` 里的
  // "gen-4" 会被命中，把图片模型判成视频。Runway 靠 `runway` 前缀就够。
  'runway',
  'pika',
  'hailuo',
  'minimax-video',
  'vidu',
  'luma',
  'ray-2',
  't2v',
  'i2v',
  'video',
  'jimeng',
  'pixverse',
  'cogvideo',
  'hunyuan-video',
]

const IMAGE_HINTS = [
  'seedream',
  'seededit',
  'dall-e',
  'dalle',
  'gpt-image',
  'flux',
  'sdxl',
  'stable-diffusion',
  'imagen',
  'midjourney',
  'wanx',
  'cogview',
  'kolors',
  'qwen-image',
  'hunyuan-image',
  'nano-banana',
  'ideogram',
  'recraft',
  't2i',
  'i2i',
  'image',
]

const OTHER_HINTS = [
  'embed',
  'bge-',
  'gte-',
  'm3e',
  'rerank',
  'whisper',
  'tts',
  'asr',
  'speech',
  'audio',
  'moderation',
]

export function classifyModel(model: string): ModelGroupKey {
  const name = model.trim().toLowerCase()
  if (!name) return 'text'
  if (VIDEO_HINTS.some((hint) => name.includes(hint))) return 'video'
  if (IMAGE_HINTS.some((hint) => name.includes(hint))) return 'image'
  if (OTHER_HINTS.some((hint) => name.includes(hint))) return 'other'
  return 'text'
}

/**
 * 分组并保持渠道里的原始顺序（组内顺序不动，只把同组的挪到一起）。
 * 空组不出现——只有文本模型的渠道不该多出三个空表头。
 */
export function groupModels(models: readonly string[]): ModelGroup[] {
  const buckets = new Map<ModelGroupKey, string[]>()

  for (const model of models) {
    const name = model.trim()
    if (!name) continue
    const key = classifyModel(name)
    const bucket = buckets.get(key)
    if (!bucket) {
      buckets.set(key, [name])
    } else if (!bucket.includes(name)) {
      bucket.push(name)
    }
  }

  return MODEL_GROUP_ORDER.filter((key) => buckets.has(key)).map((key) => ({
    key,
    models: buckets.get(key) as string[],
  }))
}
