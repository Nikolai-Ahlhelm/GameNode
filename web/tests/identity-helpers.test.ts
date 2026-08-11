import assert from 'node:assert/strict';
import test from 'node:test';
import { listOrEmpty } from '../src/identity-helpers.ts';

test('normalizes nullable API lists', () => {
  const entries = [{ id: 'group-1' }];
  assert.equal(listOrEmpty(entries), entries);
  assert.deepEqual(listOrEmpty(null), []);
  assert.deepEqual(listOrEmpty(undefined), []);
});
