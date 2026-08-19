import assert from 'node:assert/strict';
import test from 'node:test';
import { canPullContainer, containerPullRequest, engineAvailabilityLabel, formatContainerPortMapping, formatCPULimit, formatMemoryLimit, imageAvailabilityLabel, imageAvailabilityTone, isContainerRuntime, pullStateLabel, pullStateTone } from '../src/container-detail-helpers.ts';

test('runtime isolation keeps container detail state out of native servers', () => {
  assert.equal(isContainerRuntime('native'), false);
  assert.equal(isContainerRuntime('container'), true);
  assert.equal(canPullContainer('native', 'missing', 'idle', true), false);
  assert.equal(canPullContainer('container', 'missing', 'idle', true), true);
});

test('image availability distinguishes reachable, missing, and unavailable engine states', () => {
  assert.equal(imageAvailabilityLabel('available'), 'Available');
  assert.equal(imageAvailabilityLabel('missing'), 'Image not available locally');
  assert.equal(engineAvailabilityLabel('available'), 'Reachable');
  assert.equal(engineAvailabilityLabel('missing'), 'Reachable');
  assert.equal(engineAvailabilityLabel('engine_unavailable'), 'Unavailable');
  assert.equal(imageAvailabilityLabel('engine_unavailable'), 'Container engine unavailable');
  assert.notEqual(imageAvailabilityLabel('engine_unavailable'), imageAvailabilityLabel('missing'));
  assert.equal(imageAvailabilityTone('available'), 'running');
  assert.equal(imageAvailabilityTone('missing'), 'degraded');
  assert.equal(imageAvailabilityTone('engine_unavailable'), 'unknown');
});

test('pull state is quiet when idle and explicit while active or failed', () => {
  assert.equal(pullStateLabel('idle'), '');
  assert.equal(pullStateLabel('pulling'), 'Pulling image…');
  assert.equal(pullStateTone('pulling'), 'starting');
  assert.equal(pullStateLabel('failed'), 'Image pull failed');
  assert.equal(pullStateTone('failed'), 'crashed');
});

test('pull capability requires Server.Edit, an engine result, and is disabled while pulling', () => {
  assert.equal(canPullContainer('container', 'missing', 'idle', true), true);
  assert.equal(canPullContainer('container', 'missing', 'idle', false), false);
  assert.equal(canPullContainer('container', 'missing', 'pulling', true), false);
  assert.equal(canPullContainer('container', 'engine_unavailable', 'idle', true), false);
  assert.equal(canPullContainer('container', 'available', 'idle', true), true);
});

test('container resource formatting stays in create/edit units', () => {
  assert.equal(formatCPULimit(2000), '2000 millicores');
  assert.equal(formatCPULimit(undefined), 'Unavailable');
  assert.equal(formatMemoryLimit(4096 * 1024 * 1024), '4096 MiB');
  assert.equal(formatMemoryLimit(undefined), 'Unavailable');
});

test('container networking formats host to container mappings', () => {
  assert.equal(formatContainerPortMapping({ bind_address: '0.0.0.0', port: 25565, protocol: 'tcp', container_port: 25565 }), '0.0.0.0:25565/TCP → 25565/TCP');
  assert.equal(formatContainerPortMapping({ port: 19132, protocol: 'udp' }), '0.0.0.0:19132/UDP → 19132/UDP');
});

test('pull request uses the existing authenticated endpoint and POST method', () => {
  const request = containerPullRequest('server-123', 'csrf-token');
  assert.equal(request.path, '/servers/server-123/container/pull');
  assert.equal(request.init.method, 'POST');
  assert.deepEqual(request.init.headers, { 'X-CSRF-Token': 'csrf-token' });
});
