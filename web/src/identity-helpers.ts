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
  if (!/^[A-Za-z0-9][A-Za-z0-9_.-]{1,63}$/.test(name.trim())) errors.push('Group name must be 2 to 64 ASCII letters, digits, dots, hyphens, or underscores.');
  if (description.trim().length > 512) errors.push('Description must be 512 characters or fewer.');
  return errors;
}

export function validatePasswordReset(password: string, confirmation: string): string[] {
  const errors: string[] = [];
  if (password.length < 12 || password.length > 256) errors.push('Password must be 12 to 256 characters.');
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
