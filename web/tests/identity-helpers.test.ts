import assert from 'node:assert/strict';
import test from 'node:test';
import { availableIdentityActions, filterMembershipCandidates, listOrEmpty, permissionScopeLabel, serverRoleSuitability, userStatusLabel, validateGroupForm, validatePasswordReset, validateUserForm } from '../src/identity-helpers.ts';

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
  assert.deepEqual(validateGroupForm('Minecraft Admins', 'Server operators'), []);
  assert.equal(validateGroupForm('bad/name', '').length, 1);
  assert.deepEqual(validatePasswordReset('twelve chars!', 'twelve chars!'), []);
  assert.deepEqual(validatePasswordReset('12345678', '12345678'), []);
  assert.equal(validatePasswordReset('12345678', '12345678', 10, 24).length, 1);
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

test('derives permission scope labels from the backend catalog', () => {
  assert.equal(permissionScopeLabel(['global']), 'Global only');
  assert.equal(permissionScopeLabel(['server']), 'Server only');
  assert.equal(permissionScopeLabel(['global', 'server']), 'Global / Server');
  assert.equal(permissionScopeLabel([]), 'Not assignable');
});

test('explains server role suitability including empty and mixed roles', () => {
  const catalog = [
    { key: 'Server.View', allowed_scopes: ['global', 'server'] },
    { key: 'Console.View', allowed_scopes: ['global', 'server'] },
    { key: 'Users.View', allowed_scopes: ['global'] },
  ];
  assert.equal(serverRoleSuitability([], catalog).assignable, false);
  assert.equal(serverRoleSuitability(['Server.View', 'Console.View'], catalog).assignable, true);
  const mixed = serverRoleSuitability(['Server.View', 'Users.View'], catalog);
  assert.equal(mixed.assignable, false);
  assert.deepEqual(mixed.incompatible, ['Users.View']);
});
