import assert from 'node:assert/strict';
import test from 'node:test';
import { clampBlur, clampDim, defaultPreferences, isValidWallpaperImage, MAX_WALLPAPER_DATA_URL_LENGTH, resolveTheme, sanitizePreferences, sanitizeWallpaper } from '../src/theme.ts';

test('resolveTheme follows explicit mode and falls back to system preference', () => {
  assert.equal(resolveTheme('dark', false), 'dark');
  assert.equal(resolveTheme('light', true), 'light');
  assert.equal(resolveTheme('system', true), 'dark');
  assert.equal(resolveTheme('system', false), 'light');
});

test('isValidWallpaperImage only accepts bounded PNG/JPEG/WebP data URLs', () => {
  assert.equal(isValidWallpaperImage('data:image/png;base64,AAAA'), true);
  assert.equal(isValidWallpaperImage('data:image/jpeg;base64,AAAA=='), true);
  assert.equal(isValidWallpaperImage('data:image/webp;base64,AAAA'), true);
  assert.equal(isValidWallpaperImage('data:image/svg+xml;base64,AAAA'), false);
  assert.equal(isValidWallpaperImage('https://example.com/wallpaper.png'), false);
  assert.equal(isValidWallpaperImage('data:image/png;base64,not base64!'), false);
  assert.equal(isValidWallpaperImage('data:text/html,<script>alert(1)</script>'), false);
  assert.equal(isValidWallpaperImage('data:image/png;base64,' + 'A'.repeat(MAX_WALLPAPER_DATA_URL_LENGTH + 1)), false);
  assert.equal(isValidWallpaperImage(undefined), false);
  assert.equal(isValidWallpaperImage(42), false);
});

test('sanitizeWallpaper clamps ranges and forces enabled=false without a valid image', () => {
  assert.deepEqual(sanitizeWallpaper(undefined), defaultPreferences.wallpaper);
  assert.deepEqual(sanitizeWallpaper({ enabled: true, image: 'javascript:alert(1)', blur: 5, dim: 10 }), { enabled: false, image: null, blur: 5, dim: 10 });
  assert.deepEqual(sanitizeWallpaper({ enabled: true, image: 'data:image/png;base64,AAAA', blur: 999, dim: -5 }), { enabled: true, image: 'data:image/png;base64,AAAA', blur: 20, dim: 0 });
  assert.equal(clampBlur(NaN), defaultPreferences.wallpaper.blur);
  assert.equal(clampDim(NaN), defaultPreferences.wallpaper.dim);
});

test('sanitizePreferences rejects unknown theme values and coerces booleans', () => {
  assert.deepEqual(sanitizePreferences(undefined), defaultPreferences);
  assert.deepEqual(sanitizePreferences({ theme: 'neon', sidebarCollapsed: 'yes' }), { theme: 'system', sidebarCollapsed: false, wallpaper: defaultPreferences.wallpaper });
  assert.deepEqual(sanitizePreferences({ theme: 'light', sidebarCollapsed: true, wallpaper: { enabled: true, image: 'data:image/webp;base64,AAAA', blur: 3, dim: 40 } }), { theme: 'light', sidebarCollapsed: true, wallpaper: { enabled: true, image: 'data:image/webp;base64,AAAA', blur: 3, dim: 40 } });
});
