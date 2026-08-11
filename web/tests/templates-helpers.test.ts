import assert from 'node:assert/strict';
import test from 'node:test';
import { compatibilityLabel, compatibilityTone, editableTemplateValues, maskTemplateDefault, provisioningStatusLabel, provisioningTerminal, safeDirectoryName, templateInputType, validateEggFile, validateTemplateValue, variableTypeLabel } from '../src/templates-helpers.ts';

test('compatibility helpers use stable product labels', () => {
  assert.equal(compatibilityLabel('partially_compatible'), 'Partially compatible');
  assert.equal(compatibilityTone('unsupported'), 'danger');
  assert.equal(variableTypeLabel('boolean'), 'Boolean');
});

test('provisioning helpers map phases and terminal states',()=>{assert.equal(provisioningStatusLabel('downloading_steamcmd'),'Downloading SteamCMD');assert.equal(provisioningTerminal('installing'),false);assert.equal(provisioningTerminal('failed'),true);assert.equal(safeDirectoryName(' 7 Days / EU #1 '),'7-days-eu-1');});

test('variable controls and validation follow normalized metadata',()=>{assert.equal(templateInputType('string',true),'password');assert.equal(templateInputType('integer',false),'number');const variable={required:true,nullable:false,type:'integer',validation:{min:1,max:10}};assert.equal(validateTemplateValue(variable,''),'Required');assert.equal(validateTemplateValue(variable,'x'),'Enter a valid number');assert.equal(validateTemplateValue(variable,'11'),'Maximum 10');assert.equal(validateTemplateValue(variable,'5'),undefined);});

test('provision payload includes only editable template variables',()=>{assert.deepEqual(editableTemplateValues([{key:'PORT',user_editable:true},{key:'APP_ID',user_editable:false}],{PORT:'26900',APP_ID:'294420'}),{PORT:'26900'});});

test('sensitive defaults are always masked', () => {
  assert.equal(maskTemplateDefault('', true), '••••••••');
  assert.equal(maskTemplateDefault('do-not-show', true), '••••••••');
  assert.equal(maskTemplateDefault('294420', false), '294420');
});

test('egg upload validation is bounded and JSON-only', () => {
  assert.equal(validateEggFile('egg.json', 1024), undefined);
  assert.match(validateEggFile('egg.txt', 1024)!, /JSON/);
  assert.match(validateEggFile('egg.json', 0)!, /empty/);
  assert.match(validateEggFile('egg.json', 256 * 1024 + 1)!, /256 KiB/);
});
