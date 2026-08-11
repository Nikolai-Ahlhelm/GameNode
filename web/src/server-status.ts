export const serverStateLabel=(state?:string)=>({running:'Running',stopped:'Stopped',crashed:'Crashed',detached:'Detached',starting:'Starting',stopping:'Stopping'}[state??'']??(state||'Unknown'));
export const serverStateTone=(state?:string)=>state==='running'?'running':state==='crashed'?'crashed':state==='detached'?'stopping':state==='stopped'?'stopped':state||'unknown';
export const healthLabel=(health?:string)=>health==='healthy'?'Healthy':health==='degraded'?'Degraded':'Unavailable';
export const metric=(value?:number,unit='')=>Number.isFinite(value)?`${value}${unit}`:'Unavailable';
