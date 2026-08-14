import assert from 'node:assert/strict';
import test from 'node:test';
import { settingsForm, settingsPatch, validBranding, validHistoryLimit, validPasswordLengths, validSampleInterval } from '../src/settings-helpers.ts';

test('settings ranges accept only backend integer bounds', () => {
  assert.equal(validSampleInterval('0'), false); assert.equal(validSampleInterval('1'), true); assert.equal(validSampleInterval('300'), true); assert.equal(validSampleInterval('301'), false); assert.equal(validSampleInterval('1.5'), false);
  assert.equal(validHistoryLimit('0'), false); assert.equal(validHistoryLimit('1'), true); assert.equal(validHistoryLimit('10000'), true); assert.equal(validHistoryLimit('10001'), false);
  assert.equal(validPasswordLengths('8', '256'), true); assert.equal(validPasswordLengths('7', '256'), false); assert.equal(validPasswordLengths('24', '12'), false);
  assert.equal(validBranding('My Node', 'EU West'), true); assert.equal(validBranding('', 'EU West'), false);
});
test('settings patch contains only changed typed fields', () => {
  const current={monitoring:{sample_interval_seconds:5,history_limit:300},security:{password_minimum_length:8,password_maximum_length:256},branding:{name:'GameNode',subtitle:'Infrastructure manager',custom_favicon:false},restart_required:true};
  assert.equal(settingsPatch(current, settingsForm(current)), undefined);
  assert.deepEqual(settingsPatch(current,{sampleInterval:'7',historyLimit:'300',logLevel:'info',passwordMinimumLength:'10',passwordMaximumLength:'256',brandingName:'My Node',brandingSubtitle:'Infrastructure manager'}),{monitoring:{sample_interval_seconds:7},security:{password_minimum_length:10},branding:{name:'My Node'}});
});
