import test from 'node:test';
import assert from 'node:assert/strict';
import { hasRemotePermission, remoteBytes, remotePath, remoteServerStateLabel, remoteServerStateTone } from '../src/remote-servers-helpers.ts';

test('remote permissions are explicit', () => {
  assert.equal(hasRemotePermission(['RemoteServer.View'], 'RemoteServer.View'), true);
  assert.equal(hasRemotePermission(['RemoteServer.View'], 'RemoteServer.Manage'), false);
  assert.equal(hasRemotePermission(['RemoteFiles.Upload'], 'RemoteFiles.Upload'), true);
  assert.equal(hasRemotePermission(['RemoteFiles.View'], 'RemoteFiles.Download'), false);
});
test('remote lifecycle states map to shared tones', () => {
  assert.equal(remoteServerStateLabel('running'), 'Running');
  assert.equal(remoteServerStateTone('starting'), 'starting');
  assert.equal(remoteServerStateTone('unexpected'), 'unknown');
});
test('remote byte formatting is bounded', () => {
  assert.equal(remoteBytes(1024 * 1024), '1.0 MiB');
  assert.equal(remoteBytes(undefined), 'Unavailable');
});
test('remote paths are displayed relative to server root', () => {
  assert.equal(remotePath('\\configs\\server.properties'), 'configs/server.properties');
});
