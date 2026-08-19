import assert from 'node:assert/strict';
import test from 'node:test';
import {
  compatibilityLabel,
  compatibilityTone,
  formatCapability,
  healthLabel,
  healthTone,
  listOrEmpty,
  nodeCapabilities,
  relativeTime,
  validateEndpoint,
  validatePairingToken,
} from '../src/nodes-helpers.ts';

test('normalizes nullable API lists', () => {
  assert.deepEqual(listOrEmpty(null), []);
  assert.deepEqual(listOrEmpty(undefined), []);
  assert.deepEqual(listOrEmpty([1, 2]), [1, 2]);
});

test('keeps Node.View and Node.Manage independent', () => {
  assert.deepEqual(nodeCapabilities(undefined), { view: false, manage: false });
  assert.deepEqual(nodeCapabilities(['Node.View']), { view: true, manage: false });
  assert.deepEqual(nodeCapabilities(['Node.Manage']), { view: false, manage: true });
  assert.deepEqual(nodeCapabilities(['Node.View', 'Node.Manage']), { view: true, manage: true });
});

test('maps every backend health value to a distinct label and tone', () => {
  assert.equal(healthLabel('reachable'), 'Reachable');
  assert.equal(healthTone('reachable'), 'running');
  assert.equal(healthLabel('unreachable'), 'Unreachable');
  assert.equal(healthTone('unreachable'), 'crashed');
  assert.equal(healthLabel('authentication_failed'), 'Authentication failed');
  assert.equal(healthTone('authentication_failed'), 'crashed');
  assert.equal(healthLabel('protocol_incompatible'), 'Protocol incompatible');
  assert.equal(healthTone('protocol_incompatible'), 'crashed');
  assert.equal(healthLabel('degraded'), 'Degraded');
  assert.equal(healthTone('degraded'), 'degraded');
  assert.equal(healthLabel(undefined), 'Unknown');
  assert.equal(healthTone(undefined), 'unknown');
});

test('unreachable is never conflated with authentication or protocol failures', () => {
  assert.notEqual(healthLabel('unreachable'), healthLabel('authentication_failed'));
  assert.notEqual(healthLabel('authentication_failed'), healthLabel('protocol_incompatible'));
});

test('maps compatibility states', () => {
  assert.equal(compatibilityLabel('compatible'), 'Compatible');
  assert.equal(compatibilityTone('compatible'), 'running');
  assert.equal(compatibilityLabel('limited_capabilities'), 'Limited capabilities');
  assert.equal(compatibilityTone('limited_capabilities'), 'degraded');
  assert.equal(compatibilityLabel('incompatible'), 'Incompatible');
  assert.equal(compatibilityTone('incompatible'), 'crashed');
  assert.equal(compatibilityLabel('unknown'), 'Unknown');
  assert.equal(compatibilityTone('unknown'), 'unknown');
});

test('formats snake_case capability identifiers for display', () => {
  assert.equal(formatCapability('native_runtime'), 'Native Runtime');
  assert.equal(formatCapability('container_resource_limits'), 'Container Resource Limits');
  assert.equal(formatCapability('console'), 'Console');
});

test('validates remote node endpoints', () => {
  assert.deepEqual(validateEndpoint('https://node.internal:8443'), []);
  assert.deepEqual(validateEndpoint('http://127.0.0.1:8080'), []);
  assert.notEqual(validateEndpoint('').length, 0);
  assert.notEqual(validateEndpoint('ftp://node.internal').length, 0);
  assert.notEqual(validateEndpoint('https://user:pass@node.internal').length, 0);
  assert.notEqual(validateEndpoint('https://node.internal/some/path').length, 0);
  assert.notEqual(validateEndpoint('https://node.internal?x=1').length, 0);
  assert.notEqual(validateEndpoint('not a url').length, 0);
});

test('requires a non-empty pairing token', () => {
  assert.notEqual(validatePairingToken('').length, 0);
  assert.notEqual(validatePairingToken('   ').length, 0);
  assert.deepEqual(validatePairingToken('a-real-token'), []);
});

test('formats relative last-contact time, handling missing/invalid timestamps', () => {
  assert.equal(relativeTime(undefined), 'Never');
  assert.equal(relativeTime('not-a-date'), 'Never');
  assert.equal(relativeTime(new Date().toISOString()), 'Just now');
});
