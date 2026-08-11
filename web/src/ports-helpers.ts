export const validPort=(value:number)=>Number.isInteger(value)&&value>=1&&value<=65535;
export const protocol=(value:string)=>value==='tcp'||value==='udp'?value:'';
export const statusLabel=(value:string)=>({available:'Available',in_use:'In use',unknown:'Unknown'}[value]??'Unknown');
