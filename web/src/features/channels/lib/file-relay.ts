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

/**
 * Channel types with a known /v1/files protocol. Mirrors BackendForChannel in
 * service/filerelay: a type listed here can carry an upload, but the channel
 * still has to opt in to each purpose before it receives traffic.
 */
const FILE_RELAY_CHANNEL_TYPES: ReadonlySet<number> = new Set([
  1, // OpenAI
  8, // Custom, treated as OpenAI-compatible
  25, // Moonshot
  26, // Zhipu v4
])

export function supportsFileRelay(channelType: number): boolean {
  return FILE_RELAY_CHANNEL_TYPES.has(channelType)
}

/**
 * Purposes the gateway understands. The list is deliberately flat: a channel
 * declares which of these it serves and the upload purpose is the only routing
 * signal /v1/files has.
 */
export const FILE_RELAY_PURPOSES = [
  'file-extract',
  'image',
  'video',
  'batch',
  'vision',
  'assistants',
  'fine-tune',
] as const

export type FileRelayPurpose = (typeof FILE_RELAY_PURPOSES)[number]

export interface FileRelayConfig {
  purposes: string[]
  max_upload_bytes: number
}

const EMPTY_FILE_RELAY: FileRelayConfig = { purposes: [], max_upload_bytes: 0 }

/**
 * Reads the file relay block out of the channel's settings JSON.
 *
 * The value stays a plain JSON field rather than a form column: the channel
 * settings blob is already the place for per-channel extensions, and
 * buildSettingsJSON preserves keys it does not know, so nothing has to be
 * migrated or cascaded when a channel is deleted.
 */
export function readFileRelayConfig(
  settings: Record<string, unknown>
): FileRelayConfig {
  const raw = settings.file_relay
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) {
    return EMPTY_FILE_RELAY
  }
  const record = raw as Record<string, unknown>
  const purposes = Array.isArray(record.purposes)
    ? record.purposes.filter((item): item is string => typeof item === 'string')
    : []
  const maxBytes =
    typeof record.max_upload_bytes === 'number' ? record.max_upload_bytes : 0
  return { purposes, max_upload_bytes: maxBytes }
}

/**
 * Suggested purposes per channel type. Purely advisory: a channel still has to
 * opt in, and the backend validates whatever it is given.
 */
export function defaultFileRelayPurposes(channelType: number): string[] {
  switch (channelType) {
    case 25: // Moonshot
      return ['file-extract', 'image', 'video', 'batch']
    case 26: // Zhipu v4
      return ['file-extract', 'batch']
    default:
      return ['batch', 'vision', 'assistants', 'fine-tune']
  }
}
