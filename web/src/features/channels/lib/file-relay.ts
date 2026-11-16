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
 * still has to declare each purpose before it receives traffic.
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

/**
 * Routing-name prefix of one file purpose, mirroring endpoint.FileUploadPrefix
 * in pkg/endpoint.
 *
 * The purpose lives in the model list rather than in the settings blob because
 * the model list is what the gateway routes on: a channel serves
 * "@files/upload/batch" exactly the way it serves a model name. A field in the
 * settings blob would be a second capability list that routing never reads, and
 * a stale one is invisible.
 */
export const FILE_UPLOAD_MODEL_PREFIX = '@files/upload/'

export function fileUploadModel(purpose: string): string {
  return `${FILE_UPLOAD_MODEL_PREFIX}${purpose}`
}

/**
 * Recovers the purposes a channel declares from its comma-separated model list.
 * Unknown and empty entries are dropped so a hand-edited list cannot render an
 * empty checkbox.
 */
export function readFileUploadPurposes(models: string): string[] {
  return models
    .split(',')
    .map((name) => name.trim())
    .filter((name) => name.length > FILE_UPLOAD_MODEL_PREFIX.length)
    .filter((name) => name.startsWith(FILE_UPLOAD_MODEL_PREFIX))
    .map((name) => name.slice(FILE_UPLOAD_MODEL_PREFIX.length))
}

/**
 * Returns the model list with one purpose added or removed. Order is preserved
 * and duplicates are dropped: the backend compares the list as a set, so a
 * repeated name would only make the console disagree with the form.
 */
export function withFileUploadPurpose(
  models: string,
  purpose: string,
  enabled: boolean
): string {
  const name = fileUploadModel(purpose)
  const names = models
    .split(',')
    .map((entry) => entry.trim())
    .filter((entry) => entry.length > 0 && entry !== name)
  if (enabled) {
    names.push(name)
  }
  return names.join(',')
}

export interface FileRelayConfig {
  max_upload_bytes: number
}

const EMPTY_FILE_RELAY: FileRelayConfig = { max_upload_bytes: 0 }

/**
 * Reads the file relay block out of the channel's settings JSON.
 *
 * Only the per-file ceiling stays here. The value rides along with the normal
 * channel save instead of needing its own endpoint, and buildSettingsJSON
 * preserves keys it does not know, so nothing has to be migrated or cascaded
 * when a channel is deleted.
 */
export function readFileRelayConfig(
  settings: Record<string, unknown>
): FileRelayConfig {
  const raw = settings.file_relay
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) {
    return EMPTY_FILE_RELAY
  }
  const record = raw as Record<string, unknown>
  const maxBytes =
    typeof record.max_upload_bytes === 'number' ? record.max_upload_bytes : 0
  return { max_upload_bytes: maxBytes }
}

/**
 * Suggested purposes per channel type. Purely advisory: a channel still has to
 * declare them, and the backend routes on whatever the model list ends up
 * containing.
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
