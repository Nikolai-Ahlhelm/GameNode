import assert from 'node:assert/strict';
import test from 'node:test';
import { buildServerCreatePayload, defaultContainerCreateDraft, type ServerCreateValues } from '../src/server-create-helpers.ts';

const native: ServerCreateValues = {
  runtime_type: 'native', name: 'Native server', working_directory: 'C:/servers/native', executable: 'server.exe', arguments: ['old'], environment_variables: { OLD: '1' }, stop_method: 'terminate',
};

test('native selection retains the existing native payload shape', () => {
  const payload = buildServerCreatePayload(native, ['--port', '25565'], { EULA: 'TRUE' }, defaultContainerCreateDraft());
  assert.deepEqual(payload, { ...native, arguments: ['--port', '25565'], environment_variables: { EULA: 'TRUE' } });
});

test('container selection creates the typed container payload', () => {
  const payload = buildServerCreatePayload({ ...native, runtime_type: 'container' }, ['--ignored'], { IGNORED: 'true' }, { image: 'itzg/minecraft-server:latest', command: 'java\n-jar\nserver.jar', cpuLimitMillis: '2000', memoryLimitMiB: '4096' });
  assert.deepEqual(payload, {
    runtime_type: 'container', name: 'Native server', working_directory: 'C:/servers/native', stop_method: 'terminate',
    container: { image: 'itzg/minecraft-server:latest', command: ['java', '-jar', 'server.jar'], cpu_limit_millis: 2000, memory_limit_bytes: 4096 * 1024 * 1024 },
  });
  assert.equal('executable' in payload, false);
  assert.equal('arguments' in payload, false);
  assert.equal('environment_variables' in payload, false);
});

test('runtime switching does not leak hidden configuration into the other payload', () => {
  const container = buildServerCreatePayload({ ...native, runtime_type: 'container' }, [], {}, { image: 'example/game:1', command: '', cpuLimitMillis: '1000', memoryLimitMiB: '1024' });
  const nativePayload = buildServerCreatePayload({ ...native, container: { image: 'example/game:1' } }, ['--native'], { NATIVE: 'yes' }, { image: 'example/game:1', command: 'ignored', cpuLimitMillis: '1000', memoryLimitMiB: '1024' });
  assert.equal('container' in container, true);
  assert.equal('container' in nativePayload, false);
  assert.deepEqual(nativePayload.arguments, ['--native']);
});

test('container resources normalize MiB to backend bytes and reject invalid values', () => {
  const payload = buildServerCreatePayload({ ...native, runtime_type: 'container' }, [], {}, { image: 'example/game:1', command: '', cpuLimitMillis: '250', memoryLimitMiB: '512' });
  assert.deepEqual(payload.container, { image: 'example/game:1', command: [], cpu_limit_millis: 250, memory_limit_bytes: 512 * 1024 * 1024 });
  assert.throws(() => buildServerCreatePayload({ ...native, runtime_type: 'container' }, [], {}, { image: '', command: '', cpuLimitMillis: '250', memoryLimitMiB: '512' }), /image/);
  assert.throws(() => buildServerCreatePayload({ ...native, runtime_type: 'container' }, [], {}, { image: 'example/game:1', command: '', cpuLimitMillis: '1.5', memoryLimitMiB: '512' }), /CPU/);
});
