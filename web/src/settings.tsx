import { FormEvent, useEffect, useState } from 'react';
import './settings.css';
import { SettingsForm, SettingsResponse, settingsForm, settingsPatch, validHistoryLimit, validSampleInterval } from './settings-helpers';
import { supportFilename } from './support-helpers';

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`/api/v1${path}`, { credentials: 'same-origin', headers: { 'Content-Type': 'application/json', ...(init?.headers ?? {}) }, ...init });
  if (!response.ok) {
    const body = await response.json().catch(() => null);
    throw new Error(body?.error?.message ?? 'Request failed');
  }
  return response.json();
}

export function SettingsPage({ token, canManage }: { token: string; canManage: boolean }) {
  const [current, setCurrent] = useState<SettingsResponse>();
  const [form, setForm] = useState<SettingsForm>({ sampleInterval: '', historyLimit: '' });
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [loading, setLoading] = useState(true);
  const load = () => {
    setLoading(true); setError('');
    void request<SettingsResponse>('/settings').then(value => { setCurrent(value); setForm(settingsForm(value)); }).catch(reason => setError(reason instanceof Error ? reason.message : 'Request failed')).finally(() => setLoading(false));
  };
  useEffect(load, []);
  const patch = current ? settingsPatch(current, form) : undefined;
  const valid = validSampleInterval(form.sampleInterval) && validHistoryLimit(form.historyLimit);
  async function save(event: FormEvent) {
    event.preventDefault();
    if (!patch || !valid) return;
    setError(''); setNotice('');
    try {
      const value = await request<SettingsResponse>('/settings', { method: 'PATCH', headers: { 'X-CSRF-Token': token }, body: JSON.stringify(patch) });
      setCurrent(value); setForm(settingsForm(value)); setNotice('Settings saved. Restart GameNode for changes to take effect.');
    } catch (reason) { setError(reason instanceof Error ? reason.message : 'Request failed'); }
  }
  if (loading) return <section><h1>Settings</h1><p className="muted">Loading settings…</p></section>;
  if (!current) return <section><h1>Settings</h1><section className="panel"><p className="error">{error || 'Settings are unavailable.'}</p><button className="quiet" onClick={load}>Retry</button></section></section>;
  const field = (label: string, description: string, value: string, update: (next: string) => void, min: number, max: number, unit: string, validValue: boolean) => <label className="settings-field">{label}<span className="hint">{description}</span><div className="settings-number"><input id={label.toLowerCase().replaceAll(' ', '-')} type="number" min={min} max={max} step="1" value={value} onChange={event => update(event.target.value)} disabled={!canManage} aria-invalid={!validValue} /><span>{unit}</span></div>{!validValue && <span className="error">Enter a whole number from {min} to {max}.</span>}</label>;
  return <section className="settings-page"><div className="row"><div><h1>Settings</h1><p className="muted">Configure GameNode platform settings.</p></div></div><section className="panel settings-panel"><div className="row"><div><h2>Monitoring</h2><p className="muted">Configure bounded in-memory monitoring collection for each server.</p></div>{current.restart_required && <span className="status starting">Restart required</span>}</div><div className="settings-restart" role="note">Changes are saved immediately but take effect after the GameNode process is restarted.</div><form onSubmit={save} className="settings-form">{field('Sample interval', 'How often GameNode samples server monitoring data.', form.sampleInterval, sampleInterval => { setForm({ ...form, sampleInterval }); setNotice(''); }, 1, 300, 'seconds', validSampleInterval(form.sampleInterval))}{field('History limit', 'Maximum number of in-memory monitoring samples retained per server.', form.historyLimit, historyLimit => { setForm({ ...form, historyLimit }); setNotice(''); }, 1, 10000, 'samples', validHistoryLimit(form.historyLimit))}{error && <p className="error">{error}</p>}{notice && <p className="settings-success">{notice}</p>}{canManage && <div className="actions"><button type="submit" disabled={!patch || !valid}>Save changes</button></div>}</form></section><DiagnosticsInfo /><SupportInfo token={token} canManage={canManage}/></section>;
}

type Diagnostics = { status:string; application:{version?:string;go_version:string;process_started_at:string;uptime_seconds:number};platform:{os:string;arch:string;logical_cpus:number};database:{type:string;schema_version?:string;healthy:boolean};monitoring:{sample_interval_seconds:number;history_limit:number;restart_required:boolean} };
function DiagnosticsInfo(){const[value,setValue]=useState<Diagnostics>();const[error,setError]=useState('');const load=()=>{setError('');void request<Diagnostics>('/diagnostics').then(setValue).catch(e=>setError(e instanceof Error?e.message:'Request failed'))};useEffect(load,[]);if(error)return <section className="panel settings-diagnostics"><h2>System information</h2><p className="error">{error}</p><button className="quiet" onClick={load}>Retry</button></section>;if(!value)return <section className="panel settings-diagnostics"><h2>System information</h2><p className="muted">Loading diagnostics…</p></section>;return <section className="panel settings-diagnostics"><div className="row"><div><h2>System information</h2><p className="muted">Safe local GameNode diagnostics.</p></div><span className={`status ${value.status==='healthy'?'running':'starting'}`}>{value.status}</span></div><div className="settings-diagnostic-grid"><div><small>GameNode</small><strong>{value.application.version||'Development build'}</strong><span>Uptime {Math.floor(value.application.uptime_seconds/60)} min</span></div><div><small>Platform</small><strong>{value.platform.os} / {value.platform.arch}</strong><span>{value.platform.logical_cpus} logical CPUs</span></div><div><small>Database</small><strong>{value.database.type}</strong><span>Schema {value.database.schema_version||'unavailable'} · {value.database.healthy?'healthy':'degraded'}</span></div><div><small>Monitoring</small><strong>{value.monitoring.sample_interval_seconds}s / {value.monitoring.history_limit} samples</strong><span>{value.monitoring.restart_required?'Restart required':'Current runtime configuration'}</span></div></div></section>}
function SupportInfo({token,canManage}:{token:string;canManage:boolean}){const[busy,setBusy]=useState(false),[message,setMessage]=useState('');const generate=async()=>{if(busy)return;setBusy(true);setMessage('');try{const r=await fetch('/api/v1/support/bundle',{method:'POST',credentials:'same-origin',headers:{'X-CSRF-Token':token}});if(!r.ok){const b=await r.json().catch(()=>null);throw new Error(b?.error?.message??'Request failed')}const url=URL.createObjectURL(await r.blob());const a=document.createElement('a');a.href=url;a.download=supportFilename(r.headers.get('Content-Disposition'));a.click();URL.revokeObjectURL(url);setMessage('Support bundle generated.')}catch(e){setMessage(e instanceof Error?e.message:'Request failed')}finally{setBusy(false)}};return <section className="panel settings-diagnostics"><h2>Support</h2><p className="muted">Generate a sanitized support bundle for troubleshooting. It includes diagnostics, safe settings, recent audit activity, and sanitized server summaries. It excludes passwords, server files, console contents, raw logs, and database files.</p>{canManage&&<button onClick={generate} disabled={busy}>{busy?'Generating…':'Generate support bundle'}</button>}{message&&<p className={message==='Support bundle generated.'?'settings-success':'error'}>{message}</p>}</section>}
