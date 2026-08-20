import { useCallback, useEffect, useState } from 'react';
import { Clipboard, RefreshCw } from 'lucide-react';
import { SectionHeader } from './ui';
import './ftp.css';

type FTPProfile = {
  server_id: string;
  username: string;
  enabled: boolean;
  configured: boolean;
  service_enabled: boolean;
  listen_address: string;
  public_host?: string;
  tls: boolean;
  tls_required: boolean;
  updated_at: string;
};

type FTPCredential = FTPProfile & { password: string };

async function ftpAPI<T>(path: string, token: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`/api/v1${path}`, {
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json', ...(init?.method && init.method !== 'GET' ? { 'X-CSRF-Token': token } : {}), ...(init?.headers ?? {}) },
    ...init,
  });
  if (!response.ok) {
    const body = await response.json().catch(() => null);
    throw new Error(body?.error?.message ?? 'FTP request failed');
  }
  return response.json();
}

export function FTPPanel({ serverID, token, canManage }: { serverID: string; token: string; canManage: boolean }) {
  const [profile, setProfile] = useState<FTPProfile>();
  const [credential, setCredential] = useState<FTPCredential>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    try {
      setProfile(await ftpAPI<FTPProfile>(`/servers/${serverID}/ftp`, token));
      setError('');
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'FTP status could not be loaded');
    }
  }, [serverID, token]);

  useEffect(() => { void load(); }, [load]);

  async function rotate() {
    if (profile?.configured && !confirm('Rotate this FTP password? The current password will stop working immediately.')) return;
    setBusy(true); setError('');
    try {
      const next = await ftpAPI<FTPCredential>(`/servers/${serverID}/ftp/credentials`, token, { method: 'POST', body: '{}' });
      setCredential(next); setProfile(next);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'FTP credentials could not be generated');
    } finally { setBusy(false); }
  }

  async function toggle() {
    if (!profile) return;
    setBusy(true); setError('');
    try {
      const next = await ftpAPI<FTPProfile>(`/servers/${serverID}/ftp`, token, { method: 'PATCH', body: JSON.stringify({ enabled: !profile.enabled }) });
      setProfile(next); setCredential(undefined);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'FTP access could not be changed');
    } finally { setBusy(false); }
  }

  async function copy(value: string) {
    try { await navigator.clipboard.writeText(value); } catch { setError('Clipboard access is unavailable. Copy the value manually.'); }
  }

  if (!profile) return <section className="subpanel"><SectionHeader title="FTP access" description="Loading connection status…" />{error && <p className="error notice">{error}</p>}</section>;
  const protocol = profile.tls ? 'FTPS (explicit TLS)' : 'FTP';
  return <div className="ftp-panel">
    <section className="subpanel">
      <SectionHeader title="FTP access" description="A dedicated credential confined to this server's working directory." actions={canManage ? <div className="actions"><button className="quiet" disabled={busy} onClick={() => void rotate()}><RefreshCw />{profile.configured ? 'Rotate password' : 'Generate credentials'}</button>{profile.configured && <button disabled={busy} className={profile.enabled ? 'danger quiet' : ''} onClick={() => void toggle()}>{profile.enabled ? 'Disable access' : 'Enable access'}</button>}</div> : undefined} />
      {!profile.service_enabled && <div className="notice notice--warning"><strong>FTP service is disabled globally.</strong><br />Enable the <code>ftp</code> listener in <code>config.yaml</code> and restart GameNode.</div>}
      {!profile.tls && profile.service_enabled && <div className="notice notice--warning"><strong>Connection is not encrypted.</strong><br />Configure FTP TLS before exposing this listener outside a trusted local network.</div>}
      <div className="definition-list">
        <div className="definition-row"><span>Status</span><strong><span className="status">{profile.enabled ? profile.service_enabled ? 'Enabled' : 'Waiting for service' : 'Disabled'}</span></strong></div>
        <div className="definition-row"><span>Protocol</span><strong>{protocol}{profile.tls_required ? ' · required' : ''}</strong></div>
        <div className="definition-row"><span>Address</span><code>{profile.public_host || profile.listen_address}</code></div>
        <div className="definition-row"><span>Username</span><code>{profile.username}</code></div>
        <div className="definition-row"><span>Password</span><strong>{profile.configured ? 'Configured · hidden' : 'Not generated'}</strong></div>
      </div>
    </section>
    {credential && <section className="subpanel ftp-credential"><SectionHeader title="Save this password now" description="GameNode stores only its hash. This plaintext value cannot be shown again." /><div><code>{credential.password}</code><button className="quiet" onClick={() => void copy(credential.password)}><Clipboard />Copy</button></div></section>}
    {error && <p className="error notice">{error}</p>}
  </div>;
}
