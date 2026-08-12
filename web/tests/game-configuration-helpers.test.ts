import assert from 'node:assert/strict';
import test from 'node:test';
import {gameConfigAvailability,gameConfigInputType,gameConfigPayload,initialGameConfigValues} from '../src/game-configuration-helpers.ts';

const fields=[{key:'NAME',type:'string',value:'Server',configured:true,sensitive:false,required:true,nullable:false,validation:{}},{key:'PASSWORD',type:'secret',configured:true,sensitive:true,required:false,nullable:true,validation:{}}];
test('managed config never hydrates secret values',()=>{assert.deepEqual(initialGameConfigValues(fields),{NAME:'Server',PASSWORD:''});});
test('unchanged secrets are omitted while normal fields remain explicit',()=>{assert.deepEqual(gameConfigPayload(fields,{NAME:'Changed',PASSWORD:''}),{NAME:'Changed'});assert.deepEqual(gameConfigPayload(fields,{NAME:'Changed',PASSWORD:'new-secret'}),{NAME:'Changed',PASSWORD:'new-secret'});});
test('managed config control types are deterministic',()=>{assert.equal(gameConfigInputType(fields[1]),'password');assert.equal(gameConfigInputType({...fields[0],type:'integer'}),'number');});
test('post-start configuration reports a stable pending state',()=>{assert.equal(gameConfigAvailability(true,'ignored'),'');assert.equal(gameConfigAvailability(false,'Generated after first start'),'Generated after first start');assert.match(gameConfigAvailability(false),/Start this server once/);});
