import { useEffect, useState } from 'react';

/** A deliberately small browser-history router for GameNode's single SPA.
 * It keeps URL handling local without introducing a second application shell
 * or changing any API paths. */
export type AppRoute =
  | { page: 'dashboard' }
  | { page: 'servers'; serverID?: string; mode?: 'new' | 'edit'; tab?: string }
  | { page: 'templates' }
  | { page: 'tenants'; tenantID?: string; tab?: string }
  | { page: 'nodes'; nodeID?: string }
  | { page: 'identity'; tab?: 'users' | 'groups' | 'roles' }
  | { page: 'audit' }
  | { page: 'settings' }
  | { page: 'logs' }
  | { page: 'status'; slug?: string }
  | { page: 'not-found' };

const changed = 'gamenode:navigation';

export function navigate(path: string, replace = false) {
  const target = path.startsWith('/') ? path : `/${path}`;
  window.history[replace ? 'replaceState' : 'pushState'](null, '', target);
  window.dispatchEvent(new Event(changed));
}

export function parseRoute(pathname = window.location.pathname): AppRoute {
  let parts: string[];
  try { parts = pathname.split('/').filter(Boolean).map(decodeURIComponent); } catch { return { page: 'not-found' }; }
  if (parts.length === 0 || (parts.length === 1 && parts[0] === 'dashboard')) return { page: 'dashboard' };
  if (parts[0] === 'server') {
    if (parts[1] === 'new' && parts.length === 2) return { page: 'servers', mode: 'new' };
    if (parts.length === 1) return { page: 'servers' };
    if (parts.length === 2) return { page: 'servers', serverID: parts[1] };
    if (parts.length === 3 && parts[2] === 'edit') return { page: 'servers', serverID: parts[1], mode: 'edit' };
    if (parts.length === 3) return { page: 'servers', serverID: parts[1], tab: parts[2] };
  }
  if (parts[0] === 'tenants') return parts.length === 1 ? { page: 'tenants' } : parts.length <= 3 ? { page: 'tenants', tenantID: parts[1], tab: parts[2] } : { page: 'not-found' };
  if (parts[0] === 'nodes') return parts.length === 1 ? { page: 'nodes' } : parts.length === 2 ? { page: 'nodes', nodeID: parts[1] } : { page: 'not-found' };
  if (parts[0] === 'status') return parts.length === 1 ? { page: 'status' } : parts.length === 2 ? { page: 'status', slug: parts[1] } : { page: 'not-found' };
  if (parts[0] === 'identity' && parts.length <= 2 && (!parts[1] || ['users', 'groups', 'roles'].includes(parts[1]))) return { page: 'identity', tab: parts[1] as 'users' | 'groups' | 'roles' | undefined };
  if (parts.length === 1 && ['templates', 'audit', 'settings', 'logs'].includes(parts[0])) return { page: parts[0] as 'templates' | 'audit' | 'settings' | 'logs' };
  return { page: 'not-found' };
}

export function useAppRoute(): AppRoute {
  const [route, setRoute] = useState(() => parseRoute());
  useEffect(() => {
    const update = () => setRoute(parseRoute());
    window.addEventListener('popstate', update);
    window.addEventListener(changed, update);
    return () => { window.removeEventListener('popstate', update); window.removeEventListener(changed, update); };
  }, []);
  return route;
}
