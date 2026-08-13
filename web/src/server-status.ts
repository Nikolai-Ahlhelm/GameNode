export const serverStateLabel=(state?:string)=>({running:'Running',stopped:'Stopped',crashed:'Crashed',detached:'Detached',starting:'Starting',stopping:'Stopping'}[state??'']??(state||'Unknown'));
export const serverStateTone=(state?:string)=>state==='running'?'running':state==='crashed'?'crashed':state==='detached'?'stopping':state==='stopped'?'stopped':state||'unknown';
export const runtimeStateLabel=(state?:string,consoleDetached?:boolean)=>state==='running'&&consoleDetached?'Running (console detached)':serverStateLabel(state);
export const healthLabel=(health?:string)=>health==='healthy'?'Healthy':health==='degraded'?'Degraded':health==='detached'?'Console detached':health==='stopped'?'Stopped':health==='crashed'?'Crashed':'Unavailable';
export const metric=(value?:number,unit='')=>Number.isFinite(value)?`${value}${unit}`:'Unavailable';
