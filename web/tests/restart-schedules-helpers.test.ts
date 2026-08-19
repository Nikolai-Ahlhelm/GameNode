import assert from 'node:assert/strict';
import test from 'node:test';
import { nextRestartLabel, restartScheduleLabel, restartSchedulePayload, weekdayLabel } from '../src/restart-schedules-helpers.ts';

test('builds daily and weekly payloads without leaking weekday into daily schedules', () => {
  assert.deepEqual(restartSchedulePayload({ schedule_type: 'daily', time_of_day: '04:00', day_of_week: 0, time_zone: ' Europe/Berlin ' }), { schedule_type: 'daily', time_of_day: '04:00', time_zone: 'Europe/Berlin' });
  assert.deepEqual(restartSchedulePayload({ schedule_type: 'weekly', time_of_day: '04:00', day_of_week: 0, time_zone: 'UTC' }), { schedule_type: 'weekly', time_of_day: '04:00', day_of_week: 0, time_zone: 'UTC' });
});

test('formats recurrence and backend next timestamp', () => {
  assert.equal(weekdayLabel(0), 'Sunday');
  assert.equal(restartScheduleLabel({ schedule_type: 'weekly', day_of_week: 0, time_of_day: '04:00', time_zone: 'Europe/Berlin' }), 'Every Sunday · 04:00 · Europe/Berlin');
  assert.notEqual(nextRestartLabel('2026-08-20T04:00:00Z'), 'Not scheduled');
  assert.equal(nextRestartLabel(undefined), 'Not scheduled');
});
