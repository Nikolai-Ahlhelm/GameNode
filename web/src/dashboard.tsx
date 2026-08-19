import { useEffect, useState } from 'react';
import { Activity, ArrowRight, CircleAlert, CircleStop, Network, Play, RefreshCw, RotateCcw, Server, ShieldCheck } from 'lucide-react';
import { auditActionLabel, auditActor } from './audit-helpers';
import { EmptyState, MetricCard, PageHeader, SectionHeader, SkeletonCards, SkeletonRows } from './ui';
import './dashboard.css';

type Recent = { id: string; timestamp: string; actor_username?: string; actor_user_id?: string; action: string; resource_type: string; resource_name?: string; result: string };
type Data = { servers?: Record<string, number>; monitoring?: Record<string, number>; ports?: Record<string, number>; audit?: { available?: boolean; recent?: Recent[] } };
const num = (value: Record<string, number> | undefined, key: string) => Number.isFinite(value?.[key]) ? value![key] : 0;

export function DashboardOverview({ canCreate, canAudit, onAudit, onServers }: { canCreate: boolean; canAudit: boolean; onAudit: () => void; onServers?: () => void }) {
  const [data, setData] = useState<Data>();
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const load = () => { setLoading(true); setError(''); fetch('/api/v1/dashboard', { credentials: 'same-origin' }).then(async response => { if (!response.ok) throw Error((await response.json().catch(() => null))?.error?.message || 'Request failed'); return response.json(); }).then(setData).catch(reason => setError(reason instanceof Error ? reason.message : 'Request failed')).finally(() => setLoading(false)); };
  useEffect(() => { void load(); }, []);
  if (loading) return <section><PageHeader title="Infrastructure overview" description="Live summary of the servers and services visible to you." eyebrow="GameNode control plane" /><SkeletonCards count={4} label="Loading infrastructure overview…" /><SkeletonRows count={2} /></section>;
  if (error) return <section><PageHeader title="Infrastructure overview" description="Live summary of the servers and services visible to you." eyebrow="GameNode control plane" /><EmptyState title="Dashboard unavailable" description={error} icon={CircleAlert} action={<button type="button" onClick={load}><RefreshCw />Retry</button>} /></section>;
  const servers = data?.servers, monitoring = data?.monitoring, ports = data?.ports, total = num(servers, 'total'), recent = data?.audit?.recent ?? [];
  const running = num(servers, 'running'), stopped = num(servers, 'stopped'), crashed = num(servers, 'crashed'), detached = num(servers, 'detached');
  const statusParts = total ? [{ label: 'Running', value: running, tone: 'running' }, { label: 'Stopped', value: stopped, tone: 'stopped' }, { label: 'Crashed', value: crashed, tone: 'crashed' }, { label: 'Detached', value: detached, tone: 'detached' }].filter(part => part.value > 0) : [];
  return <section className="dashboard-overview">
    <PageHeader title="Infrastructure overview" description="Live summary of the servers and services visible to you." eyebrow="GameNode control plane" actions={<button className="quiet" onClick={onServers}>View servers <ArrowRight /></button>} />
    {total === 0 ? <EmptyState title="No servers yet" description="Add a custom application or adopt an existing installation to start managing it with GameNode." icon={Server} action={canCreate ? <button onClick={onServers}>Add your first server</button> : undefined} /> : <>
      <div className="dashboard-kpis"><MetricCard label="Total servers" value={total} hint="Visible to your account" icon={Server} tone="info" /><MetricCard label="Running" value={running} hint={`${Math.round(running / total * 100)}% of visible servers`} icon={Play} tone="success" /><MetricCard label="Stopped" value={stopped} hint="Currently offline" icon={CircleStop} /><MetricCard label="Needs attention" value={crashed + detached} hint="Crashed or detached" icon={CircleAlert} tone={crashed + detached ? 'danger' : 'neutral'} /></div>
      <div className="dashboard-columns">
        <section className="panel status-overview"><SectionHeader title="Server availability" description="Current lifecycle state distribution" /><div className="status-track" aria-label="Server state distribution">{statusParts.map(part => <span key={part.label} className={`status-track__part status-track__part--${part.tone}`} style={{ width: `${part.value / total * 100}%` }} title={`${part.label}: ${part.value}`} />)}</div><div className="status-legend">{[{ label: 'Running', value: running }, { label: 'Stopped', value: stopped }, { label: 'Crashed', value: crashed }, { label: 'Detached', value: detached }].map(item => <div key={item.label}><span className={`legend-dot legend-dot--${item.label.toLowerCase()}`} /><span>{item.label}</span><strong>{item.value}</strong></div>)}</div></section>
        <section className="panel reliability-panel"><SectionHeader title="Reliability" description="Restart and failure signals" /><div className="reliability-list"><div><CircleAlert /><span>Degraded servers</span><strong>{num(monitoring, 'degraded')}</strong></div><div><RotateCcw /><span>Pending restarts</span><strong>{num(monitoring, 'pending_auto_restart')}</strong></div><div><ShieldCheck /><span>Auto-restart enabled</span><strong>{num(monitoring, 'auto_restart_enabled')}</strong></div><div><Activity /><span>Total crashes / restarts</span><strong>{num(monitoring, 'total_crashes')} / {num(monitoring, 'total_restarts')}</strong></div></div></section>
      </div>
      <SectionHeader title="Network inventory" description="Configured port assignments across visible servers" />
      <div className="dashboard-network"><MetricCard label="Configured ports" value={num(ports, 'total')} icon={Network} /><MetricCard label="TCP assignments" value={num(ports, 'tcp')} hint="Connection-oriented" /><MetricCard label="UDP assignments" value={num(ports, 'udp')} hint="Datagram traffic" /></div>
    </>}
    {data?.audit?.available && <section className="panel dashboard-audit"><SectionHeader title="Recent activity" description="Latest security and administrative events" actions={canAudit ? <button type="button" className="quiet" onClick={onAudit}>Open audit log <ArrowRight /></button> : undefined} />{recent.length === 0 ? <EmptyState compact title="No recent activity" description="New administrative actions will appear here." icon={Activity} /> : <div className="activity-list">{recent.map(event => <article key={event.id}><span className={`activity-marker activity-marker--${event.result}`} /><div><strong>{auditActionLabel(event.action)}</strong><p>{auditActor(event.actor_username, event.actor_user_id)} · {event.resource_type} {event.resource_name || ''}</p></div><div className="activity-meta"><span className={`status ${event.result === 'failure' ? 'crashed' : 'running'}`}>{event.result}</span><time>{new Date(event.timestamp).toLocaleString()}</time></div></article>)}</div>}</section>}
  </section>;
}
