import assert from 'node:assert/strict';
import test from 'node:test';
import { catalogStatusLabel, compatibilityLabel, compatibilityTone, editableTemplateValues, filterLibrary, installerLabel, maskTemplateDefault, platformAvailable, provisionabilityLabel, provisioningStatusLabel, provisioningTerminal, safeDirectoryName, sourceLabel, steamCMDReview, templateInputType, templatePortValue, validateEggFile, validateTemplateValue, variableTypeLabel, versionLabel } from '../src/templates-helpers.ts';

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

test('library search and filters stay client-side and cover catalog metadata',()=>{const items=[{name:'Minecraft NeoForge',description:'Modded Java',source_type:'official',version:'1.1.0',category:'minecraft',platforms:['linux'],installer:{type:'existing'},source_metadata:{tags:['mods']}},{name:'Seven',description:'Survival',source_type:'pelican-pterodactyl',category:'steamcmd',installer:{type:'steamcmd'},source_metadata:{tags:['zombies']}}];assert.equal(filterLibrary(items,'mods','all','all').length,1);assert.equal(filterLibrary(items,'survival','pelican-pterodactyl','steamcmd').length,1);assert.equal(filterLibrary(items,'missing','all','all').length,0);});
test('library labels distinguish provenance, version, and offline state',()=>{assert.equal(sourceLabel('official'),'GameNode Official');assert.equal(sourceLabel('pelican-pterodactyl'),'Imported Egg');assert.equal(versionLabel('1.2.0'),'v1.2.0');assert.equal(catalogStatusLabel({source:'cache',offline:true,cached:true}),'Offline · cached catalog');assert.equal(catalogStatusLabel({source:'remote',offline:false,cached:false}),'Official catalog up to date');});

test('SteamCMD cards and review expose fixed installer metadata',()=>{assert.equal(installerLabel('steamcmd'),'SteamCMD');assert.equal(platformAvailable(['windows','linux'],'linux'),true);assert.equal(platformAvailable(['windows'],'linux'),false);assert.equal(provisionabilityLabel(false,'linux'),'Unavailable on linux');assert.deepEqual(steamCMDReview({appID:294420,platform:'windows',validate:true,executable:'7DaysToDieServer.exe'}),[{label:'Installer',value:'SteamCMD'},{label:'Steam App ID',value:'294420'},{label:'Platform',value:'windows'},{label:'Validation',value:'Enabled'},{label:'Executable',value:'7DaysToDieServer.exe'}]);});
test('template port review resolves variable offsets without changing variables',()=>{assert.equal(templatePortValue({variable:'PORT',offset:3},{PORT:'26900'}),'26903');assert.equal(templatePortValue({port:27015},{}),'27015');});
