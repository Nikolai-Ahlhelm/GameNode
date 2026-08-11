import type { LucideIcon } from 'lucide-react';
import { Inbox } from 'lucide-react';

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

export function MetricCard({ label, value, hint, tone = 'neutral', icon: Icon }: { label: string; value: React.ReactNode; hint?: string; tone?: 'neutral' | 'success' | 'warning' | 'danger' | 'info'; icon?: LucideIcon }) {
  return <article className={`metric-card metric-card--${tone}`}>
    <div className="metric-card__top"><span>{label}</span>{Icon && <Icon aria-hidden="true" />}</div>
    <strong>{value}</strong>{hint && <small>{hint}</small>}
  </article>;
}
