export const validPort=(value:number)=>Number.isInteger(value)&&value>=1&&value<=65535;
export const protocol=(value:string)=>value==='tcp'||value==='udp'?value:'';
export const statusLabel=(value:string)=>({available:'Available',in_use:'In use',unknown:'Unknown'}[value]??'Unknown');
export type PortSummary = { bind_address?: string; port: number; protocol: string; container_port?: number };
/** A JSON-encoded Go nil slice serializes as `null`, not `[]`. Guards every
 * caller that renders this list (e.g. `ports.length`) from crashing on that
 * empty-but-null response, matching the same pattern tenants-helpers.ts's
 * listOrEmpty already applies to its own list endpoints. */
export const listOrEmpty=<T,>(value:T[]|null|undefined):T[]=>value??[];
