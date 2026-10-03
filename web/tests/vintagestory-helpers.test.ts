import assert from 'node:assert/strict';
import test from 'node:test';
import { defaultVersion, dotnetAdvice, vintageStorySelectionError, versionOptionLabel, channelLabel } from '../src/vintagestory-helpers.ts';

const versions = [
  { version: '1.22.7', channel: 'stable' as const, latest: true },
  { version: '1.22.0', channel: 'stable' as const },
  { version: '1.22.0-rc.10', channel: 'rc' as const },
];

test('default is the latest stable, never a release candidate', () => {
  assert.equal(defaultVersion(versions), '1.22.7');
  assert.equal(defaultVersion([{ version: '1.22.0-rc.1', channel: 'rc' }, { version: '1.21.6', channel: 'stable' }]), '1.21.6');
  assert.equal(defaultVersion([]), '');
});

test('option labels mark latest stable and release candidates', () => {
  assert.equal(versionOptionLabel(versions[0]), '1.22.7 (latest stable)');
  assert.equal(versionOptionLabel(versions[1]), '1.22.0');
  assert.equal(versionOptionLabel(versions[2]), '1.22.0-rc.10 (release candidate)');
  assert.equal(channelLabel('rc'), 'release candidate');
});

test('selection must be a well-formed, offered version', () => {
  assert.equal(vintageStorySelectionError('1.22.7', versions), undefined);
  assert.equal(vintageStorySelectionError('1.22.0-rc.10', versions), undefined);
  assert.equal(vintageStorySelectionError('', versions), 'Choose a Vintage Story version.');
  assert.equal(vintageStorySelectionError('1.22.7; rm', versions), 'Choose a Vintage Story version.');
  assert.equal(vintageStorySelectionError('1.30.0', versions), 'This version is not offered by the official source.');
  assert.equal(vintageStorySelectionError('1.22.7', []), undefined);
});

test('dotnet advice distinguishes missing, wrong major and fine', () => {
  const required = { '1.22.7': 10, '1.21.6': 8 };
  assert.equal(dotnetAdvice(undefined, '1.22.7'), undefined);
  assert.equal(dotnetAdvice({ found: true, installed_majors: [10], required_major: {} }, '1.22.7'), undefined);
  assert.equal(dotnetAdvice({ found: false, installed_majors: [], required_major: required }, '1.22.7')?.tone, 'danger');
  const wrong = dotnetAdvice({ found: true, installed_majors: [8, 6], required_major: required }, '1.22.7');
  assert.equal(wrong?.tone, 'warning');
  assert.match(wrong?.text ?? '', /\.NET 6, 8/);
  assert.equal(dotnetAdvice({ found: true, installed_majors: [8, 10], required_major: required }, '1.22.7')?.tone, 'ok');
  assert.equal(dotnetAdvice({ found: true, installed_majors: [8], required_major: required }, '1.21.6')?.tone, 'ok');
});
