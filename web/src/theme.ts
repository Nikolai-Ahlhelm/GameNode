// Theme / UI preference model. This is a small, local (browser-only)
// preference layer, not a backend-persisted per-user setting: GameNode's
// existing Settings model is instance-wide (see settings-helpers.ts) and is
// not where a personal appearance choice belongs. Storing theme, sidebar,
// and wallpaper choices in localStorage keeps them clearly separate from
// authoritative instance configuration while still surviving reloads.
//
// Everything here is pure/testable. DOM application (data-theme attribute,
// CSS custom properties) lives in applyTheme/applyWallpaper, which are no-ops
// outside a browser. index.html carries a small inline copy of the resolve +
// wallpaper-validation logic so the correct theme/wallpaper paints on first
// frame, before this module loads; keep the two in sync when changing rules.

export type ThemeMode = 'dark' | 'light' | 'system';
export type ResolvedTheme = 'dark' | 'light';

export type WallpaperPreference = {
  enabled: boolean;
  /** A validated `data:image/(png|jpeg|webp);base64,...` URL, or null. Never a remote URL, never SVG, never inline HTML/CSS. */
  image: string | null;
  blur: number; // px, 0-20
  dim: number; // percent, 0-90
};

export type UIPreferences = {
  theme: ThemeMode;
  sidebarCollapsed: boolean;
  wallpaper: WallpaperPreference;
};

export const defaultWallpaper: WallpaperPreference = { enabled: false, image: null, blur: 12, dim: 55 };
export const defaultPreferences: UIPreferences = { theme: 'system', sidebarCollapsed: false, wallpaper: defaultWallpaper };

export const STORAGE_KEY = 'gamenode:ui-preferences';

// ~2.8M base64 characters is roughly 2MB of decoded image bytes: comfortably
// inside typical localStorage quotas (5-10MiB/origin) alongside other keys,
// while still allowing a reasonably sized processed wallpaper.
export const MAX_WALLPAPER_DATA_URL_LENGTH = 2_800_000;
const WALLPAPER_DATA_URL = /^data:image\/(png|jpeg|webp);base64,[A-Za-z0-9+/]+={0,2}$/;

export const clampBlur = (value: number): number => Number.isFinite(value) ? Math.min(20, Math.max(0, Math.round(value))) : defaultWallpaper.blur;
export const clampDim = (value: number): number => Number.isFinite(value) ? Math.min(90, Math.max(0, Math.round(value))) : defaultWallpaper.dim;

/** Strict allow-list check: PNG/JPEG/WebP data URL only, bounded length, base64 charset only (so it is also safe to interpolate into a CSS custom property without further escaping). */
export const isValidWallpaperImage = (value: unknown): value is string =>
  typeof value === 'string' && value.length > 0 && value.length <= MAX_WALLPAPER_DATA_URL_LENGTH && WALLPAPER_DATA_URL.test(value);

export function sanitizeWallpaper(value: unknown): WallpaperPreference {
  const raw = value && typeof value === 'object' ? value as Record<string, unknown> : {};
  const image = isValidWallpaperImage(raw.image) ? raw.image : null;
  return {
    enabled: raw.enabled === true && image !== null,
    image,
    blur: clampBlur(Number(raw.blur ?? defaultWallpaper.blur)),
    dim: clampDim(Number(raw.dim ?? defaultWallpaper.dim)),
  };
}

export function sanitizePreferences(value: unknown): UIPreferences {
  const raw = value && typeof value === 'object' ? value as Record<string, unknown> : {};
  const theme: ThemeMode = raw.theme === 'dark' || raw.theme === 'light' || raw.theme === 'system' ? raw.theme : defaultPreferences.theme;
  return { theme, sidebarCollapsed: raw.sidebarCollapsed === true, wallpaper: sanitizeWallpaper(raw.wallpaper) };
}

export function loadPreferences(): UIPreferences {
  if (typeof window === 'undefined') return defaultPreferences;
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    return raw ? sanitizePreferences(JSON.parse(raw)) : defaultPreferences;
  } catch {
    return defaultPreferences;
  }
}

export function savePreferences(preferences: UIPreferences): UIPreferences {
  const sanitized = sanitizePreferences(preferences);
  if (typeof window !== 'undefined') {
    try { window.localStorage.setItem(STORAGE_KEY, JSON.stringify(sanitized)); } catch { /* storage unavailable/full: preference stays session-only */ }
  }
  return sanitized;
}

export function systemPrefersDark(): boolean {
  return typeof window !== 'undefined' && typeof window.matchMedia === 'function' && window.matchMedia('(prefers-color-scheme: dark)').matches;
}

export function resolveTheme(mode: ThemeMode, prefersDark: boolean = systemPrefersDark()): ResolvedTheme {
  return mode === 'system' ? (prefersDark ? 'dark' : 'light') : mode;
}

export function applyTheme(resolved: ResolvedTheme): void {
  if (typeof document === 'undefined') return;
  document.documentElement.dataset.theme = resolved;
  document.documentElement.style.colorScheme = resolved;
}

export function applyWallpaper(wallpaper: WallpaperPreference): void {
  if (typeof document === 'undefined') return;
  const root = document.documentElement;
  if (wallpaper.enabled && isValidWallpaperImage(wallpaper.image)) {
    root.style.setProperty('--wallpaper-image', `url("${wallpaper.image}")`);
    root.style.setProperty('--wallpaper-blur', `${clampBlur(wallpaper.blur)}px`);
    root.style.setProperty('--wallpaper-dim', `${clampDim(wallpaper.dim) / 100}`);
    root.dataset.wallpaper = 'on';
  } else {
    root.style.removeProperty('--wallpaper-image');
    root.dataset.wallpaper = 'off';
  }
}

export function applySidebarCollapsed(collapsed: boolean): void {
  if (typeof document === 'undefined') return;
  document.documentElement.dataset.sidebar = collapsed ? 'collapsed' : 'expanded';
}
