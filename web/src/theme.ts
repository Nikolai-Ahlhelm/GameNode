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
  /** Base neutral color each theme's page/sidebar/surface/border ladder is
   * derived from (see styles.css color-mix rules). Kept separate per theme
   * so switching dark/light doesn't require re-picking a color. */
  baseColorDark: string;
  baseColorLight: string;
};

export const defaultWallpaper: WallpaperPreference = { enabled: false, image: null, blur: 12, dim: 55 };
// Match the previous hardcoded --page values, so an unmodified install looks
// identical to before this became configurable.
export const defaultBaseColorDark = '#08111f';
export const defaultBaseColorLight = '#eef1f6';
export const defaultPreferences: UIPreferences = { theme: 'system', sidebarCollapsed: false, wallpaper: defaultWallpaper, baseColorDark: defaultBaseColorDark, baseColorLight: defaultBaseColorLight };

const HEX_COLOR = /^#[0-9a-fA-F]{6}$/;

/** Strict allow-list check: 6-digit `#rrggbb` hex only, so it is also safe to interpolate into a CSS custom property without further escaping. */
export const isValidHexColor = (value: unknown): value is string => typeof value === 'string' && HEX_COLOR.test(value);

export const sanitizeBaseColor = (value: unknown, fallback: string): string => isValidHexColor(value) ? value : fallback;

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
  return {
    theme,
    sidebarCollapsed: raw.sidebarCollapsed === true,
    wallpaper: sanitizeWallpaper(raw.wallpaper),
    baseColorDark: sanitizeBaseColor(raw.baseColorDark, defaultBaseColorDark),
    baseColorLight: sanitizeBaseColor(raw.baseColorLight, defaultBaseColorLight),
  };
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

export function applyBaseColors(baseColorDark: string, baseColorLight: string): void {
  if (typeof document === 'undefined') return;
  const root = document.documentElement;
  root.style.setProperty('--base-dark', sanitizeBaseColor(baseColorDark, defaultBaseColorDark));
  root.style.setProperty('--base-light', sanitizeBaseColor(baseColorLight, defaultBaseColorLight));
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
