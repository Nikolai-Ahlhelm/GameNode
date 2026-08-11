import assert from 'node:assert/strict';
import test from 'node:test';
import { canSendConsole, hasCapability } from '../src/capabilities.ts';

test('capabilities stay independent', () => {
  const capabilities = ['Server.View', 'Console.View', 'Files.Edit'];
  assert.equal(hasCapability(capabilities, 'Server.View'), true);
  assert.equal(hasCapability(capabilities, 'Server.Start'), false);
  assert.equal(hasCapability(capabilities, 'Files.View'), false);
});

test('console view does not permit input', () => {
  assert.equal(canSendConsole(['Console.View']), false);
  assert.equal(canSendConsole(['Console.View', 'Console.Send']), true);
});
