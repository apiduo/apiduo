/**
 * 渠道级图片协议（`settings.image_protocol`）的读写。
 *
 * 协议本身的语义在后端：`enabled` / `upstream` / `primary_path` / `edit_path`。
 * 这里只做「保留其它设置、剪掉空字段、关掉就删键」的收敛，避免页面上出现
 * `{"enabled": false}` 这种既占位又误导的存储形态。
 */

export type ImageProtocolSettings = {
  enabled?: boolean
  upstream?: string
  edit_path?: string
}

/**
 * 上游的图像端点约定。label 只描述**协议形状**，不绑型号：
 * 方舟换一代模型（seedream 5 / 6）布局不变，写死型号的选项会在下一代
 * 变成错误信息；OpenAI 这边同理，dall-e / gpt-image-1 / gpt-image-2 都是
 * 同一套 generations + edits 布局。
 *
 * `value` 是存储值，直接写协议名（ark / openai）；不保留历史型号名，
 * 旧配置在渠道编辑页重新选一次即可。
 */
export const IMAGE_PROTOCOL_UPSTREAMS = [
  { value: 'ark', label: 'ARK protocol (single endpoint)' },
  { value: 'openai', label: 'OpenAI protocol (dual endpoint)' },
] as const

const DEFAULT_UPSTREAM = IMAGE_PROTOCOL_UPSTREAMS[0].value

/**
 * 与后端 `dto.NormalizeImageUpstream` 同一份映射。
 *
 * 0.0.74 之前存储值写的是型号名（seedream / gpt-image-2），改名成协议名时
 * 没有做数据迁移：老渠道的 settings 里仍是旧字符串。后端已在读取侧兼容
 * （否则每次图片请求都 400），前端也得跟上——否则下拉里找不到匹配项，
 * 界面上直接显示一个裸的 `seedream`，用户既看不懂也选不回原值。
 */
export function normalizeImageUpstream(raw: string): string {
  switch (raw.trim().toLowerCase()) {
    case 'seedream':
      return 'ark'
    case 'gpt-image-2':
      return 'openai'
    default:
      // 未知值也收敛成小写，与后端 `strings.ToLower` 一致：
      // 两边归一口径不同时，界面上会出现「选了但存不回去」的假象。
      return raw.trim().toLowerCase()
  }
}

function parseSettings(settingsJson?: string): Record<string, unknown> {
  if (!settingsJson?.trim()) return {}
  try {
    const parsed: unknown = JSON.parse(settingsJson)
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
      return parsed as Record<string, unknown>
    }
  } catch {
    // 坏 JSON 当空设置：页面上的改动会整体覆盖，和保存行为一致。
  }
  return {}
}

function trimmed(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

export function readImageProtocol(
  settingsJson?: string
): ImageProtocolSettings | null {
  const raw = parseSettings(settingsJson).image_protocol
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null
  const config = raw as Record<string, unknown>
  const enabled = config.enabled === true
  return {
    enabled,
    upstream: normalizeImageUpstream(
      trimmed(config.upstream) || DEFAULT_UPSTREAM
    ),
    edit_path: trimmed(config.edit_path),
  }
}

export function writeImageProtocol(
  settingsJson: string | undefined,
  next: ImageProtocolSettings | null
): string {
  const settings = parseSettings(settingsJson)
  if (!next?.enabled) {
    delete settings.image_protocol
    return JSON.stringify(settings)
  }
  const config: Record<string, unknown> = {
    enabled: true,
    upstream: trimmed(next.upstream) || DEFAULT_UPSTREAM,
  }
  // 主端点只有 settings.primary_endpoint 一处，协议里不再存 primary_path：
  // 两个字段并存时谁生效取决于读取顺序，界面会照 A 显示、请求按 B 发。
  // 这里不写它，老配置下次保存时自然被清掉。
  if (trimmed(next.edit_path)) config.edit_path = trimmed(next.edit_path)
  settings.image_protocol = config
  return JSON.stringify(settings)
}
