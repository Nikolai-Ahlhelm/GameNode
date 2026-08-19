import assert from 'node:assert/strict';
import test from 'node:test';
import { buildContainerServerUpdatePayload, containerDraftFromValues, type ServerCreateValues } from '../src/server-create-helpers.ts';

const containerValues: ServerCreateValues = {
  runtime_type: 'container', creation_mode: 'custom', name: 'Minecraft', description: 'Survival', working_directory: 'C:/servers/minecraft',
  executable: '', arguments: [], environment_variables: {}, auto_restart_enabled: true, auto_restart_max_attempts: 3,
  auto_restart_window_seconds: 300, auto_restart_delay_seconds: 5, stop_method: 'terminate', stop_command: '', stop_timeout_seconds: 15,
};

test('container edit initialization converts persisted config to form values', () => {
  const draft = containerDraftFromValues({ container: { image: 'itzg/minecraft-server:latest', command: ['java', '-Xmx4G', '-jar', 'server.jar'], cpu_limit_millis: 2000, memory_limit_bytes: 4294967296 } });
  assert.deepEqual(draft, { image: 'itzg/minecraft-server:latest', command: 'java\n-Xmx4G\n-jar\nserver.jar', cpuLimitMillis: '2000', memoryLimitMiB: '4096' });
});

test('container edit submission round-trips typed config and common fields', () => {
  const payload = buildContainerServerUpdatePayload(containerValues, { image: 'itzg/minecraft-server:latest', command: 'java\n-Xmx4G\n-jar\nserver.jar', cpuLimitMillis: '2000', memoryLimitMiB: '4096' });
  const { executable: _executable, arguments: _arguments, environment_variables: _environment, ...common } = containerValues;
  assert.deepEqual(payload, { ...common, container: { image: 'itzg/minecraft-server:latest', command: ['java', '-Xmx4G', '-jar', 'server.jar'], cpu_limit_millis: 2000, memory_limit_bytes: 4294967296 } });
});

test('container edit isolates native launch fields', () => {
  const payload = buildContainerServerUpdatePayload({ ...containerValues, executable: 'server.exe', arguments: ['--native'], environment_variables: { NATIVE: 'yes' } }, { image: 'example/game:1', command: 'entrypoint\n--safe', cpuLimitMillis: '500', memoryLimitMiB: '512' });
  assert.equal('executable' in payload, false);
  assert.equal('arguments' in payload, false);
  assert.equal('environment_variables' in payload, false);
  assert.deepEqual(payload.container, { image: 'example/game:1', command: ['entrypoint', '--safe'], cpu_limit_millis: 500, memory_limit_bytes: 512 * 1024 * 1024 });
});

test('container edit uses the same validation as create', () => {
  assert.throws(() => buildContainerServerUpdatePayload(containerValues, { image: '', command: '', cpuLimitMillis: '2000', memoryLimitMiB: '4096' }), /image/);
  assert.throws(() => buildContainerServerUpdatePayload(containerValues, { image: 'example/game:1', command: '', cpuLimitMillis: '1.5', memoryLimitMiB: '4096' }), /CPU/);
});
