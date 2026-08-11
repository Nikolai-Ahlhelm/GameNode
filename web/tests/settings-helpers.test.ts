import assert from 'node:assert/strict';
import test from 'node:test';
import { settingsForm, settingsPatch, validHistoryLimit, validSampleInterval } from '../src/settings-helpers.ts';

test('settings ranges accept only backend integer bounds', () => {
  assert.equal(validSampleInterval('0'), false); assert.equal(validSampleInterval('1'), true); assert.equal(validSampleInterval('300'), true); assert.equal(validSampleInterval('301'), false); assert.equal(validSampleInterval('1.5'), false);
  assert.equal(validHistoryLimit('0'), false); assert.equal(validHistoryLimit('1'), true); assert.equal(validHistoryLimit('10000'), true); assert.equal(validHistoryLimit('10001'), false);
});
test('settings patch contains only changed monitoring fields', () => {
  const current={monitoring:{sample_interval_seconds:5,history_limit:300},restart_required:true};
  assert.equal(settingsPatch(current, settingsForm(current)), undefined);
  assert.deepEqual(settingsPatch(current,{sampleInterval:'7',historyLimit:'300'}),{monitoring:{sample_interval_seconds:7}});
});
