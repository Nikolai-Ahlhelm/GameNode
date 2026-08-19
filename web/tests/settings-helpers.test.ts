import assert from 'node:assert/strict';
import test from 'node:test';
import { logCategoryKeys, settingsForm, settingsPatch, validBranding, validHistoryLimit, validPasswordLengths, validSampleInterval } from '../src/settings-helpers.ts';

test('settings ranges accept only backend integer bounds', () => {
  assert.equal(validSampleInterval('0'), false); assert.equal(validSampleInterval('1'), true); assert.equal(validSampleInterval('300'), true); assert.equal(validSampleInterval('301'), false); assert.equal(validSampleInterval('1.5'), false);
  assert.equal(validHistoryLimit('0'), false); assert.equal(validHistoryLimit('1'), true); assert.equal(validHistoryLimit('10000'), true); assert.equal(validHistoryLimit('10001'), false);
  assert.equal(validPasswordLengths('8', '256'), true); assert.equal(validPasswordLengths('7', '256'), false); assert.equal(validPasswordLengths('24', '12'), false);
  assert.equal(validBranding('My Node', 'EU West'), true); assert.equal(validBranding('', 'EU West'), false);
});
test('settings patch contains only changed typed fields', () => {
  const current={monitoring:{sample_interval_seconds:5,history_limit:300},security:{password_minimum_length:8,password_maximum_length:256},branding:{name:'GameNode',subtitle:'Infrastructure manager',custom_favicon:false},restart_required:true};
  assert.equal(settingsPatch(current, settingsForm(current)), undefined);
  const form = settingsForm(current);
  assert.deepEqual(settingsPatch(current,{...form,sampleInterval:'7',passwordMinimumLength:'10',brandingName:'My Node'}),{monitoring:{sample_interval_seconds:7},security:{password_minimum_length:10},branding:{name:'My Node'}});
});
test('settingsForm defaults every category to enabled and detailed errors to disabled when the backend omits them', () => {
  const current={monitoring:{sample_interval_seconds:5,history_limit:300},security:{password_minimum_length:8,password_maximum_length:256},branding:{name:'GameNode',subtitle:'',custom_favicon:false},restart_required:false};
  const form = settingsForm(current);
  for (const key of logCategoryKeys) assert.equal(form.logCategories[key], true, key);
  assert.equal(form.logDetailedErrors, false);
});
test('settings patch only sends the categories and detailed-errors flag that actually changed', () => {
  const current={monitoring:{sample_interval_seconds:5,history_limit:300},logging:{level:'info' as const,categories:Object.fromEntries(logCategoryKeys.map(k=>[k,true])) as Record<typeof logCategoryKeys[number],boolean>,detailed_errors:false},security:{password_minimum_length:8,password_maximum_length:256},branding:{name:'GameNode',subtitle:'',custom_favicon:false},restart_required:false};
  const form = settingsForm(current);
  form.logCategories = {...form.logCategories, http:false};
  form.logDetailedErrors = true;
  assert.deepEqual(settingsPatch(current, form), {logging:{categories:{http:false},detailed_errors:true}});
});
