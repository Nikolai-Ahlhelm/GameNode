import { FormEvent, useEffect, useMemo, useState } from 'react';
import { Building2, ChevronRight, Plus } from 'lucide-react';
import { EmptyState, PageHeader, SectionHeader, SkeletonRows } from './ui';
import { hasCapability } from './capabilities';
import { filterMembershipCandidates, listOrEmpty, slugify, tenantCapabilities, validateTenantName, validateTenantSlug } from './tenants-helpers';
import { runtimeStateLabel, serverStateTone } from './server-status';
import { TenantAccess } from './identity';
import './tenants.css';

export type Tenant = { id: string; name: string; slug: string; status_page_enabled: boolean; status_page_public: boolean; created_at: string; updated_at: string };
type TenantUser = { id: string; username: string; enabled: boolean };
type Member = { tenant_id: string; user_id: string; username: string; created_at: string };
type ServerListItem = { server: { id: string; name: string; tenant_id: string; working_directory: string }; runtime: { current_state: string; console_detached?: boolean } };

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
const openServerFallback = (id: string) => { sessionStorage.setItem('gamenode:open-server', id); window.dispatchEvent(new Event('gamenode:open-server')); };

/** useCreatableTenants powers every "which tenant may I create a server in?"
 * decision in the product (Create Server, Game Library) from one shared,
 * Tenants.View-independent endpoint. See resolveTenantSelection for how the
 * result becomes a locked/open/disabled selector. */
export function useCreatableTenants(): { tenants: Tenant[]; loading: boolean } {
  const [tenants, setTenants] = useState<Tenant[]>([]);
  const [loading, setLoading] = useState(true);
  useEffect(() => { let cancelled = false; api<{ tenants: Tenant[] | null }>('/servers/creatable-tenants').then(result => { if (!cancelled) setTenants(listOrEmpty(result.tenants)); }).catch(() => { if (!cancelled) setTenants([]); }).finally(() => { if (!cancelled) setLoading(false); }); return () => { cancelled = true; }; }, []);
  return { tenants, loading };
}

export function TenantsPage({ token, capabilities, onOpenServer, openTenantID }: { token: string; capabilities?: string[]; onOpenServer?: (id: string) => void; openTenantID?: string }) {
  const rights = tenantCapabilities(capabilities);
  const [items, setItems] = useState<Tenant[]>([]);
  const [counts, setCounts] = useState<Record<string, { servers: number; members: number }>>({});
  const [selected, setSelected] = useState<Tenant>();
  // Lets a server's detail view deep-link straight into its own tenant
  // (see main.tsx's "View" button next to the Tenant field) without needing
  // its own routing: it just asks for a tenant by id and this page opens it.
  useEffect(() => { if (openTenantID && rights.view) void api<{ tenant: Tenant }>(`/tenants/${openTenantID}`).then(result => setSelected(result.tenant)).catch(() => {}); }, [openTenantID, rights.view]);
  const [creating, setCreating] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const load = async () => {
    setLoading(true); setError('');
    try {
      const list = listOrEmpty((await api<{ tenants: Tenant[] | null }>('/tenants')).tenants);
      setItems(list);
      const entries = await Promise.all(list.map(async tenant => {
        try {
          const [servers, members] = await Promise.all([api<{ servers: unknown[] | null }>(`/tenants/${tenant.id}/servers`), api<{ members: unknown[] | null }>(`/tenants/${tenant.id}/members`)]);
          return [tenant.id, { servers: listOrEmpty(servers.servers).length, members: listOrEmpty(members.members).length }] as const;
        } catch { return [tenant.id, undefined] as const; }
      }));
      setCounts(Object.fromEntries(entries.filter((entry): entry is [string, { servers: number; members: number }] => entry[1] !== undefined)));
    } catch (e) { setError(errorMessage(e)); } finally { setLoading(false); }
  };
  useEffect(() => { if (rights.view) void load(); else setLoading(false); }, [rights.view]);
  if (!rights.view) return <section><PageHeader eyebrow="Multi-tenancy" title="Tenants" description="Logically separate customer or organization boundaries." /><EmptyState icon={Building2} title="Tenants unavailable" description="Tenants.View is required to browse tenant entities." /></section>;
  if (selected) return <TenantDetail initial={selected} token={token} capabilities={capabilities} onBack={() => { setSelected(undefined); void load(); }} onDeleted={() => { setSelected(undefined); void load(); }} onOpenServer={onOpenServer} />;
  return <section className="tenants-page">
    <PageHeader eyebrow="Multi-tenancy" title="Tenants" description="Every server belongs to exactly one tenant. Tenant boundaries protect GameNode's API and application access; they are not a hostile native-process sandbox." actions={rights.manage ? <button onClick={() => setCreating(true)}><Plus />Create tenant</button> : undefined} />
    {error && <p className="error notice">{error}</p>}
    {creating && <CreateTenant token={token} onCancel={() => setCreating(false)} onCreated={tenant => { setCreating(false); setSelected(tenant); }} />}
    {loading ? <SkeletonRows count={5} label="Loading tenants" /> : items.length === 0 ? <EmptyState icon={Building2} title="No tenants yet" description="Create a tenant to logically separate customers or organizations on this GameNode." action={rights.manage ? <button onClick={() => setCreating(true)}>Create tenant</button> : undefined} /> : <div className="data-table tenants-table">
      <div className="table-head"><span>Tenant</span><span>Slug</span><span>Servers</span><span>Members</span><span>Actions</span></div>
      {items.map(tenant => <div className="table-row" key={tenant.id}>
        <strong>{tenant.name}</strong>
        <code>{tenant.slug}</code>
        <span>{counts[tenant.id]?.servers ?? '—'}</span>
        <span>{counts[tenant.id]?.members ?? '—'}</span>
        <span className="table-actions"><button className="quiet" onClick={() => setSelected(tenant)}>{rights.manage ? 'View / edit' : 'View'}</button></span>
      </div>)}
    </div>}
  </section>;
}

function CreateTenant({ token, onCancel, onCreated }: { token: string; onCancel: () => void; onCreated: (tenant: Tenant) => void }) {
  const [name, setName] = useState(''); const [slug, setSlug] = useState(''); const [editSlug, setEditSlug] = useState(false); const [error, setError] = useState(''); const [saving, setSaving] = useState(false);
  const preview = editSlug ? slug : slugify(name);
  async function submit(event: FormEvent) {
    event.preventDefault();
    const errors = [...validateTenantName(name), ...(editSlug ? validateTenantSlug(slug) : [])];
    if (errors.length) { setError(errors.join(' ')); return; }
    setSaving(true); setError('');
    try { onCreated((await api<{ tenant: Tenant }>('/tenants', { method: 'POST', headers: csrf(token), body: JSON.stringify({ name, ...(editSlug ? { slug } : {}) }) })).tenant); }
    catch (e) { setError(errorMessage(e)); } finally { setSaving(false); }
  }
  return <form className="panel form-panel identity-form" onSubmit={submit}>
    <SectionHeader title="Create tenant" description="A tenant is a logically separate customer or organization boundary. Its ID is generated once and is immutable." />
    <label>Name<input autoFocus value={name} onChange={e => setName(e.target.value)} required /></label>
    <label>Slug{!editSlug && <button type="button" className="quiet field-inline-action" onClick={() => { setSlug(preview); setEditSlug(true); }}>Customize</button>}<input value={editSlug ? slug : preview} disabled={!editSlug} placeholder="derived-from-name" onChange={e => setSlug(e.target.value)} /></label>
    <p className="field-help full-field">{editSlug ? 'The slug will be used exactly as entered.' : 'Automatically derived from the name. A display/URL convenience only - never a storage or security identifier.'}</p>
    {error && <p className="error notice">{error}</p>}
    <div className="actions"><button disabled={saving || !name.trim()}>{saving ? 'Creating…' : 'Create tenant'}</button><button type="button" className="quiet" onClick={onCancel} disabled={saving}>Cancel</button></div>
  </form>;
}

function TenantDetail({ initial, token, capabilities, onBack, onDeleted, onOpenServer }: { initial: Tenant; token: string; capabilities?: string[]; onBack: () => void; onDeleted: () => void; onOpenServer?: (id: string) => void }) {
  const rights = tenantCapabilities(capabilities); const usersView = hasCapability(capabilities, 'Users.View');
  const [tenant, setTenant] = useState(initial); const [tab, setTab] = useState<'overview' | 'servers' | 'members' | 'access'>('overview'); const [error, setError] = useState(''); const [notice, setNotice] = useState('');
  useEffect(() => { void api<{ tenant: Tenant }>(`/tenants/${initial.id}`).then(result => setTenant(result.tenant)).catch(e => setError(errorMessage(e))); }, [initial.id]);
  return <section className="identity-detail tenant-detail">
    <PageHeader eyebrow="Tenant" title={tenant.name} description={`Tenant · ${tenant.id}`} actions={<button className="quiet" onClick={onBack}>Back to tenants</button>} />
    {error && <p className="error notice">{error}</p>}{notice && <p className="success notice">{notice}</p>}
    <nav className="segmented-tabs" aria-label="Tenant detail sections">
      <button className={tab === 'overview' ? 'active' : ''} onClick={() => setTab('overview')}>Overview</button>
      <button className={tab === 'servers' ? 'active' : ''} onClick={() => setTab('servers')}>Servers</button>
      <button className={tab === 'members' ? 'active' : ''} onClick={() => setTab('members')}>Members</button>
      <button className={tab === 'access' ? 'active' : ''} onClick={() => setTab('access')}>Access</button>
    </nav>
    {tab === 'overview' && <TenantOverview tenant={tenant} token={token} manage={rights.manage} onSaved={t => { setTenant(t); setNotice('Tenant details saved.'); setError(''); }} onDeleted={onDeleted} onError={setError} />}
    {tab === 'servers' && <TenantServers tenantID={tenant.id} onOpenServer={onOpenServer ?? openServerFallback} />}
    {tab === 'members' && <TenantMembers tenantID={tenant.id} token={token} manage={rights.manage} usersView={usersView} />}
    {tab === 'access' && <TenantAccess tenantID={tenant.id} tenantName={tenant.name} token={token} capabilities={capabilities} />}
  </section>;
}

function TenantOverview({ tenant, token, manage, onSaved, onDeleted, onError }: { tenant: Tenant; token: string; manage: boolean; onSaved: (tenant: Tenant) => void; onDeleted: () => void; onError: (message: string) => void }) {
  const [form, setForm] = useState({ name: tenant.name, slug: tenant.slug, status_page_enabled: tenant.status_page_enabled, status_page_public: tenant.status_page_public }); const [editSlug, setEditSlug] = useState(false); const [saving, setSaving] = useState(false);
  const [counts, setCounts] = useState<{ servers: number; members: number }>();
  useEffect(() => { setForm({ name: tenant.name, slug: tenant.slug, status_page_enabled: tenant.status_page_enabled, status_page_public: tenant.status_page_public }); setEditSlug(false); }, [tenant.id, tenant.name, tenant.slug, tenant.status_page_enabled, tenant.status_page_public]);
  useEffect(() => { let cancelled = false; void Promise.all([api<{ servers: unknown[] | null }>(`/tenants/${tenant.id}/servers`), api<{ members: unknown[] | null }>(`/tenants/${tenant.id}/members`)]).then(([s, m]) => { if (!cancelled) setCounts({ servers: listOrEmpty(s.servers).length, members: listOrEmpty(m.members).length }); }).catch(() => { if (!cancelled) setCounts(undefined); }); return () => { cancelled = true; }; }, [tenant.id]);
  const dirty = form.name !== tenant.name || form.slug !== tenant.slug || form.status_page_enabled !== tenant.status_page_enabled || form.status_page_public !== tenant.status_page_public;
  async function save(event: FormEvent) {
    event.preventDefault();
    const errors = [...validateTenantName(form.name), ...validateTenantSlug(form.slug)];
    if (errors.length) { onError(errors.join(' ')); return; }
    setSaving(true); onError('');
    try { onSaved((await api<{ tenant: Tenant }>(`/tenants/${tenant.id}`, { method: 'PATCH', headers: csrf(token), body: JSON.stringify(form) })).tenant); }
    catch (e) { onError(errorMessage(e)); } finally { setSaving(false); }
  }
  async function remove() {
    if (!confirm(`Delete tenant ${tenant.name}? This only succeeds while it owns no servers, and never deletes server files.`)) return;
    try { await api(`/tenants/${tenant.id}`, { method: 'DELETE', headers: csrf(token) }); onDeleted(); } catch (e) { onError(errorMessage(e)); }
  }
  const hasServers = !!(counts && counts.servers > 0);
  return <>
    <form className="detail-card" onSubmit={save}>
      <SectionHeader title="General" description="Tenant identity. The ID is immutable, and renaming never moves managed server storage on disk." />
      <div className="form-grid">
        <label>Tenant ID<input value={tenant.id} readOnly /></label>
        <label>Created<input value={new Date(tenant.created_at).toLocaleString()} readOnly /></label>
        <label>Name<input disabled={!manage} value={form.name} onChange={e => setForm({ ...form, name: e.target.value })} /></label>
        <label>Slug{manage && !editSlug && <button type="button" className="quiet field-inline-action" onClick={() => setEditSlug(true)}>Edit</button>}<input disabled={!manage || !editSlug} value={form.slug} onChange={e => setForm({ ...form, slug: e.target.value })} /></label>
        <label><input type="checkbox" disabled={!manage} checked={form.status_page_enabled} onChange={e => setForm({ ...form, status_page_enabled: e.target.checked })} /> Enable status dashboard</label>
        <label><input type="checkbox" disabled={!manage || !form.status_page_enabled} checked={form.status_page_public} onChange={e => setForm({ ...form, status_page_public: e.target.checked })} /> Public without authentication</label>
      </div>
      <p className="field-help">Private status dashboards require tenant-scoped Monitoring.View. The dashboard exposes service names and operational state, but no process IDs, paths, metrics, or errors.</p>
      {tenant.status_page_enabled && <p><a href={`/status/${tenant.slug}`} target="_blank" rel="noreferrer">Open status dashboard</a></p>}
      {manage && <div className="actions"><button disabled={!dirty || saving}>{saving ? 'Saving…' : 'Save changes'}</button><button type="button" className="quiet" disabled={!dirty || saving} onClick={() => { setForm({ name: tenant.name, slug: tenant.slug, status_page_enabled: tenant.status_page_enabled, status_page_public: tenant.status_page_public }); setEditSlug(false); }}>Cancel changes</button></div>}
    </form>
    <section className="detail-card"><SectionHeader title="Summary" /><div className="definition-list"><div className="definition-row"><span>Servers</span><strong>{counts ? counts.servers : '—'}</strong></div><div className="definition-row"><span>Members</span><strong>{counts ? counts.members : '—'}</strong></div></div></section>
    {manage && <section className="detail-card danger-zone"><SectionHeader title="Danger zone" description="Deletion only succeeds while the tenant owns no servers. GameNode never deletes server files, and never recursively removes assignments or memberships as a side effect." /><div className="danger-actions"><div><strong>Delete this tenant</strong><p>{counts === undefined ? 'Loading current server ownership…' : hasServers ? `Remove or reassign all ${counts!.servers} server${counts!.servers === 1 ? '' : 's'} first.` : 'This tenant currently owns no servers.'}</p></div><button className="danger" disabled={hasServers || counts === undefined} onClick={() => void remove()}>Delete tenant</button></div></section>}
  </>;
}

function TenantServers({ tenantID, onOpenServer }: { tenantID: string; onOpenServer: (id: string) => void }) {
  // Reuses the ordinary, per-server RBAC-filtered GET /servers rather than
  // the Tenants.View-gated admin listing: this tab must show only the
  // servers the actor themselves may see (item 4), which for a tenant
  // operator without global Tenants.View may be a subset of the tenant's
  // full server roster.
  const [items, setItems] = useState<ServerListItem[]>(); const [error, setError] = useState('');
  useEffect(() => { let cancelled = false; api<{ servers: ServerListItem[] | null }>('/servers').then(result => { if (!cancelled) setItems(listOrEmpty(result.servers).filter(item => item.server.tenant_id === tenantID)); }).catch(e => { if (!cancelled) setError(errorMessage(e)); }); return () => { cancelled = true; }; }, [tenantID]);
  if (error) return <p className="error notice">{error}</p>;
  if (!items) return <SkeletonRows count={3} label="Loading servers" />;
  if (items.length === 0) return <EmptyState compact title="No visible servers in this tenant" description="Either this tenant owns no servers yet, or none are visible to your account." />;
  return <div className="server-list">{items.map(item => <button className="server-card" key={item.server.id} onClick={() => onOpenServer(item.server.id)}><span className="server-card__identity"><strong>{item.server.name}</strong><small>{item.server.working_directory}</small></span><span className={`status ${serverStateTone(item.runtime.current_state)}`}>{runtimeStateLabel(item.runtime.current_state, item.runtime.console_detached)}</span><ChevronRight className="server-card__arrow" /></button>)}</div>;
}

function TenantMembers({ tenantID, token, manage, usersView }: { tenantID: string; token: string; manage: boolean; usersView: boolean }) {
  const [members, setMembers] = useState<Member[]>(); const [users, setUsers] = useState<TenantUser[]>([]); const [query, setQuery] = useState(''); const [error, setError] = useState(''); const [notice, setNotice] = useState('');
  const load = async () => {
    try {
      const memberResult = await api<{ members: Member[] | null }>(`/tenants/${tenantID}/members`);
      setMembers(listOrEmpty(memberResult.members));
      if (usersView) setUsers(listOrEmpty((await api<{ users: TenantUser[] | null }>('/users')).users));
    } catch (e) { setError(errorMessage(e)); }
  };
  useEffect(() => { void load(); }, [tenantID, usersView]);
  const candidates = useMemo(() => filterMembershipCandidates(users, new Set((members ?? []).map(member => member.user_id)), query), [users, members, query]);
  async function add(userID: string) { if (!userID) return; try { await api(`/tenants/${tenantID}/members`, { method: 'POST', headers: csrf(token), body: JSON.stringify({ user_id: userID }) }); setQuery(''); setNotice('Member added.'); setError(''); await load(); } catch (e) { setError(errorMessage(e)); } }
  async function remove(member: Member) { if (!confirm(`Remove ${member.username || member.user_id} from this tenant?`)) return; try { await api(`/tenants/${tenantID}/members/${member.user_id}`, { method: 'DELETE', headers: csrf(token) }); setNotice('Member removed.'); setError(''); await load(); } catch (e) { setError(errorMessage(e)); } }
  return <section className="detail-card">
    <SectionHeader title="Members" description="Membership is a plain roster - it grants no permission by itself. Assign a tenant-scoped role in the Access tab to give a member actual rights." />
    {error && <p className="error notice">{error}</p>}{notice && <p className="success notice">{notice}</p>}
    {manage && usersView && <div className="membership-picker"><input aria-label="Search users" placeholder="Search users…" value={query} onChange={e => setQuery(e.target.value)} /><select aria-label="Add user" value="" onChange={e => void add(e.target.value)}><option value="">Add user…</option>{candidates.map(user => <option key={user.id} value={user.id}>{user.username}{user.enabled ? '' : ' (disabled)'}</option>)}</select></div>}
    {manage && !usersView && <p className="hint">Users.View is required to search for and add members.</p>}
    {!members ? <SkeletonRows count={3} label="Loading members" /> : members.length === 0 ? <EmptyState compact title="No members yet" description={manage && usersView ? 'Search for a user and add them to this tenant.' : 'This tenant has no members.'} /> : <div className="member-list">{members.map(member => <div key={member.user_id}><span><strong>{member.username || member.user_id}</strong><small>Member since {new Date(member.created_at).toLocaleDateString()}</small></span>{manage && <button className="danger quiet" onClick={() => void remove(member)}>Remove</button>}</div>)}</div>}
  </section>;
}

