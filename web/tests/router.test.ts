import assert from 'node:assert/strict';
import test from 'node:test';
import { parseRoute } from '../src/router.ts';

test('parses primary and server deep-link routes', () => {
  assert.deepEqual(parseRoute('/dashboard'), { page: 'dashboard' });
  assert.deepEqual(parseRoute('/server'), { page: 'servers' });
  assert.deepEqual(parseRoute('/server/server-1'), { page: 'servers', serverID: 'server-1' });
  assert.deepEqual(parseRoute('/server/server-1/edit'), { page: 'servers', serverID: 'server-1', mode: 'edit' });
  assert.deepEqual(parseRoute('/tenants/tenant-1/members'), { page: 'tenants', tenantID: 'tenant-1', tab: 'members' });
  assert.deepEqual(parseRoute('/status/default'), { page: 'status', slug: 'default' });
});

test('rejects unknown and malformed routes', () => {
  assert.deepEqual(parseRoute('/unknown'), { page: 'not-found' });
  assert.deepEqual(parseRoute('/server/%E0%A4%A'), { page: 'not-found' });
});
