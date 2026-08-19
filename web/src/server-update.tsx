import { useEffect, useState } from 'react';
import { CheckCircle2, CircleAlert, HardDriveDownload, LoaderCircle, XCircle } from 'lucide-react';
import { serverUpdateCancellable, serverUpdateStatusLabel, serverUpdateTerminal } from './server-updates-helpers';
// .provision-progress/.provision-output are shared with template-provision.tsx's
// job-progress panel; reuse the same stylesheet rather than duplicating rules.
import './templates.css';

export type ServerUpdateEligibility = { eligible: boolean; reason?: string; installer?: string; app_id?: number; validate: boolean; template_id?: string; template_version?: string; server_state?: string; active_job?: ServerUpdateJob };
export type ServerUpdateJob = { id: string; server_id: string; template_id: string; template_version: string; app_id: number; validate: boolean; status: string; current_phase: string; summary: string; error_summary?: string; created_at: string; updated_at: string; installer_output?: string[]; output_truncated?: boolean };

async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`/api/v1${path}`, { credentials: 'same-origin', headers: { 'Content-Type': 'application/json', ...(init?.headers ?? {}) }, ...init });
  if (!response.ok) { const body = await response.json().catch(() => null); throw new Error(body?.error?.message ?? 'Request failed'); }
  return response.status === 204 ? undefined as T : response.json();
}

/** ServerUpdateDialog is Server Detail's "Update Server" modal: it reviews
 * the trusted persisted SteamCMD provenance (never a freshly re-resolved
 * template), starts the update job, and polls its phase-based progress -
 * mirroring TemplateProvisionWizard's job-polling pattern in
 * template-provision.tsx, scoped to this much smaller flow. */
export function ServerUpdateDialog({ serverID, serverName, token, onClose, onCompleted }: { serverID: string; serverName: string; token: string; onClose: () => void; onCompleted: () => void }) {
  const [status, setStatus] = useState<ServerUpdateEligibility>();
  const [job, setJob] = useState<ServerUpdateJob>();
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  useEffect(() => { api<ServerUpdateEligibility>(`/servers/${serverID}/update`).then(setStatus).catch(reason => setError(reason instanceof Error ? reason.message : 'Update status could not be loaded')); }, [serverID]);
  useEffect(() => {
    if (!job || serverUpdateTerminal(job.status)) return;
    const timer = window.setInterval(() => api<ServerUpdateJob>(`/server-update-jobs/${job.id}`).then(setJob).catch(reason => setError(reason instanceof Error ? reason.message : 'Update status could not be refreshed')), 1000);
    return () => window.clearInterval(timer);
  }, [job?.id, job?.status]);

  async function start() {
    try {
      setBusy(true); setError('');
      setJob(await api<ServerUpdateJob>(`/servers/${serverID}/update`, { method: 'POST', headers: { 'X-CSRF-Token': token } }));
    } catch (reason) { setError(reason instanceof Error ? reason.message : 'Update could not start'); } finally { setBusy(false); }
  }
  async function cancel() {
    if (!job) return;
    try { setJob(await api<ServerUpdateJob>(`/server-update-jobs/${job.id}/cancel`, { method: 'POST', headers: { 'X-CSRF-Token': token } })); }
    catch (reason) { setError(reason instanceof Error ? reason.message : 'Cancellation failed'); }
  }
  function close() { if (job?.status === 'completed') onCompleted(); onClose(); }

  return <section className="panel inline-dialog" aria-label={`Update ${serverName}`}>
    <div className="row"><h2>Update {serverName}</h2><button className="quiet" onClick={close}>Close</button></div>
    {error && <p className="error notice">{error}</p>}
    {!status && !job && !error && <div className="provision-progress"><LoaderCircle className="spin" /><p>Checking update eligibility…</p></div>}
    {status && !job && (
      status.eligible
        ? <><dl>
            <div><dt>Steam App ID</dt><dd>{status.app_id}</dd></div>
            <div><dt>Template</dt><dd>{status.template_id} {status.template_version}</dd></div>
            <div><dt>Validate files</dt><dd>{status.validate ? 'Yes' : 'No'}</dd></div>
            <div><dt>Current state</dt><dd>{status.server_state}</dd></div>
          </dl>
          <p className="notice">This updates the installed Steam depot in place. Your GameNode server definition and pinned template configuration will not be migrated.</p>
          <div className="actions"><button className="quiet" onClick={onClose}>Cancel</button><button onClick={start} disabled={busy}><HardDriveDownload />{busy ? 'Starting…' : 'Start Update'}</button></div>
        </>
        : <><div className="notice notice--warning"><CircleAlert />{status.reason ?? 'This server cannot be updated right now.'}</div><div className="actions"><button className="quiet" onClick={onClose}>Close</button></div></>
    )}
    {job && <>
      <div className={`provision-progress provision-progress--${job.status}`}>
        {job.status === 'completed' ? <CheckCircle2 /> : job.status === 'failed' || job.status === 'cancelled' ? <XCircle /> : <LoaderCircle className="spin" />}
        <p className="eyebrow">{serverUpdateStatusLabel(job.status)}</p>
        <h3>{job.summary}</h3>
        {job.error_summary && <p className="error">{job.error_summary}</p>}
      </div>
      <section className="provision-output">
        <header><strong>SteamCMD output</strong><small>Installer output is kept only in memory and is cleared when GameNode restarts. This gives an approximate sense of progress; GameNode does not fabricate a percentage from it.</small></header>
        <pre>{(job.installer_output ?? []).length ? job.installer_output!.join('\n') : 'Waiting for SteamCMD output…'}</pre>
        {job.output_truncated && <p className="notice notice--warning">Output limit reached; earlier installer output remains visible.</p>}
      </section>
      <div className="actions">
        {serverUpdateCancellable(job.status) && <button className="danger quiet" onClick={cancel}>Cancel update</button>}
        {serverUpdateTerminal(job.status) && <button onClick={close}>{job.status === 'completed' ? 'Done' : 'Close'}</button>}
      </div>
    </>}
  </section>;
}
