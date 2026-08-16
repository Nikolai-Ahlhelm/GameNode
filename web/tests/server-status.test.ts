import assert from 'node:assert/strict';
import test from 'node:test';
import { healthLabel, healthTone, serverStateLabel, serverStateTone } from '../src/server-status.ts';

test('serverStateTone maps every known lifecycle state to a status class', () => {
  assert.equal(serverStateTone('running'), 'running');
  assert.equal(serverStateTone('crashed'), 'crashed');
  assert.equal(serverStateTone('detached'), 'stopping');
  assert.equal(serverStateTone('stopped'), 'stopped');
  assert.equal(serverStateTone('starting'), 'starting');
  assert.equal(serverStateTone(undefined), 'unknown');
  assert.equal(serverStateTone('made-up'), 'made-up');
});

test('serverStateLabel falls back to the raw state for unknown values', () => {
  assert.equal(serverStateLabel('running'), 'Running');
  assert.equal(serverStateLabel(undefined), 'Unknown');
  assert.equal(serverStateLabel('weird'), 'weird');
});

test('healthTone maps every known health value to a status class, defaulting to unknown', () => {
  assert.equal(healthTone('healthy'), 'running');
  assert.equal(healthTone('degraded'), 'degraded');
  assert.equal(healthTone('crashed'), 'crashed');
  assert.equal(healthTone('detached'), 'stopping');
  assert.equal(healthTone('stopped'), 'stopped');
  assert.equal(healthTone(undefined), 'unknown');
  assert.equal(healthLabel(undefined), 'Unavailable');
  assert.equal(healthLabel('degraded'), 'Degraded');
});
