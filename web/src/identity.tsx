import { FormEvent, useEffect, useMemo, useState } from 'react';
import { EmptyState, LoadingState, PageHeader, SectionHeader } from './ui';
import { hasCapability } from './capabilities';
import {
  availableIdentityActions,
  filterMembershipCandidates,
  listOrEmpty,
  permissionScopeLabel,
  serverRoleSuitability,
  userStatusLabel,
  validateGroupForm,
  validatePasswordReset,
  validateUserForm,
} from './identity-helpers';
import './identity.css';

type User = { id: string; username: string; display_name: string; email: string; enabled: boolean; is_admin: boolean; group_count?: number };
type Group = { id: string; name: string; description: string; member_count?: number; assignment_count?: number };
type Server = { id: string; name: string };
type Role = { id: string; name: string; description: string; permissions?: string[]; server_assignable: boolean };
type Permission = { key: string; category: string; description: string; allowed_scopes: string[] };
type Assignment = { id: string; role_id: string; role_name: string; scope: { scope_type: string; scope_id?: string } };

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

export function IdentityAdmin({ token, capabilities, isAdmin, initialTab }: { token: string; capabilities?: string[]; isAdmin: boolean; initialTab?: 'users' | 'groups' | 'roles' }) {
	const [passwordPolicy, setPasswordPolicy] = useState({ password_minimum_length: 8, password_maximum_length: 256 });
	useEffect(() => { void api<{ password_policy: typeof passwordPolicy }>('/setup/status').then(value => setPasswordPolicy(value.password_policy)); }, []);
  const visible = {
    users: hasCapability(capabilities, 'Users.View'),
    groups: hasCapability(capabilities, 'Groups.View'),
    roles: hasCapability(capabilities, 'Roles.View'),
  };
  const initial = initialTab && visible[initialTab] ? initialTab : visible.users ? 'users' : visible.groups ? 'groups' : 'roles';
  const [tab, setTab] = useState<'users' | 'groups' | 'roles'>(initial);
  return <section className="identity-page">
    <PageHeader eyebrow="Access control" title="Users, groups & roles" description="Manage local identities, memberships, and explicit role assignments." />
    <nav className="segmented-tabs" aria-label="Identity administration">
      {visible.users && <button className={tab === 'users' ? 'active' : ''} onClick={() => setTab('users')}>Users</button>}
      {visible.groups && <button className={tab === 'groups' ? 'active' : ''} onClick={() => setTab('groups')}>Groups</button>}
      {visible.roles && <button className={tab === 'roles' ? 'active' : ''} onClick={() => setTab('roles')}>Roles & access</button>}
    </nav>
    {tab === 'users' ? <Users token={token} capabilities={capabilities} isAdmin={isAdmin} passwordPolicy={passwordPolicy} /> : tab === 'groups' ? <Groups token={token} capabilities={capabilities} /> : <Roles token={token} capabilities={capabilities} />}
  </section>;
}

export function ServerAccess({ serverID, token, capabilities, onOpenRoles }: { serverID: string; token: string; capabilities?: string[]; onOpenRoles?: () => void }) {
  type ServerAssignment = { id: string; role_name: string; subject_type: 'user' | 'group'; subject_id: string; subject_name: string };
  const [items, setItems] = useState<ServerAssignment[]>([]);
  const [users, setUsers] = useState<User[]>([]);
  const [groups, setGroups] = useState<Group[]>([]);
  const [roles, setRoles] = useState<Role[]>([]);
  const [kind, setKind] = useState<'user' | 'group'>('group');
  const [subject, setSubject] = useState('');
  const [role, setRole] = useState('');
  const [error, setError] = useState('');
  const view = hasCapability(capabilities, 'Roles.View');
  const manage = hasCapability(capabilities, 'Roles.Manage');
  const load = async () => {
    try {
      const access = await api<{ assignments: ServerAssignment[] | null }>(`/servers/${serverID}/access`);
      setItems(listOrEmpty(access.assignments));
      if (manage) {
        const [userResult, groupResult, roleResult] = await Promise.all([
          api<{ users: User[] | null }>('/users'),
          api<{ groups: Group[] | null }>('/groups'),
          api<{ roles: Role[] | null }>('/roles'),
        ]);
        setUsers(listOrEmpty(userResult.users));
        setGroups(listOrEmpty(groupResult.groups));
        setRoles(listOrEmpty(roleResult.roles));
      }
      setError('');
    } catch (e) { setError(errorMessage(e)); }
  };
  useEffect(() => { if (view) void load(); }, [serverID, view, manage]);
  async function add(event: FormEvent) {
    event.preventDefault();
    try {
      await api(`/${kind === 'user' ? 'users' : 'groups'}/${subject}/roles`, { method: 'POST', headers: csrf(token), body: JSON.stringify({ role_id: role, scope_type: 'server', scope_id: serverID }) });
      setSubject(''); setRole(''); await load();
    } catch (e) { setError(errorMessage(e)); }
  }
  async function remove(item: ServerAssignment) {
    if (!confirm(`Remove ${item.role_name} from ${item.subject_name}?`)) return;
    try { await api(`/${item.subject_type === 'user' ? 'users' : 'groups'}/${item.subject_id}/roles/${item.id}`, { method: 'DELETE', headers: csrf(token) }); await load(); } catch (e) { setError(errorMessage(e)); }
  }
  if (!view) return null;
  const subjects = kind === 'group' ? groups : users;
  const serverRoles = roles.filter(item => item.server_assignable);
  const excludedRoles = roles.length - serverRoles.length;
  return <section className="subpanel"><SectionHeader title="Who has access to this server?" description="Server-scoped user and group role assignments." />{error && <p className="error notice">{error}</p>}{items.length === 0 ? <EmptyState compact title="No access assignments" description="Assign a server-capable role to a user or group below." /> : <div className="assignment-list">{items.map(item => <div key={item.id}><span>{item.subject_type === 'group' ? 'Group' : 'User'}: {item.subject_name}</span><strong>{item.role_name}</strong>{manage && <button className="danger quiet" onClick={() => void remove(item)}>Remove</button>}</div>)}</div>}{manage && (serverRoles.length === 0 ? <EmptyState compact title="No server roles available" description="Create a role containing server-scoped permissions such as Server.View, Server.Start, Console.View, or Files.View." action={onOpenRoles ? <button onClick={onOpenRoles}>Open Roles</button> : undefined} /> : <form className="form-grid assignment-form" onSubmit={add}><label>Subject type<select aria-label="Subject type" value={kind} onChange={e => { setKind(e.target.value as 'user' | 'group'); setSubject(''); }}><option value="group">Group</option><option value="user">User</option></select></label><label>{kind === 'group' ? 'Group' : 'User'}<select aria-label={kind === 'group' ? 'Group' : 'User'} value={subject} onChange={e => setSubject(e.target.value)} required><option value="">Choose {kind}…</option>{subjects.map(item => <option key={item.id} value={item.id}>{'username' in item ? item.username : item.name}</option>)}</select></label><label>Role<select aria-label="Server role" value={role} onChange={e => setRole(e.target.value)} required><option value="">Choose role…</option>{serverRoles.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label><button disabled={!subject || !role}>Assign role</button>{excludedRoles > 0 && <p className="field-help full-field">{excludedRoles} {excludedRoles === 1 ? 'role is' : 'roles are'} hidden because empty roles and roles containing global-only permissions cannot be assigned to a server.</p>}</form>)}</section>;
}

function Users({ token, capabilities, isAdmin, passwordPolicy }: { token: string; capabilities?: string[]; isAdmin: boolean; passwordPolicy: { password_minimum_length: number; password_maximum_length: number } }) {
  const rights = availableIdentityActions(capabilities, 'user');
  const [items, setItems] = useState<User[]>([]);
  const [selected, setSelected] = useState<{ user: User; section?: 'security' }>();
  const [creating, setCreating] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const load = async () => { setLoading(true); setError(''); try { setItems(listOrEmpty((await api<{ users: User[] | null }>('/users')).users)); } catch (e) { setError(errorMessage(e)); } finally { setLoading(false); } };
  useEffect(() => { if (rights.view) void load(); }, [rights.view]);
  async function toggle(user: User) {
    if (!confirm(`${user.enabled ? 'Disable' : 'Enable'} ${user.username}?${user.enabled ? ' Existing sessions will be invalidated.' : ''}`)) return;
    try { await api(`/users/${user.id}`, { method: 'PATCH', headers: csrf(token), body: JSON.stringify({ enabled: !user.enabled }) }); setNotice(`${user.username} is now ${user.enabled ? 'disabled' : 'active'}.`); await load(); } catch (e) { setError(errorMessage(e)); }
  }
  async function remove(user: User) {
    if (!confirm(`Delete user ${user.username}? This action cannot be undone.`)) return;
    try { await api(`/users/${user.id}`, { method: 'DELETE', headers: csrf(token) }); setNotice(`${user.username} was deleted.`); await load(); } catch (e) { setError(errorMessage(e)); }
  }
  if (selected) return <UserDetail initial={selected.user} initialSection={selected.section} token={token} capabilities={capabilities} isAdmin={isAdmin} passwordPolicy={passwordPolicy} onBack={() => { setSelected(undefined); void load(); }} />;
  return <>
    <SectionHeader title="Local users" description="Local accounts, status, memberships, and administrative actions." actions={rights.manage ? <button onClick={() => setCreating(true)}>Create user</button> : undefined} />
    {error && <p className="error notice">{error}</p>}{notice && <p className="success notice">{notice}</p>}
    {creating && <CreateUser token={token} isAdmin={isAdmin} passwordPolicy={passwordPolicy} onCancel={() => setCreating(false)} onCreated={user => { setCreating(false); setSelected({ user }); }} />}
    {loading ? <LoadingState label="Loading users" /> : items.length === 0 ? <EmptyState title="No users found" description="Create a local user to delegate access." action={rights.manage ? <button onClick={() => setCreating(true)}>Create user</button> : undefined} /> : <div className="data-table identity-table users-table">
      <div className="table-head"><span>User</span><span>Status</span><span>Groups</span><span>Actions</span></div>
      {items.map(user => <div className="table-row" key={user.id}>
        <span><strong>{user.username}</strong><small>{user.email}</small></span>
        <span className="badge-stack"><span className={`status ${user.enabled ? 'success' : 'danger'}`}>{userStatusLabel(user.enabled)}</span>{user.is_admin && <span className="status info">Administrator</span>}</span>
        <span>{user.group_count ?? '—'}</span>
        <span className="table-actions"><button className="quiet" onClick={() => setSelected({ user })}>{rights.manage ? 'View / edit' : 'View'}</button>{rights.manage && <><button className="quiet" onClick={() => void toggle(user)}>{user.enabled ? 'Disable' : 'Enable'}</button><button className="quiet" onClick={() => setSelected({ user, section: 'security' })}>Reset password</button><button className="danger quiet" onClick={() => void remove(user)}>Delete</button></>}</span>
      </div>)}
    </div>}
  </>;
}

function CreateUser({ token, isAdmin, passwordPolicy, onCancel, onCreated }: { token: string; isAdmin: boolean; passwordPolicy: { password_minimum_length: number; password_maximum_length: number }; onCancel: () => void; onCreated: (user: User) => void }) {
  const [form, setForm] = useState({ username: '', display_name: '', email: '', password: '', confirmation: '', is_admin: false });
  const [error, setError] = useState(''); const [saving, setSaving] = useState(false);
  async function submit(event: FormEvent) {
    event.preventDefault(); const errors = [...validateUserForm(form.username, form.email), ...validatePasswordReset(form.password, form.confirmation, passwordPolicy.password_minimum_length, passwordPolicy.password_maximum_length)];
    if (errors.length) { setError(errors.join(' ')); return; }
    setSaving(true); setError('');
    try { const result = await api<{ user: User }>('/users', { method: 'POST', headers: csrf(token), body: JSON.stringify({ username: form.username, display_name: form.display_name, email: form.email, password: form.password, is_admin: isAdmin && form.is_admin }) }); setForm(old => ({ ...old, password: '', confirmation: '' })); onCreated(result.user); } catch (e) { setError(errorMessage(e)); } finally { setSaving(false); }
  }
  return <form className="panel form-panel identity-form" onSubmit={submit}><SectionHeader title="Create user" description="The password is set once and is never displayed again." /><label>Username<input autoFocus value={form.username} onChange={e => setForm({ ...form, username: e.target.value })} required /></label><label>Display name<input value={form.display_name} onChange={e => setForm({ ...form, display_name: e.target.value })} /></label><label>Email<input type="email" value={form.email} onChange={e => setForm({ ...form, email: e.target.value })} required /></label>{isAdmin && <label className="checkbox-field"><input type="checkbox" checked={form.is_admin} onChange={e => setForm({ ...form, is_admin: e.target.checked })} /> Administrator</label>}<label>Initial password<input type="password" autoComplete="new-password" minLength={passwordPolicy.password_minimum_length} maxLength={passwordPolicy.password_maximum_length} value={form.password} onChange={e => setForm({ ...form, password: e.target.value })} required /></label><label>Confirm password<input type="password" autoComplete="new-password" minLength={passwordPolicy.password_minimum_length} maxLength={passwordPolicy.password_maximum_length} value={form.confirmation} onChange={e => setForm({ ...form, confirmation: e.target.value })} required /></label><p className="field-help">{passwordPolicy.password_minimum_length}–{passwordPolicy.password_maximum_length} characters.</p>{error && <p className="error notice">{error}</p>}<div className="actions"><button disabled={saving}>{saving ? 'Creating…' : 'Create user'}</button><button type="button" className="quiet" onClick={onCancel} disabled={saving}>Cancel</button></div></form>;
}

function UserDetail({ initial, initialSection, token, capabilities, isAdmin, passwordPolicy, onBack }: { initial: User; initialSection?: 'security'; token: string; capabilities?: string[]; isAdmin: boolean; passwordPolicy: { password_minimum_length: number; password_maximum_length: number }; onBack: () => void }) {
  const manage = hasCapability(capabilities, 'Users.Manage'); const groupsView = hasCapability(capabilities, 'Groups.View'); const groupsManage = hasCapability(capabilities, 'Groups.Manage'); const rolesView = hasCapability(capabilities, 'Roles.View');
  const [user, setUser] = useState(initial); const [form, setForm] = useState(initial); const [memberships, setMemberships] = useState<Group[]>([]); const [groups, setGroups] = useState<Group[]>([]); const [error, setError] = useState(''); const [notice, setNotice] = useState(''); const [saving, setSaving] = useState(false);
  const dirty = form.username !== user.username || form.display_name !== user.display_name || form.email !== user.email || form.is_admin !== user.is_admin;
  const load = async () => { try { const fresh = (await api<{ user: User }>(`/users/${initial.id}`)).user; setUser(fresh); setForm(fresh); if (groupsView) { const [owned, all] = await Promise.all([api<{ groups: Group[] | null }>(`/users/${initial.id}/groups`), api<{ groups: Group[] | null }>('/groups')]); setMemberships(listOrEmpty(owned.groups)); setGroups(listOrEmpty(all.groups)); } } catch (e) { setError(errorMessage(e)); } };
  useEffect(() => { void load(); }, [initial.id, groupsView]);
  async function save(event: FormEvent) { event.preventDefault(); const errors = validateUserForm(form.username, form.email); if (errors.length) { setError(errors.join(' ')); return; } setSaving(true); setError(''); try { const updated = (await api<{ user: User }>(`/users/${user.id}`, { method: 'PATCH', headers: csrf(token), body: JSON.stringify({ username: form.username, display_name: form.display_name, email: form.email, ...(isAdmin ? { is_admin: form.is_admin } : {}) }) })).user; setUser(updated); setForm(updated); setNotice('User details saved.'); } catch (e) { setError(errorMessage(e)); } finally { setSaving(false); } }
  async function addGroup(groupID: string) { if (!groupID) return; try { await api(`/groups/${groupID}/members`, { method: 'POST', headers: csrf(token), body: JSON.stringify({ user_id: user.id }) }); setNotice('Group membership added.'); await load(); } catch (e) { setError(errorMessage(e)); } }
  async function removeGroup(group: Group) { if (!confirm(`Remove ${user.username} from ${group.name}?`)) return; try { await api(`/groups/${group.id}/members/${user.id}`, { method: 'DELETE', headers: csrf(token) }); setNotice('Group membership removed.'); await load(); } catch (e) { setError(errorMessage(e)); } }
  async function toggle() { if (!confirm(`${user.enabled ? 'Disable' : 'Enable'} ${user.username}?${user.enabled ? ' Existing sessions will be invalidated.' : ''}`)) return; try { await api(`/users/${user.id}`, { method: 'PATCH', headers: csrf(token), body: JSON.stringify({ enabled: !user.enabled }) }); setNotice(`${user.username} is now ${user.enabled ? 'disabled' : 'active'}.`); await load(); } catch (e) { setError(errorMessage(e)); } }
  async function remove() { if (!confirm(`Delete user ${user.username}? This action cannot be undone.`)) return; try { await api(`/users/${user.id}`, { method: 'DELETE', headers: csrf(token) }); onBack(); } catch (e) { setError(errorMessage(e)); } }
  const availableGroups = groups.filter(group => !memberships.some(member => member.id === group.id));
  return <section className="identity-detail">
    <PageHeader eyebrow="User" title={user.username} description={`Local user · ${user.id}`} actions={<div className="actions"><span className={`status ${user.enabled ? 'success' : 'danger'}`}>{userStatusLabel(user.enabled)}</span>{user.is_admin && <span className="status info">Administrator</span>}<button className="quiet" onClick={onBack}>Back to users</button></div>} />
    {error && <p className="error notice">{error}</p>}{notice && <p className="success notice">{notice}</p>}
    <form className="detail-card identity-general" onSubmit={save}><SectionHeader title="General" description="Account identity and contact details." /><div className="form-grid"><label>User ID<input value={user.id} readOnly /></label><label>Username<input disabled={!manage} value={form.username} onChange={e => setForm({ ...form, username: e.target.value })} /></label><label>Display name<input disabled={!manage} value={form.display_name} onChange={e => setForm({ ...form, display_name: e.target.value })} /></label><label>Email<input disabled={!manage} type="email" value={form.email} onChange={e => setForm({ ...form, email: e.target.value })} /></label>{isAdmin && manage && <label className="checkbox-field"><input type="checkbox" checked={form.is_admin} onChange={e => setForm({ ...form, is_admin: e.target.checked })} /> Administrator</label>}</div>{manage && <div className="actions"><button disabled={!dirty || saving}>{saving ? 'Saving…' : 'Save changes'}</button><button type="button" className="quiet" disabled={!dirty || saving} onClick={() => setForm(user)}>Cancel changes</button></div>}</form>
    {groupsView && <section className="detail-card"><SectionHeader title="Groups" description="Memberships inherited by this user." actions={groupsManage && availableGroups.length ? <select aria-label="Add to group" defaultValue="" onChange={e => { void addGroup(e.target.value); e.target.value = ''; }}><option value="">Add to group…</option>{availableGroups.map(group => <option key={group.id} value={group.id}>{group.name}</option>)}</select> : undefined} />{memberships.length === 0 ? <EmptyState compact title="This user is not a member of any groups" description={groupsManage ? 'Add the user to a group to manage access at scale.' : 'No group memberships are configured.'} /> : <div className="member-list">{memberships.map(group => <div key={group.id}><span><strong>{group.name}</strong><small>{group.description || 'No description'}</small></span>{groupsManage && <button className="danger quiet" onClick={() => void removeGroup(group)}>Remove</button>}</div>)}</div>}</section>}
    {rolesView && <AssignmentPanel subject={user} kind="user" token={token} capabilities={capabilities} />}
    {manage && <PasswordReset user={user} token={token} autoFocus={initialSection === 'security'} passwordPolicy={passwordPolicy} />}
    {manage && <section className="detail-card danger-zone"><SectionHeader title="Danger zone" description="Status changes invalidate sessions. Deletion is permanent and remains subject to backend safety rules." /><div className="danger-actions"><div><strong>{user.enabled ? 'Disable this user' : 'Enable this user'}</strong><p>{user.enabled ? 'The user will be signed out and cannot log in.' : 'The user can log in again with the current password.'}</p></div><button className={user.enabled ? 'danger' : ''} onClick={() => void toggle()}>{user.enabled ? 'Disable user' : 'Enable user'}</button><div><strong>Delete this user</strong><p>Removes the account according to backend relationship rules.</p></div><button className="danger" onClick={() => void remove()}>Delete user</button></div></section>}
  </section>;
}

function PasswordReset({ user, token, autoFocus, passwordPolicy }: { user: User; token: string; autoFocus: boolean; passwordPolicy: { password_minimum_length: number; password_maximum_length: number } }) {
  const [password, setPassword] = useState(''); const [confirmation, setConfirmation] = useState(''); const [error, setError] = useState(''); const [notice, setNotice] = useState(''); const [saving, setSaving] = useState(false);
  async function submit(event: FormEvent) { event.preventDefault(); const errors = validatePasswordReset(password, confirmation, passwordPolicy.password_minimum_length, passwordPolicy.password_maximum_length); if (errors.length) { setError(errors.join(' ')); return; } setSaving(true); setError(''); setNotice(''); try { await api(`/users/${user.id}/password`, { method: 'POST', headers: csrf(token), body: JSON.stringify({ password }) }); setPassword(''); setConfirmation(''); setNotice('Password reset. Existing sessions were invalidated.'); } catch (e) { setError(errorMessage(e)); } finally { setSaving(false); } }
  return <form className="detail-card password-card" onSubmit={submit}><SectionHeader title={`Reset password for ${user.username}`} description="Set a new password without revealing the existing password. All sessions for this user will be invalidated." /><div className="form-grid"><label>New password<input autoFocus={autoFocus} type="password" autoComplete="new-password" minLength={passwordPolicy.password_minimum_length} maxLength={passwordPolicy.password_maximum_length} value={password} onChange={e => setPassword(e.target.value)} /></label><label>Confirm password<input type="password" autoComplete="new-password" minLength={passwordPolicy.password_minimum_length} maxLength={passwordPolicy.password_maximum_length} value={confirmation} onChange={e => setConfirmation(e.target.value)} /></label></div><p className="field-help">Password requirement: {passwordPolicy.password_minimum_length}–{passwordPolicy.password_maximum_length} characters.</p>{error && <p className="error notice">{error}</p>}{notice && <p className="success notice">{notice}</p>}<button disabled={saving || !password}>{saving ? 'Resetting…' : 'Reset password'}</button></form>;
}

function Groups({ token, capabilities }: { token: string; capabilities?: string[] }) {
  const rights = availableIdentityActions(capabilities, 'group'); const rolesView = hasCapability(capabilities, 'Roles.View'); const [items, setItems] = useState<Group[]>([]); const [selected, setSelected] = useState<Group>(); const [creating, setCreating] = useState(false); const [loading, setLoading] = useState(true); const [error, setError] = useState('');
  const load = async () => { setLoading(true); setError(''); try { setItems(listOrEmpty((await api<{ groups: Group[] | null }>('/groups')).groups)); } catch (e) { setError(errorMessage(e)); } finally { setLoading(false); } };
  useEffect(() => { if (rights.view) void load(); }, [rights.view]);
  if (selected) return <GroupDetail initial={selected} token={token} capabilities={capabilities} onBack={() => { setSelected(undefined); void load(); }} />;
  return <><SectionHeader title="Groups" description="Organize users and assign access at group scope." actions={rights.manage ? <button onClick={() => setCreating(true)}>Create group</button> : undefined} />{error && <p className="error notice">{error}</p>}{creating && <GroupForm token={token} onCancel={() => setCreating(false)} onSaved={group => setSelected(group)} />}{loading ? <LoadingState label="Loading groups" /> : items.length === 0 ? <EmptyState title="No groups yet" description="Create a group to organize users and scoped access." action={rights.manage ? <button onClick={() => setCreating(true)}>Create group</button> : undefined} /> : <div className={`data-table identity-table groups-table${rolesView ? ' groups-table--assignments' : ''}`}><div className="table-head"><span>Group</span><span>Description</span><span>Members</span>{rolesView && <span>Assignments</span>}<span>Actions</span></div>{items.map(group => <div className="table-row" key={group.id}><strong>{group.name}</strong><span>{group.description || '—'}</span><span>{group.member_count ?? '—'}</span>{rolesView && <span>{group.assignment_count ?? '—'}</span>}<span className="table-actions"><button className="quiet" onClick={() => setSelected(group)}>{rights.manage ? 'View / edit' : 'View'}</button></span></div>)}</div>}</>;
}

function GroupForm({ token, onCancel, onSaved }: { token: string; onCancel: () => void; onSaved: (group: Group) => void }) {
  const [name, setName] = useState(''); const [description, setDescription] = useState(''); const [error, setError] = useState(''); const [saving, setSaving] = useState(false);
  async function submit(event: FormEvent) { event.preventDefault(); const errors = validateGroupForm(name, description); if (errors.length) { setError(errors.join(' ')); return; } setSaving(true); setError(''); try { onSaved((await api<{ group: Group }>('/groups', { method: 'POST', headers: csrf(token), body: JSON.stringify({ name, description }) })).group); } catch (e) { setError(errorMessage(e)); } finally { setSaving(false); } }
  return <form className="panel form-panel identity-form" onSubmit={submit}><SectionHeader title="Create group" description="Group names are unique and can be changed later." /><label>Name<input autoFocus value={name} onChange={e => setName(e.target.value)} required /></label><label>Description<textarea value={description} onChange={e => setDescription(e.target.value)} maxLength={512} /></label>{error && <p className="error notice">{error}</p>}<div className="actions"><button disabled={saving}>{saving ? 'Creating…' : 'Create group'}</button><button type="button" className="quiet" onClick={onCancel}>Cancel</button></div></form>;
}

function GroupDetail({ initial, token, capabilities, onBack }: { initial: Group; token: string; capabilities?: string[]; onBack: () => void }) {
  const manage = hasCapability(capabilities, 'Groups.Manage'); const usersView = hasCapability(capabilities, 'Users.View'); const rolesView = hasCapability(capabilities, 'Roles.View'); const [group, setGroup] = useState(initial); const [form, setForm] = useState(initial); const [members, setMembers] = useState<User[]>([]); const [users, setUsers] = useState<User[]>([]); const [query, setQuery] = useState(''); const [error, setError] = useState(''); const [notice, setNotice] = useState(''); const [saving, setSaving] = useState(false);
  const dirty = form.name !== group.name || form.description !== group.description;
  const load = async () => { try { const fresh = (await api<{ group: Group }>(`/groups/${initial.id}`)).group; setGroup(fresh); setForm(fresh); const memberResult = await api<{ users: User[] | null }>(`/groups/${initial.id}/members`); setMembers(listOrEmpty(memberResult.users)); if (usersView) setUsers(listOrEmpty((await api<{ users: User[] | null }>('/users')).users)); } catch (e) { setError(errorMessage(e)); } };
  useEffect(() => { void load(); }, [initial.id, usersView]);
  const candidates = useMemo(() => filterMembershipCandidates(users, new Set(members.map(member => member.id)), query), [users, members, query]);
  async function save(event: FormEvent) { event.preventDefault(); const errors = validateGroupForm(form.name, form.description); if (errors.length) { setError(errors.join(' ')); return; } setSaving(true); setError(''); try { const updated = (await api<{ group: Group }>(`/groups/${group.id}`, { method: 'PATCH', headers: csrf(token), body: JSON.stringify({ name: form.name, description: form.description }) })).group; setGroup(updated); setForm(updated); setNotice('Group details saved.'); } catch (e) { setError(errorMessage(e)); } finally { setSaving(false); } }
  async function add(userID: string) { if (!userID) return; try { await api(`/groups/${group.id}/members`, { method: 'POST', headers: csrf(token), body: JSON.stringify({ user_id: userID }) }); setQuery(''); setNotice('Member added.'); await load(); } catch (e) { setError(errorMessage(e)); } }
  async function removeMember(user: User) { if (!confirm(`Remove ${user.username} from ${group.name}?`)) return; try { await api(`/groups/${group.id}/members/${user.id}`, { method: 'DELETE', headers: csrf(token) }); setNotice('Member removed.'); await load(); } catch (e) { setError(errorMessage(e)); } }
  async function remove() { if (!confirm(`Delete group ${group.name}? This action cannot be undone.`)) return; try { await api(`/groups/${group.id}`, { method: 'DELETE', headers: csrf(token) }); onBack(); } catch (e) { setError(errorMessage(e)); } }
  return <section className="identity-detail"><PageHeader eyebrow="Group" title={group.name} description={`Local group · ${group.id}`} actions={<button className="quiet" onClick={onBack}>Back to groups</button>} />{error && <p className="error notice">{error}</p>}{notice && <p className="success notice">{notice}</p>}
    <form className="detail-card" onSubmit={save}><SectionHeader title="General" description="Group identity and purpose." /><div className="form-grid"><label>Group ID<input value={group.id} readOnly /></label><label>Name<input disabled={!manage} value={form.name} onChange={e => setForm({ ...form, name: e.target.value })} /></label><label className="full-field">Description<textarea disabled={!manage} maxLength={512} value={form.description} onChange={e => setForm({ ...form, description: e.target.value })} /></label></div>{manage && <div className="actions"><button disabled={!dirty || saving}>{saving ? 'Saving…' : 'Save changes'}</button><button type="button" className="quiet" disabled={!dirty || saving} onClick={() => setForm(group)}>Cancel changes</button></div>}</form>
    <section className="detail-card"><SectionHeader title="Members" description="Users currently in this group." />{manage && usersView && <div className="membership-picker"><input aria-label="Search users" placeholder="Search users…" value={query} onChange={e => setQuery(e.target.value)} /><select aria-label="Add user" value="" onChange={e => void add(e.target.value)}><option value="">Add user…</option>{candidates.map(user => <option key={user.id} value={user.id}>{user.username}{user.enabled ? '' : ' (disabled)'}</option>)}</select></div>}{members.length === 0 ? <EmptyState compact title="No members yet" description={manage && usersView ? 'Search for a user and add them to this group.' : 'This group has no members.'} /> : <div className="member-list">{members.map(user => <div key={user.id}><span><strong>{user.username}</strong><small>{user.email}</small></span><span className={`status ${user.enabled ? 'success' : 'danger'}`}>{userStatusLabel(user.enabled)}</span>{manage && <button className="danger quiet" onClick={() => void removeMember(user)}>Remove</button>}</div>)}</div>}</section>
    {rolesView && <AssignmentPanel subject={group} kind="group" token={token} capabilities={capabilities} />}
    {manage && <section className="detail-card danger-zone"><SectionHeader title="Danger zone" description="Deleting a group is permanent. Membership and assignment handling remains backend-authoritative." /><div className="danger-actions"><div><strong>Delete this group</strong><p>Confirm the exact group before deletion.</p></div><button className="danger" onClick={() => void remove()}>Delete group</button></div></section>}
  </section>;
}

function AssignmentPanel({ subject, kind, token, capabilities }: { subject: User | Group; kind: 'user' | 'group'; token: string; capabilities?: string[] }) {
  const manage = hasCapability(capabilities, 'Roles.Manage'); const [assignments, setAssignments] = useState<Assignment[]>([]); const [roles, setRoles] = useState<Role[]>([]); const [servers, setServers] = useState<Server[]>([]); const [scope, setScope] = useState<'global' | 'server'>('global'); const [server, setServer] = useState(''); const [role, setRole] = useState(''); const [error, setError] = useState('');
  const route = `/${kind === 'user' ? 'users' : 'groups'}/${subject.id}`;
  const load = async () => { try { const [a, r] = await Promise.all([api<{ assignments: Assignment[] | null }>(`${route}/roles`), api<{ roles: Role[] | null }>('/roles')]); setAssignments(listOrEmpty(a.assignments)); setRoles(listOrEmpty(r.roles)); try { const response = await api<{ servers: Array<Server | { server: Server }> | null }>('/servers'); setServers(listOrEmpty(response.servers).map(item => 'server' in item ? item.server : item)); } catch { setServers([]); } } catch (e) { setError(errorMessage(e)); } };
  useEffect(() => { void load(); }, [subject.id, kind]);
  async function add(event: FormEvent) { event.preventDefault(); try { await api(`${route}/roles`, { method: 'POST', headers: csrf(token), body: JSON.stringify({ role_id: role, scope_type: scope, ...(scope === 'server' ? { scope_id: server } : {}) }) }); setRole(''); setServer(''); await load(); } catch (e) { setError(errorMessage(e)); } }
  async function remove(assignment: Assignment) { if (!confirm(`Remove ${assignment.role_name} assignment?`)) return; try { await api(`${route}/roles/${assignment.id}`, { method: 'DELETE', headers: csrf(token) }); await load(); } catch (e) { setError(errorMessage(e)); } }
  const usableRoles = scope === 'server' ? roles.filter(item => item.server_assignable) : roles;
  const scopeLabel = (assignment: Assignment) => assignment.scope.scope_type === 'global' ? 'Global' : servers.find(item => item.id === assignment.scope.scope_id)?.name ?? 'Server';
  return <section className="detail-card"><SectionHeader title="Access" description="Roles are reusable permission sets; the assignment controls whether they apply globally or to one server." />{error && <p className="error notice">{error}</p>}{assignments.length === 0 ? <EmptyState compact title="No access assignments" description="No roles are assigned directly to this subject." /> : <div className="assignment-list">{assignments.map(assignment => <div key={assignment.id}><span>{scopeLabel(assignment)}</span><strong>{assignment.role_name}</strong>{manage && <button className="danger quiet" onClick={() => void remove(assignment)}>Remove</button>}</div>)}</div>}{manage && <form className="form-grid assignment-form" onSubmit={add}><label>Scope<select value={scope} onChange={e => { setScope(e.target.value as 'global' | 'server'); setServer(''); setRole(''); }}><option value="global">Global — applies everywhere</option><option value="server">One server</option></select></label>{scope === 'server' && <label>Server<select value={server} onChange={e => setServer(e.target.value)} required><option value="">Choose server…</option>{servers.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>}<label>Role<select value={role} onChange={e => setRole(e.target.value)} required disabled={usableRoles.length === 0}><option value="">{usableRoles.length === 0 ? 'No compatible roles' : 'Choose role…'}</option>{usableRoles.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label><button disabled={!role || (scope === 'server' && !server)}>Assign role</button>{scope === 'server' && usableRoles.length === 0 && <p className="notice notice--warning full-field">No server-assignable roles exist. Create a role with Server, Console, Files, Ports, or Monitoring permissions and no global-only permissions.</p>}</form>}</section>;
}

function Roles({ token, capabilities }: { token: string; capabilities?: string[] }) {
  const manage = hasCapability(capabilities, 'Roles.Manage'); const [roles, setRoles] = useState<Role[]>([]); const [selected, setSelected] = useState<Role>(); const [creating, setCreating] = useState(false); const [error, setError] = useState('');
  const load = async () => { try { setRoles(listOrEmpty((await api<{ roles: Role[] | null }>('/roles')).roles)); } catch (e) { setError(errorMessage(e)); } };
  useEffect(() => { void load(); }, []);
  if (selected) return <RoleEditor role={selected} token={token} manage={manage} onClose={() => { setSelected(undefined); void load(); }} />;
  return <><SectionHeader title="Roles" description="Reusable permission sets. Assignment scope determines where a role applies; Manage never implies View." actions={manage ? <button onClick={() => setCreating(true)}>Create role</button> : undefined} />{error && <p className="error notice">{error}</p>}{creating && <CreateRole token={token} onCancel={() => setCreating(false)} onCreated={role => { setCreating(false); setSelected(role); }} />}{roles.length === 0 ? <EmptyState title="No custom roles yet" description="Create a role to define reusable permission sets." action={manage ? <button onClick={() => setCreating(true)}>Create role</button> : undefined} /> : <div className="data-table role-table"><div className="table-head"><span>Role</span><span>Scope suitability</span><span>Description</span><span>Actions</span></div>{roles.map(item => <div className="table-row" key={item.id}><strong>{item.name}</strong><span className={`status ${item.server_assignable ? 'success' : 'info'}`}>{item.server_assignable ? 'Server assignable' : 'Global assignment only'}</span><span>{item.description || '—'}</span><span><button className="quiet" onClick={() => setSelected(item)}>Open</button></span></div>)}</div>}</>;
}

function CreateRole({ token, onCancel, onCreated }: { token: string; onCancel: () => void; onCreated: (role: Role) => void }) {
  const [name, setName] = useState(''); const [description, setDescription] = useState(''); const [error, setError] = useState('');
  async function submit(event: FormEvent) { event.preventDefault(); try { const result = await api<{ role: Role }>('/roles', { method: 'POST', headers: csrf(token), body: JSON.stringify({ name, description }) }); onCreated(result.role); } catch (e) { setError(errorMessage(e)); } }
  return <form className="panel form-panel identity-form" onSubmit={submit}><SectionHeader title="Create role" description="After creation, choose explicit permissions and review whether the role is server assignable." /><label>Name<input autoFocus value={name} onChange={e => setName(e.target.value)} required /></label><label>Description<input value={description} onChange={e => setDescription(e.target.value)} /></label>{error && <p className="error notice">{error}</p>}<div className="actions"><button>Create and choose permissions</button><button type="button" className="quiet" onClick={onCancel}>Cancel</button></div></form>;
}

function RoleEditor({ role, token, manage, onClose }: { role: Role; token: string; manage: boolean; onClose: () => void }) {
  const [value, setValue] = useState(role); const [catalog, setCatalog] = useState<Permission[]>([]); const [selected, setSelected] = useState<Set<string>>(new Set()); const [error, setError] = useState('');
  useEffect(() => { void Promise.all([api<{ permissions: Permission[] }>('/permissions'), api<{ permissions: string[] }>(`/roles/${role.id}/permissions`)]).then(([c, p]) => { setCatalog(c.permissions); setSelected(new Set(p.permissions)); }).catch(e => setError(errorMessage(e))); }, [role.id]);
  const grouped = useMemo(() => Object.entries(catalog.reduce<Record<string, Permission[]>>((result, permission) => { (result[permission.category] ??= []).push(permission); return result; }, {})), [catalog]);
  const suitability = useMemo(() => serverRoleSuitability(selected, catalog), [selected, catalog]);
  async function save() { try { await api(`/roles/${role.id}`, { method: 'PATCH', headers: csrf(token), body: JSON.stringify({ name: value.name, description: value.description }) }); await api(`/roles/${role.id}/permissions`, { method: 'PUT', headers: csrf(token), body: JSON.stringify({ permissions: [...selected] }) }); onClose(); } catch (e) { setError(errorMessage(e)); } }
  async function remove() { if (!confirm(`Delete role ${role.name}?`)) return; try { await api(`/roles/${role.id}`, { method: 'DELETE', headers: csrf(token) }); onClose(); } catch (e) { setError(errorMessage(e)); } }
  return <section><SectionHeader title={role.name} description="Permissions are explicit. Selecting Manage never selects View." actions={<div className="actions"><button className="quiet" onClick={onClose}>Back</button>{manage && <><button onClick={() => void save()}>Save role</button><button className="danger quiet" onClick={() => void remove()}>Delete</button></>}</div>} /><div className="panel role-editor"><label>Name<input disabled={!manage} value={value.name} onChange={e => setValue({ ...value, name: e.target.value })} /></label><label>Description<input disabled={!manage} value={value.description} onChange={e => setValue({ ...value, description: e.target.value })} /></label><div className={`notice ${suitability.assignable ? 'notice--success' : 'notice--warning'}`}><strong>{suitability.message}</strong>{suitability.incompatible.length > 0 && <><br /><span>Global-only: {suitability.incompatible.join(', ')}</span></>}</div><p className="muted">{selected.size} permissions selected</p>{grouped.map(([category, permissions]) => <section className="permission-group" key={category}><h3>{category}</h3>{permissions.map(permission => <label className="permission" key={permission.key}><input disabled={!manage} type="checkbox" checked={selected.has(permission.key)} onChange={e => setSelected(old => { const next = new Set(old); e.target.checked ? next.add(permission.key) : next.delete(permission.key); return next; })} /><span><strong>{permission.key.split('.')[1]}</strong><small>{permission.description} · {permissionScopeLabel(permission.allowed_scopes)}</small></span></label>)}</section>)}</div>{error && <p className="error notice">{error}</p>}</section>;
}
