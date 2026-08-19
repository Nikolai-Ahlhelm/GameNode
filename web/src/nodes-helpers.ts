export function listOrEmpty<T>(value: T[] | null | undefined): T[] {
  return Array.isArray(value) ? value : [];
}

export type RemoteNode = {
  id: string;
  node_id: string;
  display_name: string;
  endpoint: string;
  protocol_version: number;
  gamenode_version: string;
  os: string;
  arch: string;
  capabilities: string[] | null;
  enabled: boolean;
  trust_status: string;
  last_seen_at?: string;
  last_health: string;
  last_error_code?: string;
  created_at: string;
  updated_at: string;
  compatibility: string;
};

/**
 * nodeCapabilities reports what the current global capability list allows
 * for remote node administration. Node.Manage does not imply Node.View -
 * both are checked explicitly, matching every other View/Manage pair in the
 * product (see AGENTS.md's RBAC rules).
 */
export function nodeCapabilities(capabilities: readonly string[] | undefined): { view: boolean; manage: boolean } {
  return { view: capabilities?.includes('Node.View') ?? false, manage: capabilities?.includes('Node.Manage') ?? false };
}

/**
 * healthTone/healthLabel map the backend's Health enum
 * (internal/nodes.Health) to a display tone and label. "Node offline" is
 * deliberately distinct from "authentication failed" or "protocol
 * incompatible" - the operator should be able to tell the difference
 * without reading a raw error (see AGENTS.md item 19/21).
 */
export function healthLabel(health: string | undefined): string {
  switch (health) {
    case 'reachable':
      return 'Reachable';
    case 'unreachable':
      return 'Unreachable';
    case 'authentication_failed':
      return 'Authentication failed';
    case 'protocol_incompatible':
      return 'Protocol incompatible';
    case 'degraded':
      return 'Degraded';
    default:
      return 'Unknown';
  }
}

/**
 * healthTone reuses the exact status pill CSS classes web/src/styles.css
 * already defines for server state ("running"/"crashed"/"degraded"/
 * "stopped") rather than inventing a parallel color vocabulary.
 */
export function healthTone(health: string | undefined): 'running' | 'crashed' | 'degraded' | 'stopped' | 'unknown' {
  switch (health) {
    case 'reachable':
      return 'running';
    case 'degraded':
      return 'degraded';
    case 'unreachable':
    case 'authentication_failed':
    case 'protocol_incompatible':
      return 'crashed';
    default:
      return 'unknown';
  }
}

/**
 * compatibilityLabel/compatibilityTone map the backend-derived
 * `compatibility` field (never stored, always computed against this
 * controller's own protocol version - see internal/api/remotenodes.go) to a
 * display string. This is presentation only; the backend remains
 * authoritative for what a remote node actually supports.
 */
export function compatibilityLabel(compatibility: string | undefined): string {
  switch (compatibility) {
    case 'compatible':
      return 'Compatible';
    case 'limited_capabilities':
      return 'Limited capabilities';
    case 'incompatible':
      return 'Incompatible';
    default:
      return 'Unknown';
  }
}

export function compatibilityTone(compatibility: string | undefined): 'running' | 'degraded' | 'crashed' | 'unknown' {
  switch (compatibility) {
    case 'compatible':
      return 'running';
    case 'limited_capabilities':
      return 'degraded';
    case 'incompatible':
      return 'crashed';
    default:
      return 'unknown';
  }
}

/** formatCapability turns a raw capability identifier such as
 * "container_resource_limits" into a readable label. It never invents a
 * capability the backend did not advertise. */
export function formatCapability(capability: string): string {
  return capability
    .split('_')
    .map(part => part.length ? part[0].toUpperCase() + part.slice(1) : part)
    .join(' ');
}

export function validateEndpoint(endpoint: string): string[] {
  const trimmed = endpoint.trim();
  const errors: string[] = [];
  if (!trimmed) {
    errors.push('Endpoint is required.');
    return errors;
  }
  let url: URL;
  try {
    url = new URL(trimmed);
  } catch {
    errors.push('Endpoint must be a valid URL.');
    return errors;
  }
  if (url.protocol !== 'http:' && url.protocol !== 'https:') errors.push('Endpoint must use http or https.');
  if (url.username || url.password) errors.push('Endpoint must not contain a username or password.');
  if (url.pathname !== '' && url.pathname !== '/') errors.push('Endpoint must not contain a path.');
  if (url.search || url.hash) errors.push('Endpoint must not contain a query or fragment.');
  return errors;
}

export function validatePairingToken(token: string): string[] {
  return token.trim() ? [] : ['Pairing token is required.'];
}

export function relativeTime(iso: string | undefined): string {
  if (!iso) return 'Never';
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return 'Never';
  const seconds = Math.max(0, Math.floor((Date.now() - then) / 1000));
  if (seconds < 60) return 'Just now';
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  return `${days}d ago`;
}
