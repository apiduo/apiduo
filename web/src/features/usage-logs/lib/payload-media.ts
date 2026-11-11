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
 * Pulls images out of a trace payload so the drawer can draw them.
 *
 * Why this is its own module rather than part of conversation.ts:
 * conversation.ts turns a body into chat bubbles, and a bubble is text — it
 * deliberately drops image parts into `[image]`. This module does the opposite.
 * It keeps only the pixels, and returns a copy of the body whose heavy values
 * are swapped for short markers so the JSON tree rendered next to it does not
 * have to lay out a one-megabyte string.
 *
 * Scope is the shapes this gateway actually sees:
 *   - OpenAI images: `data[].b64_json` / `data[].url`
 *   - chat vision input: `image_url` as a string, as `{url}`, or a data URI
 *   - Anthropic: `source: {type: "base64", media_type, data}`
 *   - Gemini: `inlineData` / `inline_data` with `mimeType` + `data`
 *   - any string value that is itself a `data:image/…` URI
 *
 * Why not render every URL: a remote URL is only treated as an image when the
 * path looks like one, or when it sits next to `b64_json` / `output_format`.
 * Guessing wider than that turns every `link` field in an arbitrary JSON body
 * into a broken <img>.
 */

export interface MediaItem {
  /** Unique within one payload; doubles as the React key. */
  key: string
  /** Ready for <img src>: a data URI or an http(s) URL. */
  src: string
  /** Full-resolution source when `src` is only a thumbnail. */
  full?: string
  /** Caption text, already formatted: "PNG · 480 KB" or "URL". */
  label: string
  /** Remote sources are the only ones that can be opened in a new tab. */
  remote: boolean
}

export interface MediaScan {
  items: MediaItem[]
  /** Same shape as the input, with extracted values replaced by markers. */
  view: unknown
}

/** Enough to cover a batch response without turning the drawer into a gallery. */
const MAX_ITEMS = 12
/** Guards against a pathological body; real payloads nest ~6 levels. */
const MAX_DEPTH = 14
/** Below this a data URI is a probe, not a picture (1×1 tracking pixels). */
const MIN_URI_CHARS = 120
/** An <img> this large costs more than it tells; the marker keeps the size. */
const MAX_RENDER_BYTES = 4 * 1024 * 1024

const IMAGE_EXT = /\.(png|jpe?g|webp|gif|avif|bmp)(?:[?#]|$)/i
const HTTP_URL = /^https?:\/\//i
/**
 * A URL this gateway minted for an externalized image. The bytes live in the
 * trace store, not in the payload, so the tile loads a 512 px JPEG instead of
 * the multi-megabyte original; `full` keeps the original for the zoom layer.
 */
const APIDUO_MEDIA_URL = /\/api\/apiduo\/trace\/media\/[A-Za-z0-9_-]{6,64}\.(png|jpe?g|webp|gif)(?:[?#]|$)/i
const THUMB_WIDTH = 512
/** SVG is excluded on purpose: it is a document, not a picture. */
const SAFE_MIME = /^image\/(png|jpe?g|webp|gif|avif|bmp)$/i

/**
 * Fields whose media is already lifted by a dedicated branch above. Walking
 * them again would extract the same picture twice and hand the second copy a
 * different marker.
 */
const SKIP_KEYS = new Set(['b64_json', 'source', 'image_url', 'inlineData', 'inline_data'])

function asRecord(value: unknown): Record<string, unknown> | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null
  return value as Record<string, unknown>
}

/** Decoded size of a base64 run, without allocating the bytes. */
function base64Bytes(b64: string): number {
  let len = 0
  let pad = 0
  for (const ch of b64) {
    if (ch === '\n' || ch === '\r' || ch === ' ' || ch === '\t') continue
    len += 1
    pad = ch === '=' ? pad + 1 : 0
  }
  return Math.max(0, Math.floor((len * 3) / 4) - pad)
}

export function humanBytes(bytes: number): string {
  if (bytes <= 0) return ''
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

/** `image/jpeg` → `JPEG`. Short enough to sit inside a tree marker. */
function shortMime(mime: string): string {
  const sub = mime.split('/')[1] ?? 'image'
  return sub.toUpperCase()
}

/** OpenAI reports `output_format` as `jpeg` / `png`, not as a MIME type. */
function mimeFromFormat(value: unknown): string | null {
  if (typeof value !== 'string') return null
  const name = value.toLowerCase()
  if (name === 'jpg' || name === 'jpeg') return 'image/jpeg'
  const mime = `image/${name}`
  return SAFE_MIME.test(mime) ? mime : null
}

/** Parses `data:image/png;base64,…` into its parts, or null. */
function splitDataUri(uri: string): { mime: string; b64: string } | null {
  if (!uri.startsWith('data:')) return null
  const comma = uri.indexOf(',')
  if (comma < 0) return null
  const header = uri.slice(5, comma)
  if (!header.includes(';base64')) return null
  const mime = header.split(';')[0]?.trim() ?? ''
  if (!SAFE_MIME.test(mime)) return null
  return { mime, b64: uri.slice(comma + 1) }
}

function buildDataUri(mime: string, raw: string): string | null {
  if (!SAFE_MIME.test(mime)) return null
  const b64 = raw.replaceAll(/[\r\n\s]/g, '')
  if (!b64) return null
  return `data:${mime};base64,${b64}`
}

function looksLikeRemoteImage(url: string): boolean {
  return HTTP_URL.test(url) && IMAGE_EXT.test(url)
}

/** `…/media/abc123.png` → `PNG`; anything else keeps the generic caption. */
function mediaLabel(url: string): string | null {
  const m = /\.([A-Za-z0-9]{2,4})(?:[?#]|$)/.exec(url)
  if (!m) return null
  const ext = m[1].toLowerCase()
  if (ext === 'jpg' || ext === 'jpeg') return 'JPEG'
  return ext.toUpperCase()
}

/** Appends the width the media proxy understands; it ignores everything else. */
function thumbnailUrl(url: string): string {
  return `${url}${url.includes('?') ? '&' : '?'}w=${THUMB_WIDTH}`
}

/**
 * Scans `root` for images.
 *
 * Returns `view`, a copy of the input with every extracted value replaced by a
 * short marker such as `[image 1 · PNG · 480 KB]`. Copy-on-write: a subtree
 * that holds no media is returned by reference, so a chat payload that happens
 * to include one pasted screenshot does not deep-clone four hundred messages.
 */
export function scanMedia(root: unknown): MediaScan {
  const items: MediaItem[] = []

  /** Registers one image and returns the marker to leave in the tree. */
  const add = (src: string, label: string, remote: boolean): string => {
    // Even a rejected item leaves a marker: dropping the value silently would
    // read as "the upstream sent nothing", which is a different fact.
    if (items.length >= MAX_ITEMS) {
      return `[${label} · over the ${MAX_ITEMS}-image cap]`
    }
    const key = `m${items.length + 1}`
    items.push({ key, src, label, remote })
    return `[image ${items.length} · ${label}]`
  }

  /** Handles a value that may itself be an image source. */
  const takeUri = (value: string): string | null => {
    const uri = splitDataUri(value)
    if (!uri) return null
    if (value.length < MIN_URI_CHARS) return null
    const bytes = base64Bytes(uri.b64)
    if (bytes > MAX_RENDER_BYTES) {
      return `[image · ${shortMime(uri.mime)} · ${humanBytes(bytes)} · too large to preview]`
    }
    return add(value, `${shortMime(uri.mime)} · ${humanBytes(bytes)}`, false)
  }

  const takeUrl = (value: string, marker: string): string => {
    if (APIDUO_MEDIA_URL.test(value)) {
      // Our own externalized image: the tile can be a thumbnail while the
      // zoom layer still opens the original.
      const label = mediaLabel(value) ?? marker
      const key = `m${items.length + 1}`
      if (items.length >= MAX_ITEMS) {
        return `[${label} · over the ${MAX_ITEMS}-image cap]`
      }
      items.push({ key, src: thumbnailUrl(value), full: value, label, remote: true })
      return `[image ${items.length} · ${label}]`
    }
    return add(value, marker, true)
  }

  const visitObject = (
    rec: Record<string, unknown>,
    depth: number,
    inImageResult: boolean
  ): unknown => {
    let copy: Record<string, unknown> | null = null
    const put = (key: string, value: unknown) => {
      if (!copy) copy = { ...rec }
      copy[key] = value
    }

    // 1. OpenAI images result item: `data[]` is a list of these. The envelope
    // (`created` + `data`) marks every item as an image result even when the
    // only field is a signed URL with no file extension — that shape is common
    // for gpt-image-1 and unguessable from the URL alone.
    const isImageResult =
      inImageResult ||
      'b64_json' in rec ||
      'output_format' in rec ||
      'revised_prompt' in rec

    const b64 = rec.b64_json
    if (typeof b64 === 'string' && HTTP_URL.test(b64)) {
      put('b64_json', takeUrl(b64, 'URL'))
    } else if (typeof b64 === 'string' && b64.length > 40) {
      const mime = mimeFromFormat(rec.output_format) ?? 'image/png'
      const bytes = base64Bytes(b64)
      if (bytes > MAX_RENDER_BYTES) {
        put(
          'b64_json',
          `[image · ${shortMime(mime)} · ${humanBytes(bytes)} · too large to preview]`
        )
      } else {
        const src = buildDataUri(mime, b64)
        if (src) put('b64_json', add(src, `${shortMime(mime)} · ${humanBytes(bytes)}`, false))
      }
    }

    const url = rec.url
    if (typeof url === 'string' && HTTP_URL.test(url)) {
      if (looksLikeRemoteImage(url) || isImageResult) put('url', takeUrl(url, 'URL'))
    }

    // 2. Anthropic image block: {type:"image", source:{type:"base64", …}}.
    const source = asRecord(rec.source)
    if (source && typeof source.data === 'string' && HTTP_URL.test(source.data)) {
      put('source', { ...source, data: takeUrl(source.data, 'URL') })
    } else if (source && typeof source.data === 'string' && source.data.length > 40) {
      const mime =
        typeof source.media_type === 'string' && SAFE_MIME.test(source.media_type)
          ? source.media_type
          : 'image/png'
      const src = buildDataUri(mime, source.data)
      if (src) {
        put('source', {
          ...source,
          data: add(src, `${shortMime(mime)} · ${humanBytes(base64Bytes(source.data))}`, false),
        })
      }
    }

    // 3. Gemini parts: `inlineData` / `inline_data` with `mimeType` + `data`.
    for (const key of ['inlineData', 'inline_data']) {
      const part = asRecord(rec[key])
      if (!part || typeof part.data !== 'string' || part.data.length <= 40) continue
      if (HTTP_URL.test(part.data)) {
        put(key, { ...part, data: takeUrl(part.data, 'URL') })
        continue
      }
      let declared = ''
      if (typeof part.mimeType === 'string') declared = part.mimeType
      else if (typeof part.mime_type === 'string') declared = part.mime_type
      if (!SAFE_MIME.test(declared)) continue
      const src = buildDataUri(declared, part.data)
      if (!src) continue
      put(key, {
        ...part,
        data: add(
          src,
          `${shortMime(declared)} · ${humanBytes(base64Bytes(part.data))}`,
          false
        ),
      })
    }

    // 4. Vision input: `image_url` is a string, a data URI, or `{url}`.
    const imageUrl = rec.image_url
    if (typeof imageUrl === 'string') {
      const marker = takeUri(imageUrl) ?? (looksLikeRemoteImage(imageUrl) ? takeUrl(imageUrl, 'URL') : null)
      if (marker) put('image_url', marker)
    } else {
      const holder = asRecord(imageUrl)
      if (holder) {
        const inner = holder.url
        if (typeof inner === 'string') {
          const marker = takeUri(inner) ?? (HTTP_URL.test(inner) ? takeUrl(inner, 'URL') : null)
          if (marker) put('image_url', { ...holder, url: marker })
        }
      }
    }

    // 5. Recurse into everything not already settled above. A string that is
    // itself an image data URI is lifted here too — some clients put the whole
    // `data:` blob straight into a text field, where no key name gives it away.
    // The one place a child inherits "this is an image" is the images envelope:
    // `{created, data: [...]}`. A bare `data` array on its own is not enough
    // evidence, since plenty of unrelated APIs nest arbitrary rows under it.
    const inEnvelope =
      Array.isArray(rec.data) && ('created' in rec || 'usage' in rec)
    for (const [key, value] of Object.entries(rec)) {
      if (SKIP_KEYS.has(key)) continue
      const next =
        typeof value === 'string'
          ? (takeUri(value) ?? value)
          : visit(value, depth + 1, inImageResult || (inEnvelope && key === 'data'))
      if (next !== value) put(key, next)
    }

    return copy ?? rec
  }

  const visit = (
    node: unknown,
    depth: number,
    inImageResult: boolean
  ): unknown => {
    if (depth > MAX_DEPTH) return node
    if (Array.isArray(node)) {
      let copy: unknown[] | null = null
      for (let i = 0; i < node.length; i += 1) {
        const next = visit(node[i], depth + 1, inImageResult)
        if (next !== node[i]) {
          if (!copy) copy = [...node]
          copy[i] = next
        }
      }
      return copy ?? node
    }
    const rec = asRecord(node)
    return rec ? visitObject(rec, depth, inImageResult) : node
  }

  // A bare string body (not JSON) can still be a data URI.
  if (typeof root === 'string') {
    const marker = takeUri(root)
    return { items, view: marker ?? root }
  }

  return { items, view: visit(root, 0, false) }
}
