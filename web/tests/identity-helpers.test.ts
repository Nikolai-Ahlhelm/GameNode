import assert from 'node:assert/strict';
import test from 'node:test';
import { availableIdentityActions, filterMembershipCandidates, listOrEmpty, userStatusLabel, validateGroupForm, validatePasswordReset, validateUserForm } from '../src/identity-helpers.ts';

test('normalizes nullable API lists', () => {
  const entries = [{ id: 'group-1' }];
  assert.equal(listOrEmpty(entries), entries);
  assert.deepEqual(listOrEmpty(null), []);
  assert.deepEqual(listOrEmpty(undefined), []);
});

test('labels active and disabled users', () => {
  assert.equal(userStatusLabel(true), 'Active');
  assert.equal(userStatusLabel(false), 'Disabled');
});

test('validates user, group, and password forms', () => {
  assert.deepEqual(validateUserForm('alice', 'alice@example.test'), []);
  assert.equal(validateUserForm('!', 'invalid').length, 2);
  assert.deepEqual(validateGroupForm('operators', 'Server operators'), []);
  assert.equal(validateGroupForm('bad name', '').length, 1);
  assert.deepEqual(validatePasswordReset('twelve chars!', 'twelve chars!'), []);
  assert.equal(validatePasswordReset('short', 'different').length, 2);
});

test('filters existing memberships and marks disabled candidates without hiding them', () => {
  const users = [
    { id: 'one', username: 'Alice', enabled: true },
    { id: 'two', username: 'Bob', enabled: false },
    { id: 'three', username: 'Carol', enabled: true },
  ];
  assert.deepEqual(filterMembershipCandidates(users, new Set(['one']), 'bo'), [users[1]]);
  assert.deepEqual(filterMembershipCandidates(users, new Set(['one']), ''), [users[1], users[2]]);
});

test('keeps View and Manage capabilities independent', () => {
  assert.deepEqual(availableIdentityActions(['Users.Manage'], 'user'), { view: false, manage: true });
  assert.deepEqual(availableIdentityActions(['Groups.View'], 'group'), { view: true, manage: false });
});
