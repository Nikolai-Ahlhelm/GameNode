export type Branding = { name: string; subtitle: string; custom_favicon: boolean };

export const defaultBranding: Branding = { name: 'GameNode', subtitle: 'Infrastructure manager', custom_favicon: false };

export function applyBranding(branding: Branding, refreshFavicon = false): void {
  document.title = branding.name;
  document.querySelectorAll('.brand-copy strong').forEach(element => { element.textContent = branding.name; });
  document.querySelectorAll('.brand-copy small').forEach(element => { element.textContent = branding.subtitle; });
  const existing = document.querySelector<HTMLLinkElement>('link[data-gamenode-favicon]');
  if (!branding.custom_favicon) { existing?.remove(); return; }
  const link = existing ?? document.head.appendChild(document.createElement('link'));
  link.rel = 'icon'; link.dataset.gamenodeFavicon = '';
  link.href = `/api/v1/branding/favicon${refreshFavicon ? `?v=${Date.now()}` : ''}`;
}
