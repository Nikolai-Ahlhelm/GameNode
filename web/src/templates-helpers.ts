export type CompatibilityStatus = 'compatible' | 'partially_compatible' | 'unsupported';

export function compatibilityLabel(status: CompatibilityStatus): string {
  return status === 'compatible' ? 'Compatible' : status === 'partially_compatible' ? 'Partially compatible' : 'Unsupported';
}

export function compatibilityTone(status: CompatibilityStatus): 'success' | 'warning' | 'danger' {
  return status === 'compatible' ? 'success' : status === 'partially_compatible' ? 'warning' : 'danger';
}

export function maskTemplateDefault(value: string, sensitive: boolean): string {
  return sensitive ? '••••••••' : value || 'Empty';
}

export function validateEggFile(name: string, size: number): string | undefined {
  if (!name.toLowerCase().endsWith('.json')) return 'Choose a JSON Egg file.';
  if (size <= 0) return 'The Egg file is empty.';
  if (size > 256 * 1024) return 'The Egg file exceeds the 256 KiB limit.';
}

export function variableTypeLabel(type: string): string {
  return ({ integer: 'Integer', number: 'Number', boolean: 'Boolean', enum: 'Select', secret: 'Secret', string: 'Text' } as Record<string, string>)[type] ?? 'Text';
}

export function provisioningStatusLabel(status: string): string {
  return ({ pending: 'Queued', preparing: 'Preparing server storage', downloading_steamcmd: 'Downloading SteamCMD', steamcmd_ready: 'SteamCMD ready', installing: 'Installing game files', creating_server: 'Creating GameNode server', completed: 'Complete', failed: 'Failed', cancelled: 'Cancelled' } as Record<string,string>)[status] ?? 'Provisioning';
}
export function provisioningTerminal(status: string): boolean { return ['completed','failed','cancelled'].includes(status); }
export function templateInputType(type: string, sensitive: boolean): 'text'|'password'|'number'|'checkbox' { if(sensitive||type==='secret')return 'password';if(type==='integer'||type==='number')return 'number';if(type==='boolean')return 'checkbox';return 'text'; }
export function safeDirectoryName(name: string): string { return name.trim().toLowerCase().replace(/[^a-z0-9._-]+/g,'-').replace(/^[._-]+|[._-]+$/g,'').slice(0,64) || 'game-server'; }
export function editableTemplateValues(variables: { key:string;user_editable:boolean }[], values: Record<string,string>): Record<string,string> { return Object.fromEntries(variables.filter(variable=>variable.user_editable).map(variable=>[variable.key,values[variable.key]??''])); }
export function validateTemplateValue(variable: { required:boolean;nullable:boolean;type:string;validation:{min?:number;max?:number;min_length?:number;max_length?:number;allowed?:string[]} }, value:string): string|undefined { if(!value&&variable.nullable)return;if(!value&&variable.required)return 'Required';if((variable.type==='integer'&&!/^-?\d+$/.test(value))||(variable.type==='number'&&!Number.isFinite(Number(value))))return 'Enter a valid number';const number=Number(value);if(variable.validation.min!==undefined&&number<variable.validation.min)return `Minimum ${variable.validation.min}`;if(variable.validation.max!==undefined&&number>variable.validation.max)return `Maximum ${variable.validation.max}`;if(variable.validation.min_length!==undefined&&value.length<variable.validation.min_length)return `At least ${variable.validation.min_length} characters`;if(variable.validation.max_length!==undefined&&value.length>variable.validation.max_length)return `At most ${variable.validation.max_length} characters`;if(variable.validation.allowed?.length&&!variable.validation.allowed.includes(value))return 'Choose an allowed value'; }
