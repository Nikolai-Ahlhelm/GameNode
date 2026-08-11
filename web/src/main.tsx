import { FormEvent, useEffect, useState } from 'react';
import { createRoot } from 'react-dom/client';
import { hasCapability } from './capabilities';
import { ConsoleTab } from './console';
import { FilesTab } from './files';
import { IdentityAdmin } from './identity';
import './styles.css';

type User = { id: string; username: string; display_name: string; is_admin: boolean };
type Me = { user: User; csrf_token: string; capabilities?: string[] };
type GameServer = { id: string; creation_mode: 'new' | 'adopt' | 'custom'; name: string; description: string; working_directory: string; executable: string; arguments: string[]; environment_variables: Record<string, string>; runtime_type: 'native'; auto_start: boolean; restart_policy: 'never'; stop_method: 'terminate'; stop_command: string; stop_timeout_seconds: number };
type ServerRecord = { server: GameServer; runtime: { pid?: number; current_state: string; last_error?: string }; capabilities?: string[] };
type ServerAction = 'start' | 'stop' | 'restart' | 'kill' | 'delete';

const api = async <T,>(path: string, init?: RequestInit): Promise<T> => {
  const response = await fetch(`/api/v1${path}`, { credentials: 'same-origin', headers: { 'Content-Type': 'application/json', ...(init?.headers ?? {}) }, ...init });
  if (!response.ok) {
    const body = await response.json().catch(() => null);
    throw new Error(body?.error?.message ?? 'Request failed');
  }
  return response.status === 204 ? undefined as T : response.json();
};
const csrf = (token: string) => ({ 'X-CSRF-Token': token });

function Credentials({ setup, onComplete }: { setup: boolean; onComplete: (me: Me) => void }) {
  const [username, setUsername] = useState(''); const [email, setEmail] = useState(''); const [password, setPassword] = useState(''); const [error, setError] = useState('');
  async function submit(event: FormEvent) { event.preventDefault(); try { onComplete(await api<Me>(setup ? '/setup' : '/auth/login', { method: 'POST', body: JSON.stringify(setup ? { username, email, password } : { username, password }) })); } catch (reason) { setError(reason instanceof Error ? reason.message : 'Request failed'); } }
  return <main className="auth"><section className="card"><p className="eyebrow">GAMENODE</p><h1>{setup ? 'Create administrator' : 'Welcome back'}</h1><form onSubmit={submit}>{setup && <label>Email<input type="email" value={email} onChange={event => setEmail(event.target.value)} required /></label>}<label>Username<input value={username} onChange={event => setUsername(event.target.value)} required /></label><label>Password<input type="password" value={password} onChange={event => setPassword(event.target.value)} minLength={12} required /></label>{error && <p className="error">{error}</p>}<button>{setup ? 'Create administrator' : 'Sign in'}</button></form></section></main>;
}

const empty = (): GameServer => ({ id: '', creation_mode: 'custom', name: '', description: '', working_directory: '', executable: '', arguments: [], environment_variables: {}, runtime_type: 'native', auto_start: false, restart_policy: 'never', stop_method: 'terminate', stop_command: '', stop_timeout_seconds: 15 });

function ServerForm({ initial, token, onDone, onCancel }: { initial?: GameServer; token: string; onDone: (record: ServerRecord) => void; onCancel: () => void }) {
  const [value, setValue] = useState(initial ?? empty()); const [args, setArgs] = useState((initial?.arguments ?? []).join('\n')); const [env, setEnv] = useState(Object.entries(initial?.environment_variables ?? {}).map(([key, entry]) => `${key}=${entry}`).join('\n')); const [error, setError] = useState('');
  async function submit(event: FormEvent) { event.preventDefault(); try { const variables: Record<string, string> = {}; env.split('\n').filter(Boolean).forEach(line => { const index = line.indexOf('='); if (index < 1) throw new Error('Environment lines must be KEY=VALUE'); variables[line.slice(0, index)] = line.slice(index + 1); }); onDone(await api<ServerRecord>(initial ? `/servers/${initial.id}` : '/servers', { method: initial ? 'PATCH' : 'POST', headers: csrf(token), body: JSON.stringify({ ...value, arguments: args.split('\n').filter(Boolean), environment_variables: variables }) })); } catch (reason) { setError(reason instanceof Error ? reason.message : 'Request failed'); } }
  return <section className="panel form-panel"><div className="row"><h2>{initial ? 'Edit server' : 'Add server'}</h2><button className="quiet" type="button" onClick={onCancel}>Cancel</button></div><form onSubmit={submit}><label>Workflow<select value={value.creation_mode} onChange={event => setValue({ ...value, creation_mode: event.target.value as GameServer['creation_mode'] })}><option value="new">Create New</option><option value="adopt">Adopt Existing</option><option value="custom">Custom Application</option></select></label><p className="hint">Adopt Existing only registers existing paths; it never changes files.</p><label>Name<input value={value.name} onChange={event => setValue({ ...value, name: event.target.value })} required /></label><label>Description<textarea value={value.description} onChange={event => setValue({ ...value, description: event.target.value })} /></label><label>Working directory<input value={value.working_directory} onChange={event => setValue({ ...value, working_directory: event.target.value })} required /></label><label>Executable<input value={value.executable} onChange={event => setValue({ ...value, executable: event.target.value })} required /></label><label>Arguments <span className="hint">one per line</span><textarea value={args} onChange={event => setArgs(event.target.value)} /></label><label>Environment <span className="hint">KEY=VALUE, one per line</span><textarea value={env} onChange={event => setEnv(event.target.value)} /></label><label>Stop timeout (seconds)<input type="number" min="1" max="300" value={value.stop_timeout_seconds} onChange={event => setValue({ ...value, stop_timeout_seconds: Number(event.target.value) })} /></label>{error && <p className="error">{error}</p>}<button>Save server</button></form></section>;
}

function ServerDetail({ record, token, onBack, onEdit, onAction, error }: { record: ServerRecord; token: string; onBack: () => void; onEdit: () => void; onAction: (action: ServerAction) => void; error: string }) {
  const [tab, setTab] = useState<'overview' | 'console' | 'files' | 'configuration'>('overview');
  const can = (permission: string) => hasCapability(record.capabilities, permission);
  const action = (label: string, permission: string, name: ServerAction, className?: string) => can(permission) && <button className={className} onClick={() => onAction(name)}>{label}</button>;
  return <section className="panel"><div className="row"><div><p className="eyebrow">{record.server.creation_mode}</p><h2>{record.server.name}</h2></div><button className="quiet" onClick={onBack}>Back</button></div><p><span className={`status ${record.runtime.current_state}`}>{record.runtime.current_state}</span>{record.runtime.pid ? ` PID ${record.runtime.pid}` : ''}</p><nav className="detail-tabs"><button className={tab === 'overview' ? 'active' : ''} onClick={() => setTab('overview')}>Overview</button>{can('Console.View') && <button className={tab === 'console' ? 'active' : ''} onClick={() => setTab('console')}>Console</button>}{can('Files.View') && <button className={tab === 'files' ? 'active' : ''} onClick={() => setTab('files')}>Files</button>}{can('Server.Edit') && <button className={tab === 'configuration' ? 'active' : ''} onClick={() => setTab('configuration')}>Configuration</button>}</nav>{tab === 'overview' && <><p className="muted">{record.server.working_directory}<br />{record.server.executable}</p>{record.runtime.last_error && <p className="error">{record.runtime.last_error}</p>}<div className="actions">{action('Start', 'Server.Start', 'start')}{action('Stop', 'Server.Stop', 'stop')}{action('Restart', 'Server.Restart', 'restart')}{action('Kill', 'Server.Kill', 'kill', 'danger')}{can('Server.Edit') && <button className="quiet" onClick={onEdit}>Edit</button>}{action('Delete', 'Server.Delete', 'delete', 'danger quiet')}</div></>}{tab === 'console' && can('Console.View') && <ConsoleTab serverID={record.server.id} serverState={record.runtime.current_state} canSend={can('Console.Send')} />}{tab === 'files' && can('Files.View') && <FilesTab serverID={record.server.id} token={token} permissions={record.capabilities} />}{tab === 'configuration' && can('Server.Edit') && <p className="muted">{record.server.working_directory}<br />{record.server.executable}</p>}{error && <p className="error">{error}</p>}</section>;
}

function Servers({ token, capabilities }: { token: string; capabilities?: string[] }) {
  const [records, setRecords] = useState<ServerRecord[]>([]); const [selected, setSelected] = useState<ServerRecord>(); const [form, setForm] = useState<'create' | 'edit'>(); const [error, setError] = useState('');
  const load = () => api<{ servers: ServerRecord[] }>('/servers').then(result => setRecords(result.servers)).catch(reason => setError(reason.message));
  useEffect(() => { load(); }, []);
  const saved = (record: ServerRecord) => { setSelected(record); setForm(undefined); load(); };
  async function action(name: ServerAction) { if (!selected) return; try { if (name === 'delete') { await api<void>(`/servers/${selected.server.id}`, { method: 'DELETE', headers: csrf(token) }); setSelected(undefined); load(); return; } saved(await api<ServerRecord>(`/servers/${selected.server.id}/${name}`, { method: 'POST', headers: csrf(token) })); } catch (reason) { setError(reason instanceof Error ? reason.message : 'Request failed'); } }
  if (form) return <ServerForm initial={form === 'edit' ? selected?.server : undefined} token={token} onDone={saved} onCancel={() => setForm(undefined)} />;
  if (selected) return <ServerDetail record={selected} token={token} onBack={() => setSelected(undefined)} onEdit={() => setForm('edit')} onAction={action} error={error} />;
  return <section><div className="row"><h1>Servers</h1>{hasCapability(capabilities, 'Server.Create') && <button onClick={() => setForm('create')}>Add server</button>}</div>{error && <p className="error">{error}</p>}<div className="server-list">{records.length === 0 ? <section className="panel"><h2>No servers yet</h2><p>Create a custom application or adopt an existing installation.</p></section> : records.map(record => <button className="server-card" key={record.server.id} onClick={() => setSelected(record)}><strong>{record.server.name}</strong><span className={`status ${record.runtime.current_state}`}>{record.runtime.current_state}</span><small>{record.server.working_directory}</small></button>)}</div></section>;
}

function Dashboard({ me, onLogout }: { me: Me; onLogout: () => void }) {
  const [page, setPage] = useState<'dashboard' | 'servers' | 'identity'>('dashboard');
  async function logout() { await api('/auth/logout', { method: 'POST', headers: csrf(me.csrf_token) }); onLogout(); }
  const canIdentity = me.user.is_admin || hasCapability(me.capabilities, 'Users.View') || hasCapability(me.capabilities, 'Users.Manage') || hasCapability(me.capabilities, 'Groups.View') || hasCapability(me.capabilities, 'Groups.Manage');
  return <main className="app"><aside><p className="eyebrow">GAMENODE</p><nav><button className={page === 'dashboard' ? 'active' : ''} onClick={() => setPage('dashboard')}>Dashboard</button><button className={page === 'servers' ? 'active' : ''} onClick={() => setPage('servers')}>Servers</button>{canIdentity && <button className={page === 'identity' ? 'active' : ''} onClick={() => setPage('identity')}>Users & groups</button>}</nav><button className="quiet" onClick={logout}>Sign out</button></aside><section className="content">{page === 'servers' ? <Servers token={me.csrf_token} capabilities={me.capabilities} /> : page === 'identity' && canIdentity ? <IdentityAdmin token={me.csrf_token} capabilities={me.capabilities} isAdmin={me.user.is_admin} /> : <section className="panel"><p className="eyebrow">LOCAL NODE</p><h1>Dashboard</h1><p className="muted">Signed in as {me.user.username}. Manage native applications from Servers.</p></section>}</section></main>;
}

function App() {
  const [mode, setMode] = useState<'loading' | 'setup' | 'login' | 'dashboard'>('loading'); const [me, setMe] = useState<Me>();
  useEffect(() => { Promise.all([api<{ setup_required: boolean }>('/setup/status'), api<Me>('/auth/me').catch(() => undefined)]).then(([setup, current]) => { if (current) { setMe(current); setMode('dashboard'); } else setMode(setup.setup_required ? 'setup' : 'login'); }); }, []);
  if (mode === 'loading') return <main className="auth">Loading…</main>;
  return mode === 'dashboard' && me ? <Dashboard me={me} onLogout={() => { setMe(undefined); setMode('login'); }} /> : <Credentials setup={mode === 'setup'} onComplete={current => { setMe(current); setMode('dashboard'); }} />;
}

createRoot(document.getElementById('root')!).render(<App />);
