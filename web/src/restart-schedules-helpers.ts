export type RestartScheduleType = 'daily' | 'weekly';

export type RestartSchedule = {
  id: string;
  server_id: string;
  enabled: boolean;
  schedule_type: RestartScheduleType;
  time_of_day: string;
  day_of_week?: number;
  time_zone: string;
  next_restart_at?: string;
};

export type RestartScheduleDraft = {
  schedule_type: RestartScheduleType;
  time_of_day: string;
  day_of_week: number;
  time_zone: string;
};

export function restartSchedulePayload(draft: RestartScheduleDraft): Record<string, unknown> {
  const payload: Record<string, unknown> = {
    schedule_type: draft.schedule_type,
    time_of_day: draft.time_of_day,
    time_zone: draft.time_zone.trim(),
  };
  if (draft.schedule_type === 'weekly') payload.day_of_week = draft.day_of_week;
  return payload;
}

export function restartScheduleLabel(schedule: Pick<RestartSchedule, 'schedule_type' | 'day_of_week' | 'time_of_day' | 'time_zone'>): string {
  const recurrence = schedule.schedule_type === 'daily' ? 'Every day' : `Every ${weekdayLabel(schedule.day_of_week)}`;
  return `${recurrence} · ${schedule.time_of_day} · ${schedule.time_zone}`;
}

export function weekdayLabel(day?: number): string {
  return ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'][day ?? -1] ?? 'weekday';
}

export function nextRestartLabel(value?: string): string {
  if (!value) return 'Not scheduled';
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? 'Unavailable' : date.toLocaleString();
}
