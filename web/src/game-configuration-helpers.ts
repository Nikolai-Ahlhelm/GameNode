export type GameConfigField={key:string;type:string;value?:string;configured:boolean;sensitive:boolean;required:boolean;nullable:boolean;validation:{min?:number;max?:number;min_length?:number;max_length?:number;allowed?:string[]}};
export type GameConfigAdapterSummary={id:string;version:string;format:string;target:string;restart_required:boolean;ready:boolean;fields:{key:string}[]};
export function initialGameConfigValues(fields:GameConfigField[]):Record<string,string>{return Object.fromEntries(fields.map(field=>[field.key,field.sensitive?'':field.value??'']));}
export function gameConfigPayload(fields:GameConfigField[],values:Record<string,string>):Record<string,string>{return Object.fromEntries(fields.filter(field=>!field.sensitive||(values[field.key]??'')!=='').map(field=>[field.key,values[field.key]??'']));}
export function gameConfigInputType(field:GameConfigField):'text'|'password'|'number'|'checkbox'{if(field.sensitive||field.type==='secret')return'password';if(field.type==='integer'||field.type==='number')return'number';if(field.type==='boolean')return'checkbox';return'text';}
export function gameConfigAvailability(ready:boolean,statusMessage?:string):string{return ready?'':statusMessage?.trim()||'Start this server once so the game can generate its configuration file.';}
// A managed-launch adapter owns no game file, so it has no target path to show.
export function gameConfigAdapterSource(adapter:{format:string;target:string;version:string}):string{return `${adapter.format==='managed-launch'?'Applied at server start':adapter.target} · adapter ${adapter.version}`;}
export function gameConfigSettingCount(adapters:GameConfigAdapterSummary[]):number{return adapters.reduce((total,adapter)=>total+adapter.fields.length,0);}
export function gameConfigRestartRequired(adapters:GameConfigAdapterSummary[]):boolean{return adapters.some(adapter=>adapter.restart_required);}
