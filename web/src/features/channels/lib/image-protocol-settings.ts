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
  primary_path?: string
  edit_path?: string
}

export const IMAGE_PROTOCOL_UPSTREAMS = [
  { value: 'seedream', label: 'Seedream single endpoint' },
  { value: 'gpt-image-2', label: 'GPT Image 2 dual endpoint' },
] as const

const DEFAULT_UPSTREAM = IMAGE_PROTOCOL_UPSTREAMS[0].value

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
    upstream: trimmed(config.upstream) || DEFAULT_UPSTREAM,
    primary_path: trimmed(config.primary_path),
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
  if (trimmed(next.primary_path)) config.primary_path = trimmed(next.primary_path)
  if (trimmed(next.edit_path)) config.edit_path = trimmed(next.edit_path)
  settings.image_protocol = config
  return JSON.stringify(settings)
}
