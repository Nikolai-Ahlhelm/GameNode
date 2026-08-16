import assert from 'node:assert/strict';
import test from 'node:test';
import { filterMembershipCandidates, listOrEmpty, resolveTenantSelection, slugify, tenantCapabilities, validateTenantName, validateTenantSlug } from '../src/tenants-helpers.ts';

test('normalizes nullable API lists', () => {
  const entries = [{ id: 'tenant-1' }];
  assert.equal(listOrEmpty(entries), entries);
  assert.deepEqual(listOrEmpty(null), []);
  assert.deepEqual(listOrEmpty(undefined), []);
});

test('slugify mirrors the backend: lowercase, hyphen-separated, ASCII-only, dropped rather than transliterated', () => {
  assert.equal(slugify('Acme Corp'), 'acme-corp');
  assert.equal(slugify('  Müller GmbH  '), 'm-ller-gmbh');
  assert.equal(slugify('Tenant_One.Test'), 'tenant-one-test');
  assert.equal(slugify('---'), '');
  assert.equal(slugify('A'), 'a');
});

test('validates tenant name and slug forms', () => {
  assert.deepEqual(validateTenantName('Acme Corp'), []);
  assert.equal(validateTenantName('a').length, 1);
  assert.equal(validateTenantName('bad/name').length, 1);
  assert.deepEqual(validateTenantSlug('acme-corp'), []);
  assert.deepEqual(validateTenantSlug('Acme-Corp'), []);
  assert.equal(validateTenantSlug('a').length, 1);
  assert.equal(validateTenantSlug('-leading').length, 1);
  assert.equal(validateTenantSlug('double--hyphen').length, 1);
});

test('filters existing tenant members', () => {
  const users = [
    { id: 'one', username: 'Alice', enabled: true },
    { id: 'two', username: 'Bob', enabled: false },
  ];
  assert.deepEqual(filterMembershipCandidates(users, new Set(['one']), ''), [users[1]]);
  assert.deepEqual(filterMembershipCandidates(users, new Set(), 'ali'), [users[0]]);
});

test('keeps Tenants.View and Tenants.Manage independent', () => {
  assert.deepEqual(tenantCapabilities(['Tenants.Manage']), { view: false, manage: true });
  assert.deepEqual(tenantCapabilities(['Tenants.View']), { view: true, manage: false });
  assert.deepEqual(tenantCapabilities(undefined), { view: false, manage: false });
});

test('resolves the Create Server tenant selector from creatable tenants', () => {
  assert.deepEqual(resolveTenantSelection([]), { canCreate: false, locked: false, preselected: '' });
  assert.deepEqual(resolveTenantSelection([{ id: 't-1', name: 'Tenant A' }]), { canCreate: true, locked: true, preselected: 't-1' });
  assert.deepEqual(resolveTenantSelection([{ id: 't-1', name: 'Tenant A' }, { id: 't-2', name: 'Tenant B' }]), { canCreate: true, locked: false, preselected: '' });
});
