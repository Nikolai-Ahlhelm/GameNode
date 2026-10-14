import { FormEvent, useEffect, useState } from 'react';
import { RefreshCw, Upload } from 'lucide-react';
import { SectionHeader } from './ui';

type Startup = { distribution: 'vanilla'|'neoforge'|'forge'|'fabric'|'paper'; java_executable: string; xms_mb: number; xmx_mb: number; jvm_arguments: string[]; program_arguments: string[] };
type StartupView = { configuration: Startup; executable: string; arguments: string[]; working_directory: string; java_found: boolean; container: boolean };

async function request<T>(url: string, token: string, init?: RequestInit): Promise<T> {
  const response = await fetch(url, { credentials: 'same-origin', ...init, headers: { ...(init?.headers ?? {}), ...(init?.method && init.method !== 'GET' ? { 'X-CSRF-Token': token } : {}) } });
  if (!response.ok) throw new Error((await response.json().catch(() => null))?.error?.message || 'Minecraft startup settings could not be loaded');
  return response.status === 204 ? undefined as T : response.json();
}

export function MinecraftStartupPanel({ serverID, token, canEdit, running }: { serverID: string; token: string; canEdit: boolean; running: boolean }) {
  const [view, setView] = useState<StartupView>(); const [draft, setDraft] = useState<Startup>(); const [error, setError] = useState(''); const [busy, setBusy] = useState(false);
  async function load() { setError(''); try { const result = await request<StartupView>(`/api/v1/servers/${serverID}/minecraft-startup`, token); setView(result); setDraft(result.configuration); } catch (reason) { setError(reason instanceof Error ? reason.message : 'Startup configuration could not be loaded'); } }
  useEffect(() => { setView(undefined); void load(); }, [serverID]);
  async function save(event: FormEvent) { event.preventDefault(); if (!draft) return; setBusy(true); setError(''); try { const result = await request<StartupView>(`/api/v1/servers/${serverID}/minecraft-startup`, token, { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ...draft, jvm_arguments: draft.jvm_arguments, program_arguments: draft.program_arguments }) }); setView(result); setDraft(result.configuration); } catch (reason) { setError(reason instanceof Error ? reason.message : 'Startup configuration could not be saved'); } finally { setBusy(false); } }
  async function importArgs() { setBusy(true); setError(''); try { await request<void>(`/api/v1/servers/${serverID}/minecraft-startup/import`, token, { method: 'POST' }); await load(); } catch (reason) { setError(reason instanceof Error ? reason.message : 'JVM arguments could not be imported'); } finally { setBusy(false); } }
  if (!draft) return <section className="subpanel"><SectionHeader title="Minecraft Java startup" />{error ? <p className="error notice">{error}</p> : <p className="muted">Loading Java startup configuration…</p>}</section>;
  const update = (patch: Partial<Startup>) => setDraft(current => current ? { ...current, ...patch } : current);
  return <section className="subpanel minecraft-startup"><SectionHeader title="Minecraft Java startup" description={view?.container ? 'Structured Java command passed directly to the existing container runtime.' : 'Java starts as a direct process; no batch file or shell command is used.'} actions={<button className="quiet" type="button" onClick={() => void load()}><RefreshCw />Refresh</button>} />
    {error && <p className="error notice" role="alert">{error}</p>}{running && <p className="notice notice--warning">Stop the server before changing startup settings.</p>}
    <form onSubmit={save}>
      <label>Distribution<select value={draft.distribution} disabled={!canEdit || running} onChange={event => update({ distribution: event.target.value as Startup['distribution'] })}><option value="vanilla">Vanilla</option><option value="neoforge">NeoForge</option><option value="forge">Forge</option><option value="fabric">Fabric</option><option value="paper">Paper</option></select></label>
      <label>Java executable/runtime<input value={draft.java_executable} disabled={!canEdit || running} onChange={event => update({ java_executable: event.target.value })} placeholder={view?.container ? 'java (inside container)' : 'Detected Java runtime'} /></label>
      <div className="form-grid"><label>Minimum heap (MiB)<input type="number" min="1" value={draft.xms_mb} disabled={!canEdit || running} onChange={event => update({ xms_mb: Number(event.target.value) })} /></label><label>Maximum heap (MiB)<input type="number" min="1" value={draft.xmx_mb} disabled={!canEdit || running} onChange={event => update({ xmx_mb: Number(event.target.value) })} /></label></div>
      <label>Additional JVM arguments <small>One argument per line; Xms/Xmx are managed above.</small><textarea value={draft.jvm_arguments.join('\n')} disabled={!canEdit || running} onChange={event => update({ jvm_arguments: event.target.value.split('\n').filter(value => value.length > 0) })} rows={4} /></label>
      <label>Program arguments <small>One argument per line.</small><textarea value={draft.program_arguments.join('\n')} disabled={!canEdit || running} onChange={event => update({ program_arguments: event.target.value.split('\n').filter(value => value.length > 0) })} rows={3} /></label>
      <div className="actions">{canEdit && <><button type="submit" disabled={busy || running}>{busy ? 'Saving…' : 'Save startup settings'}</button><button className="quiet" type="button" disabled={busy || running} onClick={() => void importArgs()}><Upload />Import user_jvm_args.txt</button></>}</div>
    </form>
    {view && <><SectionHeader title="Resolved startup preview" description={`Working directory: ${view.working_directory}`} /><div className="definition-list"><div className="definition-row"><span>Java</span><code>{view.executable}</code></div><div className="definition-row"><span>Arguments</span><code>{view.arguments.join(' ')}</code></div><div className="definition-row"><span>Java check</span><strong>{view.container ? 'Resolved by container image at start' : view.java_found ? 'Executable available' : 'Not found'}</strong></div></div></>}
  </section>;
}
