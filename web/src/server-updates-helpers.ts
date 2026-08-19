// Pure helpers for the v0.2.1 manual SteamCMD server update feature. Kept
// separate from server-update.tsx so they can be unit-tested without a DOM,
// mirroring templates-helpers.ts/server-status.ts's pattern.

export function serverUpdateStatusLabel(status: string): string {
  return ({
    pending: 'Queued',
    preparing: 'Preparing to update',
    downloading_steamcmd: 'Downloading SteamCMD',
    steamcmd_ready: 'SteamCMD ready',
    updating: 'Updating server files',
    steamcmd_completed: 'SteamCMD completed',
    validating_installation: 'Validating installation',
    completed: 'Complete',
    failed: 'Failed',
    cancelled: 'Cancelled',
  } as Record<string, string>)[status] ?? 'Updating';
}

export function serverUpdateTerminal(status: string): boolean {
  return status === 'completed' || status === 'failed' || status === 'cancelled';
}

export function serverUpdateCancellable(status: string): boolean {
  return !serverUpdateTerminal(status);
}
