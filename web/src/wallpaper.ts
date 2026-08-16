// Browser-only wallpaper file processing. Not unit-testable under
// node:test (needs canvas/Image decoding), so keep this file small and
// defer all reusable/sanitization logic to theme.ts.
import { isValidWallpaperImage } from './theme';

const ACCEPTED_TYPES = new Set(['image/png', 'image/jpeg', 'image/webp']);
const MAX_SOURCE_BYTES = 8 * 1024 * 1024; // 8 MiB original upload, before re-encode
const MAX_DIMENSION = 1920;

/**
 * Validates and re-encodes a user-selected wallpaper image into a bounded
 * PNG/JPEG data URL stored only in this browser's localStorage.
 *
 * Security notes:
 * - SVG is never accepted (not in ACCEPTED_TYPES), so no inline SVG/script
 *   payload can reach the DOM as a wallpaper.
 * - The file is decoded with `createImageBitmap`, which rejects anything
 *   that isn't real raster image data regardless of its claimed MIME type
 *   or extension (defends against a mislabeled/polyglot upload).
 * - The decoded bitmap is redrawn onto a canvas and re-exported, which
 *   rasterizes the image and discards any non-pixel bytes (metadata,
 *   trailing/prepended payloads) before the result is stored or ever used
 *   in a CSS value.
 * - There is no server round-trip: nothing here touches auth, CSRF, or the
 *   backend. The processed value must still pass isValidWallpaperImage's
 *   strict allow-list before it is ever written to a CSS custom property.
 */
export async function processWallpaperFile(file: File): Promise<string> {
  if (!ACCEPTED_TYPES.has(file.type)) throw new Error('Only PNG, JPEG, or WebP images are supported.');
  if (file.size > MAX_SOURCE_BYTES) throw new Error('Image is too large (maximum 8 MiB).');
  let bitmap: ImageBitmap;
  try { bitmap = await createImageBitmap(file); }
  catch { throw new Error('The selected file is not a readable PNG, JPEG, or WebP image.'); }
  try {
    const scale = Math.min(1, MAX_DIMENSION / Math.max(bitmap.width, bitmap.height));
    const width = Math.max(1, Math.round(bitmap.width * scale));
    const height = Math.max(1, Math.round(bitmap.height * scale));
    const canvas = document.createElement('canvas');
    canvas.width = width; canvas.height = height;
    const ctx = canvas.getContext('2d');
    if (!ctx) throw new Error('Image processing is unavailable in this browser.');
    ctx.drawImage(bitmap, 0, 0, width, height);
    const mime = file.type === 'image/png' ? 'image/png' : 'image/jpeg';
    const dataURL = canvas.toDataURL(mime, mime === 'image/jpeg' ? 0.85 : undefined);
    if (!isValidWallpaperImage(dataURL)) throw new Error('The processed image is too large to store. Try a smaller or simpler image.');
    return dataURL;
  } finally {
    bitmap.close();
  }
}
