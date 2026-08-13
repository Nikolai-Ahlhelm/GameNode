import { useEffect, useState } from 'react';
import { Activity, ArrowLeft, ArrowRight, Search } from 'lucide-react';
import { auditActionLabel, auditActor, auditMetadata, auditResource, auditTimestamp, type AuditEvent } from './audit-helpers';
import { EmptyState, PageHeader } from './ui';
import './audit.css';

export { auditActionLabel, auditActor } from './audit-helpers';

const pageSize = 100;
const resourceTypes = ['', 'auth', 'server', 'console', 'file', 'port', 'user', 'group', 'role', 'settings', 'system', 'template'];

function AuditDetails({ item }: { item: AuditEvent }) {
  const timestamp = auditTimestamp(item.timestamp);
  return <details className="audit-detail"><summary>View event details</summary><div className="audit-detail-grid">
    <div><span>Event ID</span><code>{item.id}</code></div>
    <div><span>Exact time</span><code>{timestamp.iso || 'Unknown'}</code></div>
    <div><span>Actor</span><strong>{auditActor(item.actor_username, item.actor_user_id)}</strong></div>
    <div><span>Actor ID</span><code>{item.actor_user_id || 'Not authenticated'}</code></div>
    <div><span>Source IP</span><code>{item.remote_ip || 'Local / unavailable'}</code></div>
    <div><span>Action</span><code>{item.action}</code></div>
    <div><span>Resource type</span><code>{item.resource_type}</code></div>
    <div><span>Resource</span><strong>{auditResource(item)}</strong></div>
    <div><span>Resource ID</span><code>{item.resource_id || '—'}</code></div>
    <div><span>Server ID</span><code>{item.server_id || '—'}</code></div>
    <div><span>Result</span><strong>{item.result}</strong></div>
    <div><span>Error code</span><code>{item.error_code || '—'}</code></div>
  </div>{item.error_summary && <p className="error audit-error"><strong>Failure:</strong> {item.error_summary}</p>}{item.metadata !== undefined && <div className="audit-metadata"><span>Controlled metadata</span><pre>{auditMetadata(item.metadata)}</pre></div>}</details>;
}

export function AuditLog() {
  const [items, setItems] = useState<AuditEvent[]>([]);
  const [offset, setOffset] = useState(0);
  const [query, setQuery] = useState('');
  const [resourceType, setResourceType] = useState('');
  const [result, setResult] = useState('');
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    const controller = new AbortController();
    const parameters = new URLSearchParams({ limit: String(pageSize), offset: String(offset) });
    if (query.trim()) parameters.set('query', query.trim());
    if (resourceType) parameters.set('resource_type', resourceType);
    if (result) parameters.set('result', result);
    setLoading(true);
    fetch(`/api/v1/audit?${parameters}`, { credentials: 'same-origin', signal: controller.signal })
      .then(async response => { if (!response.ok) throw new Error((await response.json().catch(() => null))?.error?.message ?? 'Request failed'); return response.json(); })
      .then(value => { setItems(value.items ?? []); setError(''); })
      .catch(reason => { if (reason.name !== 'AbortError') setError(reason.message); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [offset, query, resourceType, result]);

  const resetPage = () => setOffset(0);
  return <section className="audit-page"><PageHeader eyebrow="Security" title="Audit log" description="Trace who performed an action, what it affected, when it happened, and whether it succeeded." /><section className="panel audit-panel">
    <div className="audit-filters"><label><span>Search events</span><span className="input-with-icon"><Search /><input value={query} maxLength={100} onChange={event => { resetPage(); setQuery(event.target.value); }} placeholder="Actor, action, resource, error…" /></span></label><label><span>Resource</span><select value={resourceType} onChange={event => { resetPage(); setResourceType(event.target.value); }}>{resourceTypes.map(type => <option key={type || 'all'} value={type}>{type ? type.charAt(0).toUpperCase() + type.slice(1) : 'All resources'}</option>)}</select></label><label><span>Result</span><select value={result} onChange={event => { resetPage(); setResult(event.target.value); }}><option value="">All results</option><option value="success">Success</option><option value="failure">Failure</option></select></label></div>
    {error && <p className="error notice">{error}</p>}
    {loading ? <p className="muted audit-loading">Loading audit events…</p> : !error && items.length === 0 ? <EmptyState title="No audit events found" description={query || result || resourceType ? 'Try adjusting the current filters.' : 'Security and administrative activity will appear here.'} icon={Activity} /> : <div className="data-table audit-table"><div className="table-head"><span>Event</span><span>Actor</span><span>Resource</span><span>Time</span><span>Result</span></div>{items.map(item => { const timestamp = auditTimestamp(item.timestamp); return <article className="table-row" key={item.id}><div><strong>{auditActionLabel(item.action)}</strong><small>{item.action}</small>{item.error_summary && <small className="audit-row-error">{item.error_summary}</small>}</div><div><strong>{auditActor(item.actor_username, item.actor_user_id)}</strong><small>{item.remote_ip || 'Local / unavailable'}</small></div><div><strong>{item.resource_type}</strong><small>{auditResource(item)}</small></div><time dateTime={timestamp.iso} title={timestamp.iso}>{timestamp.display}</time><span className={`status ${item.result === 'failure' ? 'crashed' : 'running'}`}>{item.result}</span><AuditDetails item={item} /></article>; })}</div>}
    <footer className="pagination"><span>Showing {items.length ? offset + 1 : 0}–{offset + items.length}</span><div><button className="quiet" disabled={offset === 0 || loading} onClick={() => setOffset(Math.max(0, offset - pageSize))}><ArrowLeft />Previous</button><button className="quiet" disabled={items.length < pageSize || loading} onClick={() => setOffset(offset + pageSize)}>Next<ArrowRight /></button></div></footer>
  </section></section>;
}
