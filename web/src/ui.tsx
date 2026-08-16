import type { LucideIcon } from 'lucide-react';
import { Inbox, Monitor, Moon, PanelLeftClose, PanelLeftOpen, Sun } from 'lucide-react';
import type { ThemeMode } from './theme';

export function PageHeader({ title, description, eyebrow, actions }: { title: string; description: string; eyebrow?: string; actions?: React.ReactNode }) {
  return <header className="page-header">
    <div>{eyebrow && <p className="eyebrow">{eyebrow}</p>}<h1>{title}</h1><p>{description}</p></div>
    {actions && <div className="page-actions">{actions}</div>}
  </header>;
}

export function SectionHeader({ title, description, actions }: { title: string; description?: string; actions?: React.ReactNode }) {
  return <div className="section-header"><div><h2>{title}</h2>{description && <p>{description}</p>}</div>{actions}</div>;
}

export function EmptyState({ title, description, action, icon: Icon = Inbox, compact = false }: { title: string; description: string; action?: React.ReactNode; icon?: LucideIcon; compact?: boolean }) {
  return <section className={`empty-state${compact ? ' empty-state--compact' : ''}`}><span className="empty-icon" aria-hidden="true"><Icon /></span><h2>{title}</h2><p>{description}</p>{action && <div className="empty-action">{action}</div>}</section>;
}

export function LoadingState({ label = 'Loading' }: { label?: string }) {
  return <div className="loading-state" role="status"><span className="loading-spinner" aria-hidden="true" /><span>{label}</span></div>;
}

const themeCycle: Record<ThemeMode, ThemeMode> = { system: 'dark', dark: 'light', light: 'system' };
const themeIcon: Record<ThemeMode, LucideIcon> = { system: Monitor, dark: Moon, light: Sun };
const themeLabel: Record<ThemeMode, string> = { system: 'System theme', dark: 'Dark theme', light: 'Light theme' };

/** Single reusable theme switch: cycles system → dark → light → system. Applies immediately (no reload), and the choice is announced via aria-label/title so it is never conveyed by icon alone. */
export function ThemeToggle({ theme, onChange }: { theme: ThemeMode; onChange: (next: ThemeMode) => void }) {
  const Icon = themeIcon[theme];
  return <button type="button" className="quiet theme-toggle" onClick={() => onChange(themeCycle[theme])} title={`${themeLabel[theme]} · click to switch`} aria-label={`Theme: ${themeLabel[theme]}. Click to switch.`}><Icon aria-hidden="true" /><span className="mobile-hide">{themeLabel[theme]}</span></button>;
}

/** Reusable page chrome shown above every dashboard page: breadcrumb/title, theme switch, and sidebar collapse - so no page has to rebuild this separately. Per-page primary actions still live in that page's own PageHeader. */
export function AppTopbar({ section, label, theme, onThemeChange, collapsed, onToggleSidebar }: { section: string; label: string; theme: ThemeMode; onThemeChange: (next: ThemeMode) => void; collapsed: boolean; onToggleSidebar: () => void }) {
  return <header className="app-topbar">
    <div className="app-topbar__crumb"><span>{section}</span><strong>{label}</strong></div>
    <div className="app-topbar__actions">
      <button type="button" className="quiet icon-button" onClick={onToggleSidebar} aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'} title={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}>{collapsed ? <PanelLeftOpen aria-hidden="true" /> : <PanelLeftClose aria-hidden="true" />}</button>
      <ThemeToggle theme={theme} onChange={onThemeChange} />
    </div>
  </header>;
}

export function MetricCard({ label, value, hint, tone = 'neutral', icon: Icon }: { label: string; value: React.ReactNode; hint?: string; tone?: 'neutral' | 'success' | 'warning' | 'danger' | 'info'; icon?: LucideIcon }) {
  return <article className={`metric-card metric-card--${tone}`}>
    <div className="metric-card__top"><span>{label}</span>{Icon && <Icon aria-hidden="true" />}</div>
    <strong>{value}</strong>{hint && <small>{hint}</small>}
  </article>;
}
