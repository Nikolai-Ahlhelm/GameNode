import assert from 'node:assert/strict'; import test from 'node:test'; import {listOrEmpty,protocol,statusLabel,validPort} from '../src/ports-helpers.ts';
test('validates port range',()=>{assert.equal(validPort(0),false);assert.equal(validPort(1),true);assert.equal(validPort(65535),true);assert.equal(validPort(65536),false)});
test('maps protocols and statuses',()=>{assert.equal(protocol('tcp'),'tcp');assert.equal(protocol('udp'),'udp');assert.equal(protocol('icmp'),'');assert.equal(statusLabel('available'),'Available');assert.equal(statusLabel('in_use'),'In use');assert.equal(statusLabel('unknown'),'Unknown')});
test('listOrEmpty guards against a Go nil-slice null response, regression for the Ports tab crash', () => {
  assert.deepEqual(listOrEmpty(null), []);
  assert.deepEqual(listOrEmpty(undefined), []);
  assert.deepEqual(listOrEmpty([{ id: '1' }]), [{ id: '1' }]);
});
