import { FormEvent, useEffect, useState } from 'react';
import { CalendarClock, Pencil, Plus, RefreshCw, Trash2 } from 'lucide-react';
import { EmptyState, LoadingState, SectionHeader } from './ui';
import { nextRestartLabel, restartScheduleLabel, restartSchedulePayload, type RestartSchedule, type RestartScheduleDraft } from './restart-schedules-helpers';

const browserTimeZone = () => Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
const blankDraft = (): RestartScheduleDraft => ({ schedule_type: 'daily', time_of_day: '04:00', day_of_week: 0, time_zone: browserTimeZone() });

export function RestartSchedulesPanel({ serverID, token, canEdit }: { serverID: string; token: string; canEdit: boolean }) {
  const [schedules, setSchedules] = useState<RestartSchedule[]>();
  const [draft, setDraft] = useState<RestartScheduleDraft>(blankDraft);
  const [editing, setEditing] = useState<string>();
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  const load = () => { setError(''); fetch(`/api/v1/servers/${serverID}/restart-schedules`, { credentials: 'same-origin' }).then(async response => { if (!response.ok) throw Error((await response.json().catch(() => null))?.error?.message || 'Schedules could not be loaded'); return response.json(); }).then(data => setSchedules(data.schedules ?? [])).catch(reason => setError(reason instanceof Error ? reason.message : 'Schedules could not be loaded')); };
  useEffect(() => { load(); }, [serverID]);
  const reset = () => { setDraft(blankDraft()); setEditing(undefined); };
  async function save(event: FormEvent) {
    event.preventDefault(); setSaving(true); setError('');
    try {
      const response = await fetch(`/api/v1/servers/${serverID}/restart-schedules${editing ? `/${editing}` : ''}`, { method: editing ? 'PATCH' : 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': token }, body: JSON.stringify(restartSchedulePayload(draft)) });
      if (!response.ok) throw Error((await response.json().catch(() => null))?.error?.message || 'Schedule could not be saved');
      reset(); load();
    } catch (reason) { setError(reason instanceof Error ? reason.message : 'Schedule could not be saved'); } finally { setSaving(false); }
  }
  async function patch(id: string, body: Record<string, unknown>) {
    setError('');
    try { const response = await fetch(`/api/v1/servers/${serverID}/restart-schedules/${id}`, { method: 'PATCH', credentials: 'same-origin', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': token }, body: JSON.stringify(body) }); if (!response.ok) throw Error((await response.json().catch(() => null))?.error?.message || 'Schedule could not be updated'); load(); } catch (reason) { setError(reason instanceof Error ? reason.message : 'Schedule could not be updated'); }
  }
  async function remove(id: string) {
    if (!confirm('Delete this scheduled restart?')) return;
    try { const response = await fetch(`/api/v1/servers/${serverID}/restart-schedules/${id}`, { method: 'DELETE', credentials: 'same-origin', headers: { 'X-CSRF-Token': token } }); if (!response.ok) throw Error((await response.json().catch(() => null))?.error?.message || 'Schedule could not be deleted'); load(); } catch (reason) { setError(reason instanceof Error ? reason.message : 'Schedule could not be deleted'); }
  }
  function edit(schedule: RestartSchedule) { setEditing(schedule.id); setDraft({ schedule_type: schedule.schedule_type, time_of_day: schedule.time_of_day, day_of_week: schedule.day_of_week ?? 0, time_zone: schedule.time_zone }); }
  return <section className="subpanel restart-schedules"><SectionHeader title="Scheduled Restarts" description="Local recurring restarts use the normal server lifecycle. Missed occurrences are skipped." actions={canEdit && !editing ? <button type="button" className="quiet" onClick={() => setEditing('new')}><Plus />Add</button> : undefined} />
    {error && <p className="error notice">{error}</p>}
    {schedules === undefined ? <LoadingState label="Loading scheduled restarts…" /> : schedules.length === 0 && !editing ? <EmptyState compact title="No scheduled restarts" description="Add a daily or weekly restart time with an explicit IANA timezone." icon={CalendarClock} action={canEdit ? <button type="button" onClick={() => setEditing('new')}><Plus />Add schedule</button> : undefined} /> : <div className="definition-list">{schedules.map(schedule => <div className="definition-row" key={schedule.id}><span><strong>{restartScheduleLabel(schedule)}</strong><small>{schedule.enabled ? `Next restart: ${nextRestartLabel(schedule.next_restart_at)}` : 'Disabled'}</small></span><span className="actions">{canEdit && <><button type="button" className="quiet" onClick={() => void patch(schedule.id, { enabled: !schedule.enabled })}>{schedule.enabled ? 'Disable' : 'Enable'}</button><button type="button" className="quiet icon-button" title="Edit schedule" aria-label="Edit schedule" onClick={() => edit(schedule)}><Pencil /></button><button type="button" className="danger quiet icon-button" title="Delete schedule" aria-label="Delete schedule" onClick={() => void remove(schedule.id)}><Trash2 /></button></>}</span></div>)}</div>}
    {canEdit && editing && <form className="restart-schedule-form" onSubmit={save}><SectionHeader title={editing === 'new' ? 'Add restart schedule' : 'Edit restart schedule'} /><label>Recurrence<select value={draft.schedule_type} onChange={event => setDraft({ ...draft, schedule_type: event.target.value as RestartScheduleDraft['schedule_type'] })}><option value="daily">Every day</option><option value="weekly">Weekly</option></select></label>{draft.schedule_type === 'weekly' && <label>Day<select value={draft.day_of_week} onChange={event => setDraft({ ...draft, day_of_week: Number(event.target.value) })}>{['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'].map((day, index) => <option key={day} value={index}>{day}</option>)}</select></label>}<label>Time<input type="time" value={draft.time_of_day} onChange={event => setDraft({ ...draft, time_of_day: event.target.value })} required /></label><label>Timezone <span className="hint">IANA identifier</span><input value={draft.time_zone} onChange={event => setDraft({ ...draft, time_zone: event.target.value })} placeholder="Europe/Berlin" required /></label><p className="hint">Daylight-saving gaps are skipped; repeated fall-back times run once per occurrence. The selected timezone is stored with this schedule.</p><div className="actions"><button type="submit" disabled={saving}>{saving ? 'Saving…' : 'Save schedule'}</button><button type="button" className="quiet" onClick={reset}>Cancel</button></div></form>}
    {!canEdit && schedules && schedules.length > 0 && <p className="hint"><RefreshCw /> Schedule changes require Server.Edit.</p>}
  </section>;
}
