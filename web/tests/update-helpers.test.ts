import assert from 'node:assert/strict';
import test from 'node:test';
import {
  bannerVisible,
  blockingChecks,
  canInstall,
  checkLabel,
  checkTone,
  formatBytes,
  isBusy,
  outcomeText,
  progressPercent,
  publishedLabel,
  readDismissedVersion,
  requestSettingsTab,
  restartSettled,
  shortHash,
  shouldPoll,
  stateLabel,
  statusTone,
  takeRequestedSettingsTab,
  updateErrorFromResponse,
  updatePermissions,
  warningChecks,
  writeDismissedVersion,
  type UpdateCheck,
  type UpdateStatus,
} from '../src/update-helpers.ts';

function status(overrides: Partial<UpdateStatus> = {}): UpdateStatus {
  return {
    current_version: 'v1.0.0', os: 'linux', arch: 'amd64', updatable: true, state: 'idle', update_available: true,
    available: { tag: 'v1.1.0', version: '1.1.0', url: 'https://github.com/x/y/releases/tag/v1.1.0', prerelease: false },
    checks: [], can_prepare: true, can_apply: false, requires_acknowledgement: false, restart_mode: 'self', min_check_interval_seconds: 15,
    ...overrides,
  };
}
const check = (id: string, state: UpdateCheck['status']): UpdateCheck => ({ id, label: id, status: state, message: id });

test('Update.View and Update.Manage are independent permissions', () => {
  assert.deepEqual(updatePermissions(undefined), { view: false, manage: false });
  assert.deepEqual(updatePermissions(['Update.Manage']), { view: false, manage: true });
  assert.deepEqual(updatePermissions(['Update.View']), { view: true, manage: false });
  assert.deepEqual(updatePermissions(['Update.View', 'Update.Manage', 'Settings.Manage']), { view: true, manage: true });
});

test('check status maps to the shared status pill tones and labels', () => {
  assert.equal(checkTone('pass'), 'running');
  assert.equal(checkTone('warn'), 'degraded');
  assert.equal(checkTone('block'), 'crashed');
  assert.deepEqual(['pass', 'warn', 'block'].map(s => checkLabel(s as UpdateCheck['status'])), ['Passed', 'Warning', 'Blocked']);
});

test('separates blocking checks from warnings', () => {
  const checks = [check('a', 'pass'), check('b', 'warn'), check('c', 'block'), check('d', 'block')];
  assert.deepEqual(blockingChecks(checks).map(c => c.id), ['c', 'd']);
  assert.deepEqual(warningChecks(checks).map(c => c.id), ['b']);
});

test('polling and busy states', () => {
  for (const state of ['downloading', 'applying', 'restarting'] as const) assert.ok(shouldPoll(state), state);
  for (const state of ['idle', 'ready', 'failed', 'checking', undefined] as const) assert.equal(shouldPoll(state), false, String(state));
  assert.ok(isBusy('checking'));
  assert.equal(isBusy('ready'), false);
});

test('download progress is bounded and honest about unknown totals', () => {
  assert.equal(progressPercent(undefined), undefined);
  assert.equal(progressPercent({ downloaded_bytes: 5, total_bytes: 0 }), undefined);
  assert.equal(progressPercent({ downloaded_bytes: 0, total_bytes: 200 }), 0);
  assert.equal(progressPercent({ downloaded_bytes: 50, total_bytes: 200 }), 25);
  assert.equal(progressPercent({ downloaded_bytes: 999, total_bytes: 200 }), 100);
});

test('formats byte sizes and hashes', () => {
  assert.equal(formatBytes(512), '512 B');
  assert.equal(formatBytes(2048), '2.0 KiB');
  assert.equal(formatBytes(36 * 1024 * 1024), '36.0 MiB');
  assert.equal(formatBytes(-1), '—');
  assert.equal(formatBytes(Number.NaN), '—');
  assert.equal(shortHash('0123456789abcdef0123'), '0123456789ab');
  assert.equal(shortHash(undefined), '');
});

test('install requires a ready, passing update and every warning acknowledged', () => {
  assert.equal(canInstall(undefined, true), false);
  assert.equal(canInstall(status({ state: 'idle', can_apply: true }), true), false, 'not staged yet');
  assert.equal(canInstall(status({ state: 'ready', can_apply: false }), true), false, 'blocked by a safety check');
  assert.equal(canInstall(status({ state: 'ready', can_apply: true }), false), true);
  const warned = status({ state: 'ready', can_apply: true, requires_acknowledgement: true });
  assert.equal(canInstall(warned, false), false, 'warnings unacknowledged');
  assert.equal(canInstall(warned, true), true);
});

test('a restart is settled only by a new process answering', () => {
  assert.equal(restartSettled('1.0.0', undefined, true), false, 'no answer yet');
  assert.equal(restartSettled('1.0.0', '1.0.0', false), false, 'the old process still answering is not the new one');
  assert.equal(restartSettled('1.0.0', '1.1.0', false), true, 'a new version is proof');
  assert.equal(restartSettled('1.0.0', '1.0.0', true), true, 'went down and came back (e.g. rolled back)');
});

test('the banner shows only for an actionable, undismissed release', () => {
  assert.equal(bannerVisible(undefined, undefined), false);
  assert.equal(bannerVisible(status({ update_available: false }), undefined), false);
  assert.equal(bannerVisible(status({ available: undefined }), undefined), false);
  assert.equal(bannerVisible(status(), undefined), true);
  assert.equal(bannerVisible(status(), '1.1.0'), false, 'dismissed for exactly this release');
  assert.equal(bannerVisible(status(), '1.0.9'), true, 'a newer release re-prompts');
  assert.equal(bannerVisible(status({ state: 'restarting' }), undefined), false);
  assert.equal(bannerVisible(status({ state: 'applying' }), undefined), false);
});

test('overall status tone is green only when genuinely current', () => {
  assert.equal(statusTone(status({ update_available: false })), 'stopped');
  assert.equal(statusTone(status({ update_available: false, last_checked_at: '2026-01-01T00:00:00Z' })), 'running');
  assert.equal(statusTone(status()), 'degraded');
  assert.equal(statusTone(status({ state: 'failed' })), 'crashed');
  assert.equal(statusTone(status({ update_available: false, last_checked_at: 'x', last_check_error: { code: 'c', message: 'm' } })), 'crashed');
});

test('labels for state, outcome, and publish date', () => {
  assert.equal(stateLabel(undefined), 'Unknown');
  assert.equal(stateLabel(status()), 'Update available');
  assert.equal(stateLabel(status({ update_available: false })), 'Not checked yet', 'never claim up to date before a check');
  assert.equal(stateLabel(status({ update_available: false, last_checked_at: '2026-01-01T00:00:00Z' })), 'Up to date');
  assert.equal(stateLabel(status({ update_available: false, last_check_error: { code: 'source_unavailable', message: 'x' } })), 'Check failed');
  assert.equal(stateLabel(status({ state: 'ready' })), 'Verified and ready to install');
  assert.equal(outcomeText(undefined), undefined);
  assert.equal(outcomeText({ result: 'completed', from: 'v1', to: '2', at: '' }), 'Updated from v1 to 2.');
  assert.match(outcomeText({ result: 'rolled_back', from: 'v1', to: '2', at: '' }) ?? '', /restored automatically/);
  assert.equal(publishedLabel(undefined), 'Unknown');
  assert.equal(publishedLabel('0001-01-01T00:00:00Z'), 'Unknown');
  assert.equal(publishedLabel('not a date'), 'Unknown');
  assert.notEqual(publishedLabel('2026-03-04T05:06:07Z'), 'Unknown');
});

test('error bodies keep the controlled message, code, and checks', () => {
  const error = updateErrorFromResponse(409, { error: { code: 'preflight_blocked', message: 'blocked' }, checks: [check('active_jobs', 'block')] });
  assert.equal(error.message, 'blocked');
  assert.equal(error.code, 'preflight_blocked');
  assert.deepEqual(error.checks.map(c => c.id), ['active_jobs']);
  assert.equal(updateErrorFromResponse(403, null).message, 'You do not have permission to perform this action.');
  assert.equal(updateErrorFromResponse(500, 'oops').message, 'Request failed.');
  assert.deepEqual(updateErrorFromResponse(409, { checks: 'nope' }).checks, []);
});

test('browser-local conveniences survive unavailable or blocked storage', () => {
  const store = new Map<string, string>();
  const storage = { getItem: (k: string) => store.get(k) ?? null, setItem: (k: string, v: string) => void store.set(k, v), removeItem: (k: string) => void store.delete(k) };
  (globalThis as unknown as { window: unknown }).window = { localStorage: storage, sessionStorage: storage };
  assert.equal(readDismissedVersion(), undefined);
  writeDismissedVersion('1.1.0');
  assert.equal(readDismissedVersion(), '1.1.0');
  requestSettingsTab('updates');
  assert.equal(takeRequestedSettingsTab(), 'updates');
  assert.equal(takeRequestedSettingsTab(), undefined, 'a tab request is one-shot');

  const blocked = { getItem() { throw new Error('blocked'); }, setItem() { throw new Error('blocked'); }, removeItem() { throw new Error('blocked'); } };
  (globalThis as unknown as { window: unknown }).window = { localStorage: blocked, sessionStorage: blocked };
  assert.equal(readDismissedVersion(), undefined);
  assert.doesNotThrow(() => writeDismissedVersion('1.2.0'));
  assert.doesNotThrow(() => requestSettingsTab('updates'));
  assert.equal(takeRequestedSettingsTab(), undefined);
});
