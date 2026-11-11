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
 * A chat attachment travels inside the message payload, not through an upload
 * endpoint, so it has to be small. 768 px on the long side keeps the bytes a
 * vision model actually needs and lands around 50–90 kB per picture — the same
 * copy is also what the bubble renders, so there is exactly one copy in memory.
 */
export async function compressImage(
  file: File,
  maxSide = 768,
  quality = 0.82
): Promise<string> {
  const bitmap = await createImageBitmap(file)
  try {
    const scale = Math.min(1, maxSide / Math.max(bitmap.width, bitmap.height))
    const width = Math.max(1, Math.round(bitmap.width * scale))
    const height = Math.max(1, Math.round(bitmap.height * scale))
    const canvas = document.createElement('canvas')
    canvas.width = width
    canvas.height = height
    const context = canvas.getContext('2d')
    if (!context) throw new Error('canvas unavailable')
    context.drawImage(bitmap, 0, 0, width, height)
    return canvas.toDataURL('image/jpeg', quality)
  } finally {
    bitmap.close()
  }
}
