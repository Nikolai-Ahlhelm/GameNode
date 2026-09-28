import { useCallback, useEffect, useRef, useState } from 'react';
import { CheckCircle2, Download, RefreshCw, Rocket, TriangleAlert, X } from 'lucide-react';
import { EmptyState, LoadingState, SectionHeader } from './ui';
import { navigate } from './router';
import { relativeTime } from './nodes-helpers';
import './updates.css';
import {
  bannerVisible, canInstall, checkLabel, checkTone, formatBytes, outcomeText, progressPercent, publishedLabel, readDismissedVersion, requestSettingsTab,
  restartSettled, shortHash, shouldPoll, stateLabel, statusTone, updateErrorFromResponse, UpdateRequestError, warningChecks, writeDismissedVersion,
  type RemoteUpdateView, type UpdateCheck, type UpdateStatus,
} from './update-helpers';

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`/api/v1${path}`, { credentials: 'same-origin', headers: { 'Content-Type': 'application/json', ...(init?.headers ?? {}) }, ...init });
  if (!response.ok) throw updateErrorFromResponse(response.status, await response.json().catch(() => null));
  return response.json();
}
const restartGiveUpMs = 3 * 60 * 1000;
const sentence = (text: string) => text.charAt(0).toUpperCase() + text.slice(1);
const errorMessage = (error: unknown) => error instanceof Error ? error.message : 'Request failed.';

/** One set of typed operations against either this instance or an enrolled node. */
export type UpdateAdapter = {
  load: () => Promise<RemoteUpdateView>;
  check: () => Promise<RemoteUpdateView>;
  prepare: (version: string) => Promise<RemoteUpdateView>;
  apply: (version: string, acknowledgeWarnings: boolean) => Promise<RemoteUpdateView>;
  cancel: () => Promise<RemoteUpdateView>;
};

export function localUpdateAdapter(token: string): UpdateAdapter {
  const headers = { 'X-CSRF-Token': token };
  const wrap = (update: UpdateStatus): RemoteUpdateView => ({ supported: true, update });
  const post = (action: string, body?: unknown) => request<UpdateStatus>(`/system/update/${action}`, { method: 'POST', headers, body: body === undefined ? undefined : JSON.stringify(body) }).then(wrap);
  return {
    load: () => request<UpdateStatus>('/system/update').then(wrap),
    check: () => post('check'),
    prepare: version => post('prepare', { version }),
    apply: (version, acknowledge) => post('apply', { version, acknowledge_warnings: acknowledge }),
    cancel: () => post('cancel', {}),
  };
}

export function remoteUpdateAdapter(token: string, nodeID: string): UpdateAdapter {
  const headers = { 'X-CSRF-Token': token };
  const base = `/remote-nodes/${encodeURIComponent(nodeID)}/update`;
  const post = (action: string, body?: unknown) => request<RemoteUpdateView>(`${base}/${action}`, { method: 'POST', headers, body: body === undefined ? undefined : JSON.stringify(body) });
  return {
    load: () => request<RemoteUpdateView>(base),
    check: () => post('check'),
    prepare: version => post('prepare', { version }),
    apply: (version, acknowledge) => post('apply', { version, acknowledge_warnings: acknowledge }),
    cancel: () => post('cancel', {}),
  };
}

function CheckList({ checks }: { checks: UpdateCheck[] }) {
  if (checks.length === 0) return null;
  return <ul className="update-checks" aria-label="Safety checks">{checks.map(check => <li key={check.id} className={`update-check update-check--${check.status}`}>
    <span className={`status ${checkTone(check.status)}`}>{checkLabel(check.status)}</span>
    <div><strong>{check.label}</strong><p>{check.message}</p></div>
  </li>)}</ul>;
}

/**
 * UpdateManager renders the whole update workflow - check, download and verify,
 * review the safety checks, install - for one GameNode installation. The same
 * component serves this instance and a remote node: only the adapter differs.
 * The UI only presents state; every safety check is enforced by the backend.
 */
export function UpdateManager({ adapter, canManage, subject, title, description, extra, onRestarted, reloadOnRestart }: {
  adapter: UpdateAdapter; canManage: boolean; subject: string; title: string; description: string;
  extra?: React.ReactNode; onRestarted?: () => void; reloadOnRestart?: boolean;
}) {
  const [view, setView] = useState<RemoteUpdateView>();
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [refused, setRefused] = useState<UpdateCheck[]>([]);
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState('');
  const [acknowledged, setAcknowledged] = useState(false);
  const [waiting, setWaiting] = useState<{ before: string; target: string; at: number } | undefined>();
  const sawDown = useRef(false);
  const status = view?.update;

  const load = useCallback(async (quiet = false) => {
    try {
      const next = await adapter.load();
      setView(next);
      if (!quiet) setError('');
      return next;
    } catch (reason) {
      if (!quiet) setError(errorMessage(reason));
      return undefined;
    } finally { setLoading(false); }
  }, [adapter]);
  useEffect(() => { void load(); }, [load]);

  // Poll while the backend is mid-operation, and through a restart, when
  // failing requests are expected and simply mean "not back yet".
  const polling = !!waiting || shouldPoll(status?.state);
  useEffect(() => {
    if (!polling) return;
    const timer = window.setInterval(async () => {
      if (!waiting) { void load(true); return; }
      const finish = (next: RemoteUpdateView | undefined, current: string | undefined) => {
        setWaiting(undefined); sawDown.current = false;
        if (next) setView(next);
        setNotice(current ? (current === waiting.target ? `${sentence(subject)} is now running ${current}.` : `${sentence(subject)} restarted and is running ${current}.`) : `${sentence(subject)} restarted.`);
        onRestarted?.();
        if (reloadOnRestart) window.setTimeout(() => window.location.reload(), 1500);
      };
      if (Date.now() - waiting.at > restartGiveUpMs) {
        setWaiting(undefined); sawDown.current = false;
        setError(`${sentence(subject)} did not come back within ${restartGiveUpMs / 60000} minutes. Check its application log; if the updated version failed to start, GameNode restores the previous version automatically after repeated failed starts.`);
        return;
      }
      try {
        const next = await adapter.load();
        const current = next.update?.current_version;
        if (restartSettled(waiting.before, current, sawDown.current)) finish(next, current);
      } catch (reason) {
        // An authentication error still means a process is answering (the
        // session may not survive the restart). Once the old process has been
        // seen to go away, that answer is the new one; any other failure just
        // means it is not back yet.
        if (sawDown.current && reason instanceof UpdateRequestError && reason.code === 'unauthenticated') finish(undefined, undefined);
        else sawDown.current = true;
      }
    }, 1500);
    return () => window.clearInterval(timer);
  }, [polling, waiting, adapter, load, subject, onRestarted, reloadOnRestart]);

  async function run(label: string, action: () => Promise<RemoteUpdateView>, success?: string) {
    setBusy(label); setError(''); setNotice(''); setRefused([]);
    try {
      const next = await action();
      setView(next);
      if (success) setNotice(success);
      return next;
    } catch (reason) {
      setError(errorMessage(reason));
      if (reason instanceof UpdateRequestError) setRefused(reason.checks);
      void load(true);
      return undefined;
    } finally { setBusy(''); }
  }

  const check = () => run('check', adapter.check);
  const prepare = () => status?.available && run('prepare', () => adapter.prepare(status.available!.version), undefined);
  const cancel = () => run('cancel', adapter.cancel);
  async function install() {
    if (!status?.available || !canInstall(status, acknowledged)) return;
    const version = status.available.version;
    const warnings = warningChecks(status.checks);
    const detail = warnings.length ? `\n\nWarnings you acknowledged:\n- ${warnings.map(w => w.label).join('\n- ')}` : '';
    if (!window.confirm(`Install GameNode ${version} and restart ${subject} now? ${sentence(subject)} will be briefly unavailable.${detail}`)) return;
    const before = status.current_version;
    const next = await run('apply', () => adapter.apply(version, acknowledged));
    if (next) { sawDown.current = false; setAcknowledged(false); setWaiting({ before, target: version.replace(/^v/, ''), at: Date.now() }); }
  }

  if (loading) return <section className="detail-card update-panel"><SectionHeader title={title} description={description} /><LoadingState label="Loading update status…" /></section>;
  if (view && !view.supported) return <section className="detail-card update-panel"><SectionHeader title={title} description={description} /><EmptyState compact title="Remote update is not supported" description={`${subject} runs a GameNode version that predates remote updates. Update it manually once; later updates can then be started from here.`} icon={Rocket} /></section>;
  const outcome = outcomeText(status?.last_outcome);
  const progress = progressPercent(status?.progress);
  const shownChecks = refused.length ? refused : status?.checks ?? [];
  const hasBlock = shownChecks.some(check => check.status === 'block');
  const state = status?.state;

  return <section className="detail-card update-panel">
    <SectionHeader title={title} description={description} actions={<button className="quiet" disabled={!!busy || !!waiting || state === 'downloading' || state === 'applying'} onClick={() => void check()}><RefreshCw />{busy === 'check' ? 'Checking…' : 'Check for updates'}</button>} />
    {error && <p className="error notice" role="alert">{error}</p>}
    {notice && <p className="success notice notice--success" role="status">{notice}</p>}
    {!status && !error && <p className="hint">Update status is unavailable.</p>}
    {status && <>
      <div className="definition-list">
        <div className="definition-row"><span>Installed version</span><strong>{status.current_version || 'Unknown'}</strong></div>
        <div className="definition-row"><span>Platform</span><strong>{status.os} / {status.arch}</strong></div>
        <div className="definition-row"><span>Status</span><strong className={`status ${statusTone(status)}`}>{stateLabel(status)}</strong></div>
        <div className="definition-row"><span>Last checked</span><strong>{status.last_checked_at ? relativeTime(status.last_checked_at) : 'Not checked in this session'}</strong></div>
        <div className="definition-row"><span>Restart</span><strong>{status.restart_mode === 'exit' ? 'Exits; a supervisor must restart it' : 'Restarts itself'}</strong></div>
      </div>
      {extra}
      {!status.updatable && status.updatable_reason && <p className="notice notice--warning"><TriangleAlert /> {status.updatable_reason}</p>}
      {status.last_check_error && <p className="notice notice--warning"><TriangleAlert /> Could not check for updates: {status.last_check_error.message}</p>}
      {outcome && <p className={`notice ${status.last_outcome?.result === 'completed' ? 'notice--success' : 'notice--warning'}`}>{outcome}</p>}
      {status.error && <p className="error notice" role="alert">{status.error.message}</p>}
      {status.update_available && status.available && <div className="update-release">
        <div className="update-release__head"><div><span className="eyebrow">Update available</span><h3>GameNode {status.available.version}</h3></div>
          <a href={status.available.url} target="_blank" rel="noreferrer">Release on GitHub</a></div>
        <p className="hint">Published {publishedLabel(status.available.published_at)} · fetched only from the official GameNode releases and verified against its SHA-256 checksum before it can be installed.</p>
        {status.available.notes && <details className="update-notes"><summary>Release notes</summary><pre>{status.available.notes}</pre></details>}
      </div>}
      {state === 'downloading' && <div className="update-progress" role="progressbar" aria-label="Download progress" aria-valuemin={0} aria-valuemax={100} aria-valuenow={progress}>
        <div className="update-progress__bar"><span style={{ width: `${progress ?? 15}%` }} className={progress === undefined ? 'indeterminate' : ''} /></div>
        <small>{progress === undefined ? 'Downloading…' : `${progress}% · ${formatBytes(status.progress?.downloaded_bytes ?? 0)} of ${formatBytes(status.progress?.total_bytes ?? 0)}`}</small>
      </div>}
      {status.staged && state === 'ready' && <p className="notice notice--success"><CheckCircle2 /> GameNode {status.staged.version} is downloaded and verified (SHA-256 {shortHash(status.staged.sha256)}…). Nothing has been installed yet.</p>}
      {(status.update_available || state === 'ready' || state === 'failed') && <>
        <h3 className="update-checks-title">Safety checks</h3>
        <CheckList checks={shownChecks} />
        {hasBlock && <p className="hint">Blocking checks must be resolved before this update can proceed. They cannot be overridden.</p>}
      </>}
      {waiting && <LoadingState label={`Installing and restarting ${subject}… this can take a minute${reloadOnRestart ? '; the page reloads when it is back' : ''}.`} />}
      {!waiting && canManage && <div className="actions">
        {(state === 'idle' || state === 'failed') && status.update_available && <button disabled={!status.can_prepare || !!busy} onClick={() => void prepare()}><Download />{busy === 'prepare' ? 'Starting…' : state === 'failed' ? 'Retry download' : `Download and verify ${status.available?.version ?? ''}`}</button>}
        {state === 'downloading' && <button className="quiet" disabled={!!busy} onClick={() => void cancel()}><X />Cancel download</button>}
        {state === 'ready' && <>
          {status.requires_acknowledgement && <label className="update-ack"><input type="checkbox" checked={acknowledged} onChange={event => setAcknowledged(event.target.checked)} />I understand the warnings above and want to continue.</label>}
          <button disabled={!canInstall(status, acknowledged) || !!busy} onClick={() => void install()}><Rocket />{busy === 'apply' ? 'Installing…' : `Install ${status.staged?.version ?? ''} and restart`}</button>
          <button className="quiet" disabled={!!busy} onClick={() => void cancel()}>Discard download</button>
        </>}
      </div>}
      {!waiting && !canManage && status.update_available && <p className="hint">Downloading and installing updates requires the Update.Manage permission{subject !== 'this GameNode' ? ' and Node.Manage' : ''}.</p>}
    </>}
  </section>;
}

/** Settings > Updates for this instance. */
export function UpdatesSettings({ token, canManage, canManageSettings, autoCheck, onAutoCheckChange }: {
  token: string; canManage: boolean; canManageSettings: boolean; autoCheck: boolean; onAutoCheckChange: (next: boolean) => Promise<void>;
}) {
  const [adapter] = useState(() => localUpdateAdapter(token));
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  async function toggle(next: boolean) {
    setSaving(true); setError('');
    try { await onAutoCheckChange(next); } catch (reason) { setError(errorMessage(reason)); } finally { setSaving(false); }
  }
  return <>
    <UpdateManager adapter={adapter} canManage={canManage} subject="this GameNode" title="GameNode updates" description="Check the official GameNode releases, download a verified build, and install it. GameNode restarts itself; your servers keep running." reloadOnRestart
      extra={<label className="update-autocheck"><input type="checkbox" checked={autoCheck} disabled={!canManageSettings || saving} onChange={event => void toggle(event.target.checked)} /><span>Check for new releases automatically (about twice a day)<small className="hint">Only asks GitHub whether a newer release exists. Nothing is ever downloaded or installed without an administrator's action.{!canManageSettings ? ' Changing this needs Settings.Manage.' : ''}</small></span></label>} />
    {error && <p className="error notice" role="alert">{error}</p>}
  </>;
}

/** The Node detail Update card for an enrolled remote node. */
export function RemoteNodeUpdate({ nodeID, displayName, token, manage, onRestarted }: { nodeID: string; displayName: string; token: string; manage: boolean; onRestarted?: () => void }) {
  const [adapter] = useState(() => remoteUpdateAdapter(token, nodeID));
  return <UpdateManager adapter={adapter} canManage={manage} subject={displayName} title="Software update" description="The node checks and downloads the release itself from the official GameNode releases and runs its own safety checks; this controller only asks it to proceed." onRestarted={onRestarted} />;
}

/** Dashboard notice prompting an administrator when a newer release exists. */
export function UpdateBanner({ canOpenSettings }: { canOpenSettings: boolean }) {
  const [status, setStatus] = useState<UpdateStatus>();
  const [dismissed, setDismissed] = useState<string | undefined>(() => readDismissedVersion());
  useEffect(() => {
    let cancelled = false;
    const load = () => request<UpdateStatus>('/system/update?summary=1').then(value => { if (!cancelled) setStatus(value); }).catch(() => undefined);
    void load();
    const timer = window.setInterval(load, 30 * 60 * 1000);
    return () => { cancelled = true; window.clearInterval(timer); };
  }, []);
  if (!bannerVisible(status, dismissed) || !status?.available) return null;
  const version = status.available.version;
  return <div className="update-banner" role="status">
    <Rocket aria-hidden="true" />
    <div><strong>GameNode {version} is available</strong><span>You are running {status.current_version}.{status.updatable ? '' : ' This build cannot update itself.'}</span></div>
    <div className="update-banner__actions">
      {canOpenSettings && <button onClick={() => { requestSettingsTab('updates'); navigate('/settings'); }}>Review update</button>}
      <button className="quiet" onClick={() => { writeDismissedVersion(version); setDismissed(version); }} aria-label={`Dismiss the notice for GameNode ${version}`}>Dismiss</button>
    </div>
  </div>;
}
