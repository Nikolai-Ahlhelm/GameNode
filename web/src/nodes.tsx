import { FormEvent, useEffect, useState } from 'react';
import { KeyRound, Network, Plus } from 'lucide-react';
import { EmptyState, LoadingState, PageHeader, SectionHeader } from './ui';
import { compatibilityLabel, compatibilityTone, formatCapability, healthLabel, healthTone, listOrEmpty, nodeCapabilities, relativeTime, RemoteNode, validateEndpoint, validatePairingToken } from './nodes-helpers';
import './tenants.css';
import './nodes.css';

async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`/api/v1${path}`, { credentials: 'same-origin', headers: { 'Content-Type': 'application/json', ...(init?.headers ?? {}) }, ...init });
  if (!response.ok) {
    const body = await response.json().catch(() => null);
    throw new Error(body?.error?.message ?? (response.status === 403 ? 'You do not have permission to perform this action.' : 'Request failed.'));
  }
  return response.status === 204 ? undefined as T : response.json();
}
const csrf = (token: string) => ({ 'X-CSRF-Token': token });
const errorMessage = (error: unknown) => error instanceof Error ? error.message : 'Request failed.';

export function NodesPage({ token, capabilities }: { token: string; capabilities?: string[] }) {
  const rights = nodeCapabilities(capabilities);
  const [items, setItems] = useState<RemoteNode[]>([]);
  const [selected, setSelected] = useState<RemoteNode>();
  const [enrolling, setEnrolling] = useState(false);
  const [pairing, setPairing] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const load = async () => {
    setLoading(true); setError('');
    try { setItems(listOrEmpty((await api<{ remote_nodes: RemoteNode[] | null }>('/remote-nodes')).remote_nodes)); }
    catch (e) { setError(errorMessage(e)); } finally { setLoading(false); }
  };
  useEffect(() => { if (rights.view) void load(); else setLoading(false); }, [rights.view]);
  if (!rights.view) return <section><PageHeader eyebrow="Remote management" title="Nodes" description="Other GameNode installations enrolled with this controller." /><EmptyState icon={Network} title="Remote nodes unavailable" description="Node.View is required to browse enrolled remote nodes." /></section>;
  if (selected) return <NodeDetail initial={selected} token={token} manage={rights.manage} onBack={() => { setSelected(undefined); void load(); }} onRemoved={() => { setSelected(undefined); void load(); }} />;
  return <section className="tenants-page">
    <PageHeader eyebrow="Remote management" title="Nodes" description="Every GameNode installation remains autonomous. Enrolling a remote node only lets this controller read its identity, health, and capabilities - it never takes over its database or local server lifecycle." actions={rights.manage ? <div className="page-actions"><button className="quiet" onClick={() => setPairing(true)}><KeyRound />Pairing token for this node</button><button onClick={() => setEnrolling(true)}><Plus />Enroll remote node</button></div> : undefined} />
    {error && <p className="error notice">{error}</p>}
    {pairing && <PairingTokenPanel token={token} onClose={() => setPairing(false)} />}
    {enrolling && <EnrollNode token={token} onCancel={() => setEnrolling(false)} onEnrolled={node => { setEnrolling(false); setSelected(node); }} />}
    {loading ? <LoadingState label="Loading remote nodes" /> : items.length === 0 ? <EmptyState icon={Network} title="No remote nodes enrolled" description="Enroll another GameNode installation to view its identity, health, and capabilities from here." action={rights.manage ? <button onClick={() => setEnrolling(true)}>Enroll remote node</button> : undefined} /> : <div className="data-table nodes-table">
      <div className="table-head"><span>Node</span><span>Endpoint</span><span>Health</span><span>Last seen</span><span>Version</span><span>Actions</span></div>
      {items.map(node => <div className="table-row" key={node.id}>
        <strong>{node.display_name}<br /><small>{node.node_id.slice(0, 12)}…</small></strong>
        <code>{node.endpoint}</code>
        <span className={`status ${healthTone(node.last_health)}`}>{healthLabel(node.last_health)}</span>
        <span>{relativeTime(node.last_seen_at)}</span>
        <span>{node.gamenode_version || 'Unknown'} · {node.os}/{node.arch}</span>
        <span className="table-actions"><button className="quiet" onClick={() => setSelected(node)}>{rights.manage ? 'View / manage' : 'View'}</button></span>
      </div>)}
    </div>}
  </section>;
}

function PairingTokenPanel({ token, onClose }: { token: string; onClose: () => void }) {
  const [pairingToken, setPairingToken] = useState('');
  const [expiresAt, setExpiresAt] = useState('');
  const [error, setError] = useState('');
  const [generating, setGenerating] = useState(false);
  async function generate() {
    setGenerating(true); setError('');
    try {
      const result = await api<{ pairing_token: string; expires_at: string }>('/node/pairing-tokens', { method: 'POST', headers: csrf(token), body: '{}' });
      setPairingToken(result.pairing_token); setExpiresAt(result.expires_at);
    } catch (e) { setError(errorMessage(e)); } finally { setGenerating(false); }
  }
  return <div className="panel form-panel identity-form">
    <SectionHeader title="Pairing token for this node" description="Generate a single-use, time-bounded secret another controller can use to enroll THIS installation. It is shown only once and is never retrievable again." />
    {error && <p className="error notice">{error}</p>}
    {!pairingToken ? <div className="actions"><button disabled={generating} onClick={() => void generate()}>{generating ? 'Generating…' : 'Generate pairing token'}</button><button type="button" className="quiet" onClick={onClose}>Close</button></div> : <>
      <label>Pairing token (copy now - it will not be shown again)<input readOnly value={pairingToken} onFocus={e => e.currentTarget.select()} /></label>
      <p className="field-help full-field">Expires {new Date(expiresAt).toLocaleString()}. Paste it into the enrolling controller's "Enroll remote node" form along with this node's endpoint URL.</p>
      <div className="actions"><button type="button" className="quiet" onClick={onClose}>Done</button></div>
    </>}
  </div>;
}

function EnrollNode({ token, onCancel, onEnrolled }: { token: string; onCancel: () => void; onEnrolled: (node: RemoteNode) => void }) {
  const [endpoint, setEndpoint] = useState(''); const [pairingToken, setPairingToken] = useState(''); const [displayName, setDisplayName] = useState('');
  const [error, setError] = useState(''); const [saving, setSaving] = useState(false);
  async function submit(event: FormEvent) {
    event.preventDefault();
    const errors = [...validateEndpoint(endpoint), ...validatePairingToken(pairingToken)];
    if (errors.length) { setError(errors.join(' ')); return; }
    setSaving(true); setError('');
    try {
      const result = await api<{ remote_node: RemoteNode }>('/remote-nodes', { method: 'POST', headers: csrf(token), body: JSON.stringify({ endpoint: endpoint.trim(), pairing_token: pairingToken.trim(), display_name: displayName.trim() }) });
      onEnrolled(result.remote_node);
    } catch (e) { setError(errorMessage(e)); } finally { setSaving(false); }
  }
  return <form className="panel form-panel identity-form" onSubmit={submit}>
    <SectionHeader title="Enroll remote node" description="Requires a pairing token generated on the remote node itself. Enrollment never grants this controller access to the remote node's database or runtime - only its authenticated Node API." />
    <label>Endpoint<input autoFocus placeholder="https://remote-node.example:8443" value={endpoint} onChange={e => setEndpoint(e.target.value)} required /></label>
    <label>Pairing token<input value={pairingToken} onChange={e => setPairingToken(e.target.value)} required /></label>
    <label>Display name (optional)<input placeholder="Defaults to the remote node's own name" value={displayName} onChange={e => setDisplayName(e.target.value)} /></label>
    {error && <p className="error notice">{error}</p>}
    <div className="actions"><button disabled={saving}>{saving ? 'Enrolling…' : 'Enroll'}</button><button type="button" className="quiet" onClick={onCancel} disabled={saving}>Cancel</button></div>
  </form>;
}

function NodeDetail({ initial, token, manage, onBack, onRemoved }: { initial: RemoteNode; token: string; manage: boolean; onBack: () => void; onRemoved: () => void }) {
  const [node, setNode] = useState(initial); const [error, setError] = useState(''); const [notice, setNotice] = useState('');
  const [name, setName] = useState(initial.display_name); const [saving, setSaving] = useState(false); const [refreshing, setRefreshing] = useState(false);
  useEffect(() => { void api<{ remote_node: RemoteNode }>(`/remote-nodes/${initial.id}`).then(r => setNode(r.remote_node)).catch(e => setError(errorMessage(e))); }, [initial.id]);
  async function rename() {
    setSaving(true); setError('');
    try { const r = await api<{ remote_node: RemoteNode }>(`/remote-nodes/${node.id}`, { method: 'PATCH', headers: csrf(token), body: JSON.stringify({ display_name: name }) }); setNode(r.remote_node); setNotice('Display name saved.'); }
    catch (e) { setError(errorMessage(e)); } finally { setSaving(false); }
  }
  async function toggleEnabled() {
    setSaving(true); setError('');
    try { const r = await api<{ remote_node: RemoteNode }>(`/remote-nodes/${node.id}`, { method: 'PATCH', headers: csrf(token), body: JSON.stringify({ enabled: !node.enabled }) }); setNode(r.remote_node); setNotice(r.remote_node.enabled ? 'Node enabled.' : 'Node disabled.'); }
    catch (e) { setError(errorMessage(e)); } finally { setSaving(false); }
  }
  async function refresh() {
    setRefreshing(true); setError('');
    try { const r = await api<{ remote_node: RemoteNode }>(`/remote-nodes/${node.id}/refresh`, { method: 'POST', headers: csrf(token) }); setNode(r.remote_node); setNotice('Status refreshed.'); }
    catch (e) { setError(errorMessage(e)); } finally { setRefreshing(false); }
  }
  async function remove() {
    if (!confirm(`Remove ${node.display_name} from this controller's registry? The remote node itself, its data, and its local servers are never affected.`)) return;
    try { await api(`/remote-nodes/${node.id}`, { method: 'DELETE', headers: csrf(token) }); onRemoved(); } catch (e) { setError(errorMessage(e)); }
  }
  return <section className="identity-detail tenant-detail">
    <PageHeader eyebrow="Remote node" title={node.display_name} description={`Node · ${node.node_id}`} actions={<div className="page-actions"><button className="quiet" disabled={refreshing} onClick={() => void refresh()}>{refreshing ? 'Refreshing…' : 'Refresh status'}</button><button className="quiet" onClick={onBack}>Back to nodes</button></div>} />
    {error && <p className="error notice">{error}</p>}{notice && <p className="success notice">{notice}</p>}
    <section className="detail-card">
      <SectionHeader title="Identity" description="This controller never takes ownership of the remote node's database or runtime state - only its authenticated Node API." />
      <div className="definition-list">
        <div className="definition-row"><span>Node ID</span><code>{node.node_id}</code></div>
        <div className="definition-row"><span>Endpoint</span><code>{node.endpoint}</code></div>
        <div className="definition-row"><span>GameNode version</span><strong>{node.gamenode_version || 'Unknown'}</strong></div>
        <div className="definition-row"><span>Platform</span><strong>{node.os || 'Unknown'} / {node.arch || 'Unknown'}</strong></div>
        <div className="definition-row"><span>Protocol version</span><strong>{node.protocol_version || 'Unknown'}</strong></div>
        <div className="definition-row"><span>Compatibility</span><strong className={`status ${compatibilityTone(node.compatibility)}`}>{compatibilityLabel(node.compatibility)}</strong></div>
        <div className="definition-row"><span>Health</span><strong className={`status ${healthTone(node.last_health)}`}>{healthLabel(node.last_health)}</strong></div>
        <div className="definition-row"><span>Last contact</span><strong>{relativeTime(node.last_seen_at)}</strong></div>
        <div className="definition-row"><span>Trust status</span><strong>{node.trust_status}</strong></div>
      </div>
    </section>
    <section className="detail-card">
      <SectionHeader title="Capabilities" description="What this remote node reported it supports. A capability not listed here is not assumed available." />
      {listOrEmpty(node.capabilities).length === 0 ? <p className="hint">No capabilities reported yet.</p> : <ul className="capability-list">{listOrEmpty(node.capabilities).map(c => <li key={c}>{formatCapability(c)}</li>)}</ul>}
    </section>
    {manage && <section className="detail-card">
      <SectionHeader title="Manage" />
      <div className="form-grid">
        <label>Display name<input value={name} onChange={e => setName(e.target.value)} /></label>
      </div>
      <div className="actions">
        <button disabled={saving || name.trim() === node.display_name} onClick={() => void rename()}>Save name</button>
        <button className="quiet" disabled={saving} onClick={() => void toggleEnabled()}>{node.enabled ? 'Disable' : 'Enable'}</button>
      </div>
    </section>}
    {manage && <section className="detail-card danger-zone">
      <SectionHeader title="Danger zone" description="Removing a node only deletes this controller's registry entry. It never deletes the remote node, its data, or its running servers - which remain fully autonomous (see AGENTS.md)." />
      <div className="danger-actions"><div><strong>Remove this node</strong><p>This controller will stop tracking its status until re-enrolled.</p></div><button className="danger" onClick={() => void remove()}>Remove node</button></div>
    </section>}
  </section>;
}
