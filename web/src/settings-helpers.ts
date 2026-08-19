// logCategoryKeys mirrors the fixed, whitelisted categories internal/logging
// and internal/settings recognize on the backend - never send a key outside
// this list, since the server rejects unknown logging.categories fields.
export const logCategoryKeys = ['http', 'database', 'runtime', 'auth', 'filesystem', 'provisioning', 'steamcmd', 'templates', 'general'] as const;
export type LogCategoryKey = typeof logCategoryKeys[number];
export const logCategoryLabels: Record<LogCategoryKey, string> = { http: 'HTTP access', database: 'Database', runtime: 'Server runtime', auth: 'Authentication', filesystem: 'Filesystem', provisioning: 'Provisioning & updates', steamcmd: 'SteamCMD', templates: 'Templates', general: 'General' };
const defaultLogCategories = (): Record<LogCategoryKey, boolean> => Object.fromEntries(logCategoryKeys.map(key => [key, true])) as Record<LogCategoryKey, boolean>;

export type SettingsResponse = {
  monitoring: { sample_interval_seconds: number; history_limit: number };
  logging?: { level: 'debug' | 'info' | 'warn' | 'error'; categories?: Record<LogCategoryKey, boolean>; detailed_errors?: boolean };
  security: { password_minimum_length: number; password_maximum_length: number };
  branding: { name: string; subtitle: string; custom_favicon: boolean };
  runtime?: { container_image_allowlist: string[] };
  restart_required: boolean;
  restart_required_fields?: string[];
};

export type SettingsForm = { sampleInterval: string; historyLimit: string; logLevel: 'debug' | 'info' | 'warn' | 'error'; logCategories: Record<LogCategoryKey, boolean>; logDetailedErrors: boolean; passwordMinimumLength: string; passwordMaximumLength: string; brandingName: string; brandingSubtitle: string; containerImageAllowlist: string };

export const validSampleInterval = (value: string) => /^\d+$/.test(value) && Number(value) >= 1 && Number(value) <= 300;
export const validHistoryLimit = (value: string) => /^\d+$/.test(value) && Number(value) >= 1 && Number(value) <= 10000;
export const validPasswordLengths = (minimum: string, maximum: string) => /^\d+$/.test(minimum) && /^\d+$/.test(maximum) && Number(minimum) >= 8 && Number(minimum) <= 128 && Number(maximum) >= Number(minimum) && Number(maximum) <= 256;
export const validBranding = (name: string, subtitle: string) => [...name.trim()].length >= 1 && [...name.trim()].length <= 64 && [...subtitle.trim()].length <= 128;
export const settingsForm = (value: SettingsResponse): SettingsForm => ({ sampleInterval: String(value.monitoring.sample_interval_seconds), historyLimit: String(value.monitoring.history_limit), logLevel: value.logging?.level ?? 'info', logCategories: { ...defaultLogCategories(), ...value.logging?.categories }, logDetailedErrors: value.logging?.detailed_errors ?? false, passwordMinimumLength: String(value.security.password_minimum_length), passwordMaximumLength: String(value.security.password_maximum_length), brandingName: value.branding.name, brandingSubtitle: value.branding.subtitle, containerImageAllowlist: (value.runtime?.container_image_allowlist ?? ['docker.io', 'ghcr.io', 'quay.io']).join(', ') });
export const settingsPatch = (current: SettingsResponse, form: SettingsForm) => {
  const monitoring: Record<string, number> = {};
  if (Number(form.sampleInterval) !== current.monitoring.sample_interval_seconds) monitoring.sample_interval_seconds = Number(form.sampleInterval);
  if (Number(form.historyLimit) !== current.monitoring.history_limit) monitoring.history_limit = Number(form.historyLimit);
  const currentCategories = { ...defaultLogCategories(), ...current.logging?.categories };
  const categories: Partial<Record<LogCategoryKey, boolean>> = {};
  for (const key of logCategoryKeys) if (form.logCategories[key] !== currentCategories[key]) categories[key] = form.logCategories[key];
  const logging: Record<string, unknown> = {};
  if (form.logLevel && form.logLevel !== (current.logging?.level ?? 'info')) logging.level = form.logLevel;
  if (Object.keys(categories).length) logging.categories = categories;
  if (form.logDetailedErrors !== (current.logging?.detailed_errors ?? false)) logging.detailed_errors = form.logDetailedErrors;
  const security: Record<string, number> = {};
  if (Number(form.passwordMinimumLength) !== current.security.password_minimum_length) security.password_minimum_length = Number(form.passwordMinimumLength);
  if (Number(form.passwordMaximumLength) !== current.security.password_maximum_length) security.password_maximum_length = Number(form.passwordMaximumLength);
  const branding: Record<string, string> = {};
  if (form.brandingName.trim() !== current.branding.name) branding.name = form.brandingName;
  if (form.brandingSubtitle.trim() !== current.branding.subtitle) branding.subtitle = form.brandingSubtitle;
  const allowlist = form.containerImageAllowlist.split(',').map(value => value.trim().toLowerCase()).filter(Boolean);
  const currentAllowlist = current.runtime?.container_image_allowlist ?? ['docker.io', 'ghcr.io', 'quay.io'];
  const runtime = allowlist.join(',') !== currentAllowlist.join(',') ? { container_image_allowlist: allowlist } : undefined;
  return Object.keys(monitoring).length || Object.keys(logging).length || Object.keys(security).length || Object.keys(branding).length || runtime ? { ...(Object.keys(monitoring).length ? { monitoring } : {}), ...(Object.keys(logging).length ? { logging } : {}), ...(Object.keys(security).length ? { security } : {}), ...(Object.keys(branding).length ? { branding } : {}), ...(runtime ? { runtime } : {}) } : undefined;
};
