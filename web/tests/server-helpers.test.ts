import assert from 'node:assert/strict';
import test from 'node:test';
import { serverActionDisabled } from '../src/server-helpers.ts';

test('delete remains available in every lifecycle state', () => {
  for (const state of ['stopped', 'running', 'starting', 'stopping', 'crashed', 'unknown']) {
    assert.equal(serverActionDisabled('delete', state), false, state);
  }
});

test('other lifecycle buttons retain their state guards', () => {
  assert.equal(serverActionDisabled('start', 'stopped'), false);
  assert.equal(serverActionDisabled('start', 'running'), true);
  assert.equal(serverActionDisabled('stop', 'running'), false);
  assert.equal(serverActionDisabled('kill', 'stopped'), true);
});
