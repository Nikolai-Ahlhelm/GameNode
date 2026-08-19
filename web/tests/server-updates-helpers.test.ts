import assert from 'node:assert/strict';
import test from 'node:test';
import { serverUpdateCancellable, serverUpdateStatusLabel, serverUpdateTerminal } from '../src/server-updates-helpers.ts';

test('serverUpdateStatusLabel maps every known phase, defaulting unknown values', () => {
  assert.equal(serverUpdateStatusLabel('pending'), 'Queued');
  assert.equal(serverUpdateStatusLabel('preparing'), 'Preparing to update');
  assert.equal(serverUpdateStatusLabel('downloading_steamcmd'), 'Downloading SteamCMD');
  assert.equal(serverUpdateStatusLabel('updating'), 'Updating server files');
  assert.equal(serverUpdateStatusLabel('validating_installation'), 'Validating installation');
  assert.equal(serverUpdateStatusLabel('completed'), 'Complete');
  assert.equal(serverUpdateStatusLabel('failed'), 'Failed');
  assert.equal(serverUpdateStatusLabel('cancelled'), 'Cancelled');
  assert.equal(serverUpdateStatusLabel('made-up'), 'Updating');
});

test('serverUpdateTerminal is true only for completed/failed/cancelled', () => {
  for (const status of ['completed', 'failed', 'cancelled']) assert.equal(serverUpdateTerminal(status), true, status);
  for (const status of ['pending', 'preparing', 'downloading_steamcmd', 'steamcmd_ready', 'updating', 'steamcmd_completed', 'validating_installation']) {
    assert.equal(serverUpdateTerminal(status), false, status);
  }
});

test('serverUpdateCancellable is the inverse of terminal', () => {
  assert.equal(serverUpdateCancellable('updating'), true);
  assert.equal(serverUpdateCancellable('completed'), false);
});
