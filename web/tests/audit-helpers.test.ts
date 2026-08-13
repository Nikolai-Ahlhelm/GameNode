import assert from 'node:assert/strict';
import test from 'node:test';
import { auditActionLabel, auditActor, auditMetadata, auditResource, auditTimestamp } from '../src/audit-helpers.ts';

test('audit labels and actors are human readable', () => {
  assert.equal(auditActionLabel('server.provision_complete'), 'Server installation completed');
  assert.equal(auditActionLabel('custom.background_action'), 'Custom Background Action');
  assert.equal(auditActor('admin', 'user-1'), 'admin');
  assert.equal(auditActor('', '1234567890'), 'User 12345678');
  assert.equal(auditActor(), 'Unauthenticated request');
});

test('audit timestamps retain exact ISO time and handle missing values', () => {
  assert.equal(auditTimestamp('2026-08-13T10:11:12.123Z').iso, '2026-08-13T10:11:12.123Z');
  assert.equal(auditTimestamp().display, 'Unknown time');
  assert.equal(auditTimestamp('broken').display, 'Invalid timestamp');
});

test('audit resource and controlled metadata provide useful detail', () => {
  assert.equal(auditResource({ id: '1', timestamp: '', action: 'server.start', resource_type: 'server', resource_name: 'Survival', resource_id: 'server-1', result: 'success' }), 'Survival');
  assert.equal(auditResource({ id: '1', timestamp: '', action: 'server.start', resource_type: 'server', server_id: 'server-1', result: 'success' }), 'server-1');
  assert.equal(auditMetadata({ bytes: 42 }), '{\n  "bytes": 42\n}');
});
