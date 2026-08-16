export function listOrEmpty<T>(value: T[] | null | undefined): T[] {
  return Array.isArray(value) ? value : [];
}

export function userStatusLabel(enabled: boolean): 'Active' | 'Disabled' {
  return enabled ? 'Active' : 'Disabled';
}

export function validateUserForm(username: string, email: string): string[] {
  const errors: string[] = [];
  if (!/^[A-Za-z0-9][A-Za-z0-9_.-]{1,63}$/.test(username.trim())) errors.push('Username must be 2 to 64 ASCII letters, digits, dots, hyphens, or underscores.');
  const normalizedEmail = email.trim();
  if (!normalizedEmail.includes('@') || normalizedEmail.length > 254) errors.push('Enter a valid email address.');
  return errors;
}

export function validateGroupForm(name: string, description: string): string[] {
  const errors: string[] = [];
  if (!/^[A-Za-z0-9][A-Za-z0-9_. -]{1,63}$/.test(name.trim())) errors.push('Group name must be 2 to 64 ASCII letters, digits, spaces, dots, hyphens, or underscores.');
  if (description.trim().length > 512) errors.push('Description must be 512 characters or fewer.');
  return errors;
}

export function validatePasswordReset(password: string, confirmation: string, minimum = 8, maximum = 256): string[] {
  const errors: string[] = [];
  if (password.length < minimum || password.length > maximum) errors.push(`Password must be ${minimum} to ${maximum} characters.`);
  if (password !== confirmation) errors.push('Passwords do not match.');
  return errors;
}

export function filterMembershipCandidates<T extends { id: string; username: string; enabled: boolean }>(users: T[], memberIDs: ReadonlySet<string>, query: string): T[] {
  const needle = query.trim().toLocaleLowerCase();
  return users.filter(user => !memberIDs.has(user.id) && (!needle || user.username.toLocaleLowerCase().includes(needle)));
}

export function availableIdentityActions(capabilities: readonly string[] | undefined, kind: 'user' | 'group'): { view: boolean; manage: boolean } {
  const prefix = kind === 'user' ? 'Users' : 'Groups';
  return { view: capabilities?.includes(`${prefix}.View`) ?? false, manage: capabilities?.includes(`${prefix}.Manage`) ?? false };
}

export function permissionScopeLabel(allowedScopes: readonly string[]): string {
  const global = allowedScopes.includes('global');
  const tenant = allowedScopes.includes('tenant');
  const server = allowedScopes.includes('server');
  const parts = [global && 'Global', tenant && 'Tenant', server && 'Server'].filter(Boolean) as string[];
  return parts.length ? parts.join(' / ') : 'Not assignable';
}

/**
 * roleScopeSuitability generalizes the original server-only suitability
 * check to any of the three RBAC scopes ("tenant" | "server"; "global" is
 * always suitable for every non-empty role since every permission allows
 * global scope). Whole-role validation, matching the backend: one
 * unsuitable permission makes the whole role unsuitable at that scope, and
 * an empty role is never assignable at a narrower scope even though it can
 * still be assigned globally.
 */
export function roleScopeSuitability(selectedPermissions: Iterable<string>, catalog: readonly { key: string; allowed_scopes: readonly string[] }[], scope: 'tenant' | 'server'): { assignable: boolean; message: string; incompatible: string[] } {
  const selected = [...selectedPermissions];
  const scopeLabel = scope === 'tenant' ? 'a tenant' : 'a server';
  if (selected.length === 0) return { assignable: false, message: `This role has no permissions and cannot be assigned to ${scopeLabel}.`, incompatible: [] };
  const scopes = new Map(catalog.map(permission => [permission.key, permission.allowed_scopes]));
  const incompatible = selected.filter(key => !scopes.get(key)?.includes(scope));
  if (incompatible.length > 0) return { assignable: false, message: `This role cannot be assigned to ${scopeLabel} because it contains permissions that do not support ${scope} scope.`, incompatible };
  return { assignable: true, message: scope === 'tenant' ? 'Tenant assignable' : 'Server assignable', incompatible: [] };
}

/** @deprecated use roleScopeSuitability(selected, catalog, 'server') */
export function serverRoleSuitability(selectedPermissions: Iterable<string>, catalog: readonly { key: string; allowed_scopes: readonly string[] }[]): { assignable: boolean; message: string; incompatible: string[] } {
  return roleScopeSuitability(selectedPermissions, catalog, 'server');
}
