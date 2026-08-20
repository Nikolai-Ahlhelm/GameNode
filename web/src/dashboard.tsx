import { useEffect, useState } from 'react';
import { Activity, ArrowRight, CircleAlert, CircleStop, Cpu, MemoryStick, Network, Play, RefreshCw, RotateCcw, Server, ShieldCheck } from 'lucide-react';
import { auditActionLabel, auditActor } from './audit-helpers';
import { EmptyState, MetricCard, PageHeader, SectionHeader, SkeletonCards, SkeletonRows } from './ui';
import './dashboard.css';

type Recent = { id: string; timestamp: string; actor_username?: string; actor_user_id?: string; action: string; resource_type: string; resource_name?: string; result: string };
type Workload = { cpu_percent?: number; memory_bytes?: number; sampled_servers?: number };
type Data = { servers?: Record<string, number>; monitoring?: Record<string, number>; ports?: Record<string, number>; workload?: Workload; audit?: { available?: boolean; recent?: Recent[] } };
type RemoteNode = { id: string; display_name: string; last_health: string; enabled: boolean };
type NodeStatus = { servers: Record<string, number>; workload: Workload };
const num = (value: Record<string, number> | undefined, key: string) => Number.isFinite(value?.[key]) ? value![key] : 0;
const readableBytes = (value = 0) => value < 1024 ** 2 ? `${(value / 1024).toFixed(0)} KB` : value < 1024 ** 3 ? `${(value / 1024 ** 2).toFixed(1)} MB` : `${(value / 1024 ** 3).toFixed(1)} GB`;
const nodeHealth = (health: string) => health === 'reachable' ? 'Reachable' : health === 'degraded' ? 'Degraded' : health === 'unreachable' ? 'Unreachable' : 'Unavailable';
const nodeTone = (health: string) => health === 'reachable' ? 'running' : health === 'degraded' ? 'degraded' : 'crashed';

export function DashboardOverview({ canCreate, canAudit, canNodes, onAudit, onServers, onNodes }: { canCreate: boolean; canAudit: boolean; canNodes: boolean; onAudit: () => void; onServers?: () => void; onNodes?: () => void }) {
  const [data, setData] = useState<Data>();
  const [nodes, setNodes] = useState<RemoteNode[]>([]);
  const [nodeStatuses, setNodeStatuses] = useState<Record<string, NodeStatus>>({});
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const load = () => { setLoading(true); setError(''); fetch('/api/v1/dashboard', { credentials: 'same-origin' }).then(async response => { if (!response.ok) throw Error((await response.json().catch(() => null))?.error?.message || 'Request failed'); return response.json(); }).then(setData).catch(reason => setError(reason instanceof Error ? reason.message : 'Request failed')).finally(() => setLoading(false)); };
  useEffect(() => { void load(); }, []);
  useEffect(() => {
    if (!canNodes) return;
    fetch('/api/v1/remote-nodes', { credentials: 'same-origin' }).then(r => r.ok ? r.json() : Promise.reject()).then(async ({ remote_nodes }) => {
      const items: RemoteNode[] = Array.isArray(remote_nodes) ? remote_nodes : [];
      setNodes(items);
      const results = await Promise.all(items.filter(node => node.enabled).map(async node => {
        const response = await fetch(`/api/v1/remote-nodes/${encodeURIComponent(node.id)}/status`, { credentials: 'same-origin' });
        return response.ok ? [node.id, (await response.json()).remote_node_status as NodeStatus] as const : [node.id, undefined] as const;
      }));
      setNodeStatuses(Object.fromEntries(results.filter((entry): entry is readonly [string, NodeStatus] => entry[1] !== undefined)));
    }).catch(() => { setNodes([]); setNodeStatuses({}); });
  }, [canNodes]);
  if (loading) return <section><PageHeader title="Infrastructure overview" description="Live summary of the servers and services visible to you." eyebrow="GameNode control plane" /><SkeletonCards count={4} label="Loading infrastructure overview…" /><SkeletonRows count={2} /></section>;
  if (error) return <section><PageHeader title="Infrastructure overview" description="Live summary of the servers and services visible to you." eyebrow="GameNode control plane" /><EmptyState title="Dashboard unavailable" description={error} icon={CircleAlert} action={<button type="button" onClick={load}><RefreshCw />Retry</button>} /></section>;
  const servers = data?.servers, monitoring = data?.monitoring, ports = data?.ports, total = num(servers, 'total'), recent = data?.audit?.recent ?? [];
  const running = num(servers, 'running'), stopped = num(servers, 'stopped'), crashed = num(servers, 'crashed'), detached = num(servers, 'detached');
  const localWorkload = data?.workload;
  const statusParts = total ? [{ label: 'Running', value: running, tone: 'running' }, { label: 'Stopped', value: stopped, tone: 'stopped' }, { label: 'Crashed', value: crashed, tone: 'crashed' }, { label: 'Detached', value: detached, tone: 'detached' }].filter(part => part.value > 0) : [];
  return <section className="dashboard-overview">
    <PageHeader title="Infrastructure overview" description="Live summary of the servers and services visible to you." eyebrow="GameNode control plane" actions={<button className="quiet" onClick={onServers}>View servers <ArrowRight /></button>} />
    {total === 0 && nodes.length === 0 ? <EmptyState title="No servers or remote nodes yet" description="Add a custom application, adopt an existing installation, or enroll a remote node to start managing infrastructure with GameNode." icon={Server} action={canCreate ? <button onClick={onServers}>Add your first server</button> : undefined} /> : <>
      <div className="dashboard-kpis"><MetricCard label="Total servers" value={total} hint="Visible to your account" icon={Server} tone="info" /><MetricCard label="Running" value={running} hint={`${Math.round(running / total * 100)}% of visible servers`} icon={Play} tone="success" /><MetricCard label="Stopped" value={stopped} hint="Currently offline" icon={CircleStop} /><MetricCard label="Needs attention" value={crashed + detached} hint="Crashed or detached" icon={CircleAlert} tone={crashed + detached ? 'danger' : 'neutral'} /></div>
      <div className="dashboard-columns">
        <section className="panel status-overview"><SectionHeader title="Server availability" description="Current lifecycle state distribution" /><div className="status-track" aria-label="Server state distribution">{statusParts.map(part => <span key={part.label} className={`status-track__part status-track__part--${part.tone}`} style={{ width: `${part.value / total * 100}%` }} title={`${part.label}: ${part.value}`} />)}</div><div className="status-legend">{[{ label: 'Running', value: running }, { label: 'Stopped', value: stopped }, { label: 'Crashed', value: crashed }, { label: 'Detached', value: detached }].map(item => <div key={item.label}><span className={`legend-dot legend-dot--${item.label.toLowerCase()}`} /><span>{item.label}</span><strong>{item.value}</strong></div>)}</div></section>
        <section className="panel reliability-panel"><SectionHeader title="Reliability" description="Restart and failure signals" /><div className="reliability-list"><div><CircleAlert /><span>Degraded servers</span><strong>{num(monitoring, 'degraded')}</strong></div><div><RotateCcw /><span>Pending restarts</span><strong>{num(monitoring, 'pending_auto_restart')}</strong></div><div><ShieldCheck /><span>Auto-restart enabled</span><strong>{num(monitoring, 'auto_restart_enabled')}</strong></div><div><Activity /><span>Total crashes / restarts</span><strong>{num(monitoring, 'total_crashes')} / {num(monitoring, 'total_restarts')}</strong></div></div></section>
      </div>
      <section className="node-status-panel">
        <SectionHeader title="Node status" description="Managed server workload by node. CPU and RAM cover GameNode-managed server processes, not every host process." actions={canNodes ? <button type="button" className="quiet" onClick={onNodes}>Open nodes <ArrowRight /></button> : undefined} />
        <div className="node-status-grid">
          <article className="node-status-card node-status-card--local"><div className="node-status-card__head"><div><span className="eyebrow">This node</span><strong>Local GameNode</strong></div><span className="status running">Local</span></div><div className="node-status-card__metrics"><div><Cpu /><span>CPU</span><strong>{(localWorkload?.cpu_percent ?? 0).toFixed(1)}%</strong></div><div><MemoryStick /><span>RAM</span><strong>{readableBytes(localWorkload?.memory_bytes)}</strong></div></div><p>{running} running · {total} servers · {localWorkload?.sampled_servers ?? 0} sampled</p></article>
          {nodes.map(node => { const status = nodeStatuses[node.id]; const serverCounts = status?.servers; return <article className="node-status-card" key={node.id}><div className="node-status-card__head"><div><span className="eyebrow">Remote node</span><strong>{node.display_name}</strong></div><span className={`status ${node.enabled ? nodeTone(node.last_health) : 'stopped'}`}>{node.enabled ? nodeHealth(node.last_health) : 'Disabled'}</span></div>{status ? <><div className="node-status-card__metrics"><div><Cpu /><span>CPU</span><strong>{(status.workload.cpu_percent ?? 0).toFixed(1)}%</strong></div><div><MemoryStick /><span>RAM</span><strong>{readableBytes(status.workload.memory_bytes)}</strong></div></div><p>{num(serverCounts, 'running')} running · {num(serverCounts, 'total')} servers · {status.workload.sampled_servers ?? 0} sampled</p></> : <p className="hint">{node.enabled ? 'Workload details unavailable for your permissions or while the node is offline.' : 'This node is disabled.'}</p>}</article>; })}
        </div>
      </section>
      <SectionHeader title="Network inventory" description="Configured port assignments across visible servers" />
      <div className="dashboard-network"><MetricCard label="Configured ports" value={num(ports, 'total')} icon={Network} /><MetricCard label="TCP assignments" value={num(ports, 'tcp')} hint="Connection-oriented" /><MetricCard label="UDP assignments" value={num(ports, 'udp')} hint="Datagram traffic" /></div>
    </>}
    {data?.audit?.available && <section className="panel dashboard-audit"><SectionHeader title="Recent activity" description="Latest security and administrative events" actions={canAudit ? <button type="button" className="quiet" onClick={onAudit}>Open audit log <ArrowRight /></button> : undefined} />{recent.length === 0 ? <EmptyState compact title="No recent activity" description="New administrative actions will appear here." icon={Activity} /> : <div className="activity-list">{recent.map(event => <article key={event.id}><span className={`activity-marker activity-marker--${event.result}`} /><div><strong>{auditActionLabel(event.action)}</strong><p>{auditActor(event.actor_username, event.actor_user_id)} · {event.resource_type} {event.resource_name || ''}</p></div><div className="activity-meta"><span className={`status ${event.result === 'failure' ? 'crashed' : 'running'}`}>{event.result}</span><time>{new Date(event.timestamp).toLocaleString()}</time></div></article>)}</div>}</section>}
  </section>;
}
