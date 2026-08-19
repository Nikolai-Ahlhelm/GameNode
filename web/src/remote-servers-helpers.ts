export type RemotePermission = 'RemoteServer.View' | 'RemoteServer.Manage' | 'RemoteConsole.View' | 'RemoteConsole.Send' | 'RemoteFiles.View' | 'RemoteFiles.Edit' | 'RemoteFiles.Upload' | 'RemoteFiles.Download' | 'RemoteFiles.Delete' | 'RemoteFiles.Rename' | 'RemoteMonitoring.View';

export function hasRemotePermission(permissions: readonly string[] | undefined, permission: RemotePermission): boolean {
  return permissions?.includes(permission) ?? false;
}

export function remoteServerStateLabel(state: string | undefined): string {
  switch (state) {
    case 'running': return 'Running';
    case 'starting': return 'Starting';
    case 'stopping': return 'Stopping';
    case 'stopped': return 'Stopped';
    case 'crashed': return 'Crashed';
    default: return state ? state.replaceAll('_', ' ') : 'Unknown';
  }
}

export function remoteServerStateTone(state: string | undefined): 'running' | 'starting' | 'stopping' | 'stopped' | 'crashed' | 'unknown' {
  switch (state) {
    case 'running': return 'running';
    case 'starting': return 'starting';
    case 'stopping': return 'stopping';
    case 'stopped': return 'stopped';
    case 'crashed': return 'crashed';
    default: return 'unknown';
  }
}

export function remoteBytes(value: number | undefined): string {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < 0) return 'Unavailable';
  if (value >= 1024 * 1024 * 1024) return `${(value / (1024 * 1024 * 1024)).toFixed(1)} GiB`;
  if (value >= 1024 * 1024) return `${(value / (1024 * 1024)).toFixed(1)} MiB`;
  if (value >= 1024) return `${(value / 1024).toFixed(1)} KiB`;
  return `${value} B`;
}

export function remotePath(path: string): string {
  return path.trim().replaceAll('\\', '/').replace(/^\/+/, '');
}
