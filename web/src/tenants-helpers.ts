export function listOrEmpty<T>(value: T[] | null | undefined): T[] {
  return Array.isArray(value) ? value : [];
}

/**
 * slugify mirrors the backend's internal/tenants.slugify: lowercase ASCII,
 * hyphen-separated, non-ASCII/disallowed characters dropped rather than
 * transliterated. It is used only to preview the slug that Create Tenant
 * will derive when the slug field is left untouched - the backend remains
 * the authority and re-derives/validates it independently.
 */
export function slugify(name: string): string {
  let pendingHyphen = false;
  let out = '';
  for (const char of name.toLowerCase()) {
    if (/[a-z0-9]/.test(char)) {
      if (pendingHyphen && out.length > 0) out += '-';
      pendingHyphen = false;
      out += char;
    } else {
      pendingHyphen = true;
    }
  }
  return out;
}

export function validateTenantName(name: string): string[] {
  const trimmed = name.trim();
  const errors: string[] = [];
  if (trimmed.length < 2 || trimmed.length > 100 || !/^[A-Za-z0-9][A-Za-z0-9_. -]*$/.test(trimmed)) {
    errors.push('Tenant name must be 2 to 100 ASCII letters, digits, spaces, dots, hyphens, or underscores.');
  }
  return errors;
}

export function validateTenantSlug(slug: string): string[] {
  const trimmed = slug.trim().toLowerCase();
  const errors: string[] = [];
  if (trimmed.length < 2 || trimmed.length > 64 || !/^[a-z0-9]+(-[a-z0-9]+)*$/.test(trimmed)) {
    errors.push('Slug must be 2 to 64 lowercase letters, digits, or single internal hyphens.');
  }
  return errors;
}

export function filterMembershipCandidates<T extends { id: string; username: string; enabled: boolean }>(users: T[], memberIDs: ReadonlySet<string>, query: string): T[] {
  const needle = query.trim().toLocaleLowerCase();
  return users.filter(user => !memberIDs.has(user.id) && (!needle || user.username.toLocaleLowerCase().includes(needle)));
}

/**
 * tenantCapabilities reports what the current global capability list allows
 * for tenant entity administration. Tenants.Manage does not imply
 * Tenants.View - both are checked explicitly, matching every other
 * View/Manage pair in the product (see AGENTS.md's RBAC rules).
 */
export function tenantCapabilities(capabilities: readonly string[] | undefined): { view: boolean; manage: boolean } {
  return { view: capabilities?.includes('Tenants.View') ?? false, manage: capabilities?.includes('Tenants.Manage') ?? false };
}

export type TenantOption = { id: string; name: string };

/**
 * resolveTenantSelection decides how the Create Server / provisioning
 * tenant selector should behave from the set of tenants the current user
 * may actually create a server in (see GET /servers/creatable-tenants):
 * none available disables the create action entirely, exactly one tenant
 * locks the selector to it, and more than one leaves it open with nothing
 * preselected so the user makes an explicit, confirmed choice.
 */
export function resolveTenantSelection(options: readonly TenantOption[]): { canCreate: boolean; locked: boolean; preselected: string } {
  if (options.length === 0) return { canCreate: false, locked: false, preselected: '' };
  if (options.length === 1) return { canCreate: true, locked: true, preselected: options[0].id };
  return { canCreate: true, locked: false, preselected: '' };
}
