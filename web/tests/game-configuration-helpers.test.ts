import assert from 'node:assert/strict';
import test from 'node:test';
import {gameConfigAdapterSource,gameConfigAvailability,gameConfigInputType,gameConfigPayload,gameConfigRestartRequired,gameConfigSettingCount,initialGameConfigValues} from '../src/game-configuration-helpers.ts';

const fields=[{key:'NAME',type:'string',value:'Server',configured:true,sensitive:false,required:true,nullable:false,validation:{}},{key:'PASSWORD',type:'secret',configured:true,sensitive:true,required:false,nullable:true,validation:{}}];
test('managed config never hydrates secret values',()=>{assert.deepEqual(initialGameConfigValues(fields),{NAME:'Server',PASSWORD:''});});
test('unchanged secrets are omitted while normal fields remain explicit',()=>{assert.deepEqual(gameConfigPayload(fields,{NAME:'Changed',PASSWORD:''}),{NAME:'Changed'});assert.deepEqual(gameConfigPayload(fields,{NAME:'Changed',PASSWORD:'new-secret'}),{NAME:'Changed',PASSWORD:'new-secret'});});
test('managed config control types are deterministic',()=>{assert.equal(gameConfigInputType(fields[1]),'password');assert.equal(gameConfigInputType({...fields[0],type:'integer'}),'number');});
test('post-start configuration reports a stable pending state',()=>{assert.equal(gameConfigAvailability(true,'ignored'),'');assert.equal(gameConfigAvailability(false,'Generated after first start'),'Generated after first start');assert.match(gameConfigAvailability(false),/Start this server once/);});

const launchAdapter={id:'valheim-settings',version:'1.0.0',format:'managed-launch',target:'',restart_required:true,ready:true,fields:[{key:'SERVER_NAME'},{key:'SERVER_PASSWORD'},{key:'CROSSPLAY'}]};
const fileAdapter={id:'palworld-settings',version:'1.0.0',format:'section-tuple-key-values',target:'Pal/Saved/Config/WindowsServer/PalWorldSettings.ini',restart_required:false,ready:true,fields:[{key:'SERVER_NAME'}]};
test('a launch adapter without a file target still describes its source',()=>{assert.equal(gameConfigAdapterSource(launchAdapter),'Applied at server start · adapter 1.0.0');assert.match(gameConfigAdapterSource(fileAdapter),/PalWorldSettings\.ini · adapter 1\.0\.0/);});
test('overview summary counts every managed setting across adapters',()=>{assert.equal(gameConfigSettingCount([launchAdapter,fileAdapter]),4);assert.equal(gameConfigSettingCount([]),0);});
test('restart-required state is reported when any adapter requires it',()=>{assert.equal(gameConfigRestartRequired([launchAdapter,fileAdapter]),true);assert.equal(gameConfigRestartRequired([fileAdapter]),false);});
test('secret fields on a launch adapter are never hydrated into the form',()=>{const launchFields=[{key:'SERVER_NAME',type:'string',value:'My Valheim',configured:true,sensitive:false,required:true,nullable:false,validation:{}},{key:'SERVER_PASSWORD',type:'secret',configured:true,sensitive:true,required:false,nullable:true,validation:{}}];assert.deepEqual(initialGameConfigValues(launchFields),{SERVER_NAME:'My Valheim',SERVER_PASSWORD:''});assert.deepEqual(gameConfigPayload(launchFields,{SERVER_NAME:'My Valheim',SERVER_PASSWORD:''}),{SERVER_NAME:'My Valheim'});});
