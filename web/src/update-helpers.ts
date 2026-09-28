// Types and pure helpers for GameNode self-update (docs/adr/0013-self-update.md).
// Everything here mirrors internal/selfupdate's API contract; none of it is a
// security boundary - the backend re-runs every safety check on install.

export type UpdateCheckStatus = 'pass' | 'warn' | 'block';
export type UpdateCheck = { id: string; label: string; status: UpdateCheckStatus; message: string };
export type UpdateState = 'idle' | 'checking' | 'downloading' | 'ready' | 'applying' | 'restarting' | 'failed';
export type UpdateRelease = { tag: string; version: string; name?: string; published_at?: string; notes?: string; url: string; prerelease: boolean };
export type UpdateOutcome = { result: 'completed' | 'rolled_back'; from: string; to: string; at: string; reason?: string };
export type UpdateErrorInfo = { code: string; message: string };
export type UpdateStatus = {
  current_version: string;
  os: string;
  arch: string;
  updatable: boolean;
  updatable_reason?: string;
  state: UpdateState;
  progress?: { downloaded_bytes: number; total_bytes: number };
  update_available: boolean;
  available?: UpdateRelease;
  last_checked_at?: string;
  last_check_error?: UpdateErrorInfo;
  staged?: { version: string; sha256: string; verified_at: string };
  error?: UpdateErrorInfo;
  checks: UpdateCheck[];
  can_prepare: boolean;
  can_apply: boolean;
  requires_acknowledgement: boolean;
  restart_mode: 'self' | 'exit';
  last_outcome?: UpdateOutcome;
  min_check_interval_seconds: number;
};
/** The controller-side wrapper for a remote node's update status. */
export type RemoteUpdateView = { supported: boolean; update?: UpdateStatus };

/** Update.View and Update.Manage are independent: managing does not imply viewing. */
export function updatePermissions(capabilities: readonly string[] | undefined): { view: boolean; manage: boolean } {
  return { view: capabilities?.includes('Update.View') ?? false, manage: capabilities?.includes('Update.Manage') ?? false };
}

/** Pill tone for the overall update status: green only when genuinely current. */
export function statusTone(status: UpdateStatus): 'running' | 'degraded' | 'crashed' | 'stopped' {
  if (status.state === 'failed' || status.last_check_error) return 'crashed';
  if (status.update_available || status.state === 'ready' || status.state === 'downloading') return 'degraded';
  return status.last_checked_at ? 'running' : 'stopped';
}

export const checkTone = (status: UpdateCheckStatus): 'running' | 'degraded' | 'crashed' => status === 'pass' ? 'running' : status === 'warn' ? 'degraded' : 'crashed';
export const checkLabel = (status: UpdateCheckStatus): string => status === 'pass' ? 'Passed' : status === 'warn' ? 'Warning' : 'Blocked';

/** States in which the backend is mid-operation and the UI should keep polling. */
export const shouldPoll = (state: UpdateState | undefined): boolean => state === 'downloading' || state === 'applying' || state === 'restarting';
export const isBusy = (state: UpdateState | undefined): boolean => shouldPoll(state) || state === 'checking';

export const blockingChecks = (checks: readonly UpdateCheck[]): UpdateCheck[] => checks.filter(check => check.status === 'block');
export const warningChecks = (checks: readonly UpdateCheck[]): UpdateCheck[] => checks.filter(check => check.status === 'warn');

/** Whole-number download progress, or undefined while the total is unknown. */
export function progressPercent(progress: UpdateStatus['progress']): number | undefined {
  if (!progress || progress.total_bytes <= 0) return undefined;
  return Math.max(0, Math.min(100, Math.round((progress.downloaded_bytes / progress.total_bytes) * 100)));
}

export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return '—';
  if (bytes < 1024) return `${bytes} B`;
  const units = ['KiB', 'MiB', 'GiB'];
  let value = bytes / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) { value /= 1024; unit++; }
  return `${value.toFixed(value >= 100 ? 0 : 1)} ${units[unit]}`;
}

export const shortHash = (sha256: string | undefined): string => (sha256 ?? '').slice(0, 12);

/** Install needs a passing safety review, and every warning acknowledged. */
export const canInstall = (status: UpdateStatus | undefined, acknowledged: boolean): boolean =>
  !!status && status.can_apply && status.state === 'ready' && (!status.requires_acknowledgement || acknowledged);

/**
 * restartSettled decides when to stop waiting for a restarting node/instance.
 * A restart is over when the process answers again AND either it reports a
 * different version (update installed) or we observed it go down first (so a
 * rollback to the same version still counts as settled). A response that
 * arrives before the process ever went down is the old process, not the new one.
 */
export function restartSettled(before: string, current: string | undefined, sawDown: boolean): boolean {
  if (!current) return false;
  return current !== before || sawDown;
}

/** The dashboard banner appears only for an actionable, not-yet-dismissed release. */
export function bannerVisible(status: UpdateStatus | undefined, dismissedVersion: string | undefined): boolean {
  if (!status || !status.update_available || !status.available) return false;
  if (status.state === 'applying' || status.state === 'restarting') return false;
  return dismissedVersion !== status.available.version;
}

export function publishedLabel(value: string | undefined): string {
  if (!value) return 'Unknown';
  const date = new Date(value);
  if (Number.isNaN(date.getTime()) || date.getFullYear() < 2000) return 'Unknown';
  return date.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' });
}

export function stateLabel(status: UpdateStatus | undefined): string {
  if (!status) return 'Unknown';
  switch (status.state) {
    case 'checking': return 'Checking for updates';
    case 'downloading': return 'Downloading';
    case 'ready': return 'Verified and ready to install';
    case 'applying': return 'Installing';
    case 'restarting': return 'Restarting';
    case 'failed': return 'Last attempt failed';
    default:
      if (status.update_available) return 'Update available';
      if (status.last_check_error) return 'Check failed';
      return status.last_checked_at ? 'Up to date' : 'Not checked yet';
  }
}

export function outcomeText(outcome: UpdateOutcome | undefined): string | undefined {
  if (!outcome) return undefined;
  const from = outcome.from || 'the previous version', to = outcome.to || 'the new version';
  return outcome.result === 'completed'
    ? `Updated from ${from} to ${to}.`
    : `The update to ${to} did not become healthy and ${from} was restored automatically.`;
}

// The dismissal of a banner for one specific release and a one-shot request to
// open the Updates tab are per-browser conveniences only. Storage can be
// unavailable (private mode, blocked site data), so every access is guarded.
const dismissedKey = 'gamenode:update-dismissed';
const settingsTabKey = 'gamenode:settings-tab';

export function readDismissedVersion(): string | undefined {
  try { return window.localStorage.getItem(dismissedKey) ?? undefined; } catch { return undefined; }
}
export function writeDismissedVersion(version: string): void {
  try { window.localStorage.setItem(dismissedKey, version); } catch { /* not persisted; the banner simply reappears */ }
}
export function requestSettingsTab(tab: string): void {
  try { window.sessionStorage.setItem(settingsTabKey, tab); } catch { /* the settings page opens on its default tab */ }
}
export function takeRequestedSettingsTab(): string | undefined {
  try {
    const value = window.sessionStorage.getItem(settingsTabKey) ?? undefined;
    if (value) window.sessionStorage.removeItem(settingsTabKey);
    return value;
  } catch { return undefined; }
}

/** An error that may carry the safety checks the backend refused on. */
export class UpdateRequestError extends Error {
  code: string;
  checks: UpdateCheck[];
  constructor(message: string, code = '', checks: UpdateCheck[] = []) { super(message); this.code = code; this.checks = checks; }
}

export function updateErrorFromResponse(status: number, body: unknown): UpdateRequestError {
  const record = (body && typeof body === 'object' ? body : {}) as { error?: { code?: string; message?: string }; checks?: UpdateCheck[] };
  const fallback = status === 403 ? 'You do not have permission to perform this action.' : 'Request failed.';
  return new UpdateRequestError(record.error?.message ?? fallback, record.error?.code ?? '', Array.isArray(record.checks) ? record.checks : []);
}
