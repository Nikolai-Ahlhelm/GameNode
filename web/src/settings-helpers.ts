export type SettingsResponse = {
  monitoring: { sample_interval_seconds: number; history_limit: number };
  restart_required: boolean;
  restart_required_fields?: string[];
};

export type SettingsForm = { sampleInterval: string; historyLimit: string };

export const validSampleInterval = (value: string) => /^\d+$/.test(value) && Number(value) >= 1 && Number(value) <= 300;
export const validHistoryLimit = (value: string) => /^\d+$/.test(value) && Number(value) >= 1 && Number(value) <= 10000;
export const settingsForm = (value: SettingsResponse): SettingsForm => ({ sampleInterval: String(value.monitoring.sample_interval_seconds), historyLimit: String(value.monitoring.history_limit) });
export const settingsPatch = (current: SettingsResponse, form: SettingsForm) => {
  const monitoring: Record<string, number> = {};
  if (Number(form.sampleInterval) !== current.monitoring.sample_interval_seconds) monitoring.sample_interval_seconds = Number(form.sampleInterval);
  if (Number(form.historyLimit) !== current.monitoring.history_limit) monitoring.history_limit = Number(form.historyLimit);
  return Object.keys(monitoring).length ? { monitoring } : undefined;
};
