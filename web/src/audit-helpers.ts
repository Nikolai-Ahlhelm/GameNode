export type AuditEvent = {
  id: string;
  timestamp: string;
  actor_username?: string;
  actor_user_id?: string;
  action: string;
  resource_type: string;
  resource_name?: string;
  resource_id?: string;
  server_id?: string;
  result: string;
  remote_ip?: string;
  metadata?: unknown;
  error_code?: string;
  error_summary?: string;
};

const actionLabels: Record<string, string> = {
  'auth.login': 'Signed in',
  'auth.logout': 'Signed out',
  'server.create': 'Server registered',
  'server.update': 'Server updated',
  'server.delete': 'Server deleted',
  'server.start': 'Server started',
  'server.stop': 'Server stopped',
  'server.restart': 'Server restarted',
  'server.kill': 'Server force-stopped',
  'server.provision_start': 'Server installation started',
  'server.provision_complete': 'Server installation completed',
  'server.provision_fail': 'Server installation failed',
  'server.provision_cancel': 'Server installation cancelled',
  'server.config_update': 'Game configuration updated',
  'port.create': 'Port registered',
  'port.update': 'Port updated',
  'port.delete': 'Port deleted',
  'file.create': 'File or directory created',
  'file.edit': 'File edited',
  'file.rename': 'File renamed',
  'file.move': 'File moved',
  'file.delete': 'File deleted',
  'file.upload': 'File uploaded',
  'console.input': 'Console input sent',
  'user.create': 'User created',
  'user.update': 'User updated',
  'user.enable': 'User enabled',
  'user.disable': 'User disabled',
  'user.delete': 'User deleted',
  'user.password_reset': 'Password reset',
  'group.create': 'Group created',
  'group.update': 'Group updated',
  'group.delete': 'Group deleted',
  'group.member_add': 'Group member added',
  'group.member_remove': 'Group member removed',
  'role.create': 'Role created',
  'role.update': 'Role updated',
  'role.delete': 'Role deleted',
  'role.permissions_update': 'Role permissions updated',
  'role.assignment_add': 'Role assigned',
  'role.assignment_remove': 'Role assignment removed',
  'settings.update': 'Settings updated',
  'settings.logs_clear': 'Application logs cleared',
  'settings.email_test': 'Email alert tested',
  'support.bundle_generate': 'Support bundle generated',
  'template.import': 'Template imported',
  'template.delete': 'Template deleted',
  'tenant.create': 'Tenant created',
  'tenant.update': 'Tenant updated',
  'tenant.delete': 'Tenant deleted',
  'tenant.member_add': 'Tenant member added',
  'tenant.member_remove': 'Tenant member removed',
};

export const auditActionLabel = (action: string): string => actionLabels[action] ?? action.split(/[._]/).filter(Boolean).map(part => part.charAt(0).toUpperCase() + part.slice(1)).join(' ');

export const auditActor = (username?: string, actorUserID?: string): string => {
  if (username?.trim()) return username;
  if (actorUserID) return `User ${actorUserID.slice(0, 8)}`;
  return 'Unauthenticated request';
};

export const auditResource = (event: AuditEvent): string => event.resource_name || event.resource_id || event.server_id || 'No specific resource';

export const auditTimestamp = (value?: string): { display: string; iso: string } => {
  if (!value) return { display: 'Unknown time', iso: '' };
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return { display: 'Invalid timestamp', iso: value };
  return {
    display: date.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'medium' }),
    iso: date.toISOString(),
  };
};

export const auditMetadata = (metadata: unknown): string => JSON.stringify(metadata, null, 2) ?? 'null';
