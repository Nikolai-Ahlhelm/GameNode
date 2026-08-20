import { useCallback, useEffect, useState, type ReactNode } from 'react';
import { RefreshCw, Server } from 'lucide-react';
import './status-page.css';

type ServiceStatus = { id: string; name: string; state: string; health: string; availability_percent: number; history: ('up' | 'degraded' | 'down' | 'unknown')[] };
type StatusData = { tenant: { name: string; slug: string }; public: boolean; updated_at: string; services: ServiceStatus[] | null };

const tone = (service: ServiceStatus) => service.health === 'healthy' ? 'up' : service.health === 'degraded' || service.health === 'detached' ? 'degraded' : 'down';
const label = (value: ReturnType<typeof tone>) => value === 'up' ? 'Operational' : value === 'degraded' ? 'Degraded' : 'Unavailable';

export function StatusPage({ slug, login }: { slug?: string; login: ReactNode }) {
  const [data, setData] = useState<StatusData>();
  const [state, setState] = useState<'loading' | 'ready' | 'login' | 'missing' | 'error'>('loading');
  const [seconds, setSeconds] = useState(30);
  const load = useCallback(async () => {
    try {
      const response = await fetch(`/api/v1/status/${encodeURIComponent(slug || 'default')}`, { credentials: 'same-origin' });
      if (response.status === 401 || response.status === 403) { setState('login'); return; }
      if (response.status === 404) { setState('missing'); return; }
      if (!response.ok) throw new Error('Status dashboard unavailable');
      setData(await response.json()); setState('ready'); setSeconds(30);
    } catch { setState('error'); }
  }, [slug]);
  useEffect(() => { void load(); const refresh = window.setInterval(() => void load(), 30000); const countdown = window.setInterval(() => setSeconds(value => Math.max(0, value - 1)), 1000); return () => { window.clearInterval(refresh); window.clearInterval(countdown); }; }, [load]);
  if (state === 'login') return <>{login}</>;
  if (state === 'loading') return <main className="status-page"><p className="status-message">Loading service status…</p></main>;
  if (state === 'missing') return <main className="status-page"><div className="status-message"><h1>Status dashboard unavailable</h1><p>This tenant has no enabled status dashboard.</p></div></main>;
  if (state === 'error' || !data) return <main className="status-page"><div className="status-message"><h1>Status temporarily unavailable</h1><button onClick={() => void load()}><RefreshCw />Retry</button></div></main>;
  const services = data.services ?? [];
  const tones = services.map(tone);
  const overall = tones.includes('down') ? 'down' : tones.includes('degraded') ? 'degraded' : 'up';
  const counts = { up: tones.filter(value => value === 'up').length, degraded: tones.filter(value => value === 'degraded').length, down: tones.filter(value => value === 'down').length };
  return <main className="status-page">
    <header className="status-header"><div><span className="status-brand">GN</span><h1>{data.tenant.name}</h1></div><div className="status-header__meta"><strong>Service status</strong><span>Last updated {new Date(data.updated_at).toLocaleTimeString()} · Next update in {seconds} sec.</span></div></header>
    <section className={`status-overall status-overall--${overall}`}><span className="status-orb" /><h2>{overall === 'up' ? 'All systems operational' : overall === 'degraded' ? 'Some systems degraded' : 'Service disruption detected'}</h2><span>{services.length} monitored {services.length === 1 ? 'service' : 'services'}</span></section>
    <div className="status-section-heading"><h2>Availability <span>Last 30 days</span></h2><span>Checks every 5 minutes</span></div>
    <section className="status-services">
      {services.length === 0 ? <div className="status-empty"><Server /><h2>No services configured</h2><p>This tenant currently has no servers.</p></div> : services.map(service => {
        const serviceTone = tone(service); const padding = Math.max(0, 90 - service.history.length);
        return <article className="status-service" key={service.id}><div className="status-service__name"><strong>{service.name}</strong><span>{service.state}</span></div><strong className={`status-percent status-color--${serviceTone}`}>{service.availability_percent.toFixed(1)}%</strong><div className="status-bars" aria-label={`${service.name} status history for the last 30 days`}>{Array.from({ length: padding }, (_, index) => <i className="unknown" key={`empty-${index}`} />)}{service.history.map((point, index) => <i className={point} key={`${point}-${index}`} />)}</div><span className={`status-current status-color--${serviceTone}`}><i />{label(serviceTone)}</span></article>;
      })}
    </section>
    <footer className="status-footer"><span>Total {services.length}</span><span className="status-color--up">Up {counts.up}</span><span className="status-color--degraded">Degraded {counts.degraded}</span><span className="status-color--down">Down {counts.down}</span></footer>
  </main>;
}
