import assert from 'node:assert/strict';
import test from 'node:test';
import { breadcrumbs, classifyFile, isSafeRelativePath, joinRelativePath, parentRelativePath } from '../src/files-helpers.ts';

test('builds relative breadcrumbs and paths', () => {
  assert.equal(joinRelativePath('config', 'server.properties'), 'config/server.properties');
  assert.equal(parentRelativePath('config/nested/file.txt'), 'config/nested');
  assert.deepEqual(breadcrumbs('config/nested'), [{ label: 'Root', path: '' }, { label: 'config', path: 'config' }, { label: 'nested', path: 'config/nested' }]);
});

test('classifies editable, read-only, and binary files', () => {
  assert.deepEqual(classifyFile('server.yml'), { text: true, readOnly: false, language: 'yaml' });
  assert.deepEqual(classifyFile('latest.log'), { text: true, readOnly: true, language: 'plaintext' });
  assert.equal(classifyFile('world.dat').text, false);
});

test('does not create unsafe relative paths', () => {
  assert.equal(isSafeRelativePath('config/server.properties'), true);
  assert.equal(isSafeRelativePath('../outside'), false);
  assert.equal(isSafeRelativePath('C:\\outside'), false);
  assert.equal(isSafeRelativePath('/outside'), false);
});
