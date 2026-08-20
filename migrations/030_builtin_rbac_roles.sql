-- Built-in roles are seeded defaults, not immutable permission profiles.
-- Administrators may rename them and change their permissions; they remain
-- marked built-in and cannot be deleted so every installation keeps a useful
-- baseline role vocabulary.
ALTER TABLE roles ADD COLUMN built_in INTEGER NOT NULL DEFAULT 0;

INSERT OR IGNORE INTO roles(id,name,description,built_in,created_at,updated_at) VALUES
 ('builtin-tenant-owner','Tenant.Owner','Full administration of resources in an assigned tenant.',1,datetime('now'),datetime('now')),
 ('builtin-tenant-admin','Tenant.Admin','Manage resources in an assigned tenant, excluding access administration.',1,datetime('now'),datetime('now')),
 ('builtin-tenant-reader','Tenant.Reader','Read resources in an assigned tenant.',1,datetime('now'),datetime('now')),
 ('builtin-server-owner','Server.Owner','Full administration of an assigned server.',1,datetime('now'),datetime('now')),
 ('builtin-server-admin','Server.Admin','Manage an assigned server, excluding access administration.',1,datetime('now'),datetime('now')),
 ('builtin-server-operator','Server.Operator','Start and stop an assigned server.',1,datetime('now'),datetime('now')),
 ('builtin-server-reader','Server.Reader','Read an assigned server.',1,datetime('now'),datetime('now')),
 ('builtin-gamenode-admin','GameNode.Admin','Full administration of this GameNode installation.',1,datetime('now'),datetime('now')),
 ('builtin-gamenode-operator','GameNode.Operator','Operate servers and inspect their health on this GameNode.',1,datetime('now'),datetime('now')),
 ('builtin-gamenode-auditor','GameNode.Auditor','Read audit and operational information.',1,datetime('now'),datetime('now')),
 ('builtin-gamenode-reader','GameNode.Reader','Read all information exposed by this GameNode.',1,datetime('now'),datetime('now'));

WITH p(key) AS (VALUES
 ('Server.View'),('Server.Create'),('Server.Edit'),('Server.Delete'),('Server.Start'),('Server.Stop'),('Server.Restart'),('Server.Kill'),('Server.Update'),
 ('Console.View'),('Console.Send'),('Files.View'),('Files.Edit'),('Files.Upload'),('Files.Download'),('Files.Delete'),('Files.Rename'),
 ('FTP.View'),('FTP.Manage'),('TenantAccess.Manage'),('ServerAccess.Manage'),('Ports.View'),('Ports.Manage'),('Monitoring.View'),('Tenants.Invite'),('Cluster.View'),('Cluster.Schedule'),
 ('RemoteServer.View'),('RemoteServer.Manage'),('RemoteConsole.View'),('RemoteConsole.Send'),('RemoteFiles.View'),('RemoteFiles.Edit'),('RemoteFiles.Upload'),('RemoteFiles.Download'),('RemoteFiles.Delete'),('RemoteFiles.Rename'),('RemoteMonitoring.View'))
INSERT OR IGNORE INTO role_permissions(role_id,permission_key) SELECT 'builtin-tenant-owner', key FROM p;
INSERT OR IGNORE INTO role_permissions(role_id,permission_key) SELECT 'builtin-tenant-admin', permission_key FROM role_permissions WHERE role_id='builtin-tenant-owner' AND permission_key NOT IN ('TenantAccess.Manage','ServerAccess.Manage','Roles.View','Roles.Manage');
WITH p(key) AS (VALUES ('Server.View'),('Console.View'),('Files.View'),('Files.Download'),('FTP.View'),('Ports.View'),('Monitoring.View'),('RemoteServer.View'),('RemoteConsole.View'),('RemoteFiles.View'),('RemoteFiles.Download'),('RemoteMonitoring.View'))
INSERT OR IGNORE INTO role_permissions(role_id,permission_key) SELECT 'builtin-tenant-reader', key FROM p;
INSERT OR IGNORE INTO role_permissions(role_id,permission_key) SELECT 'builtin-server-owner', permission_key FROM role_permissions WHERE role_id='builtin-tenant-owner' AND permission_key NOT IN ('Server.Create','Tenants.Invite','Cluster.View','Cluster.Schedule');
INSERT OR IGNORE INTO role_permissions(role_id,permission_key) SELECT 'builtin-server-admin', permission_key FROM role_permissions WHERE role_id='builtin-server-owner' AND permission_key NOT IN ('ServerAccess.Manage','Roles.View','Roles.Manage');
INSERT OR IGNORE INTO role_permissions(role_id,permission_key) VALUES ('builtin-server-operator','Server.Start'),('builtin-server-operator','Server.Stop');
INSERT OR IGNORE INTO role_permissions(role_id,permission_key) VALUES ('builtin-server-reader','Server.View'),('builtin-server-reader','Console.View'),('builtin-server-reader','Files.View'),('builtin-server-reader','Files.Download'),('builtin-server-reader','FTP.View'),('builtin-server-reader','Ports.View'),('builtin-server-reader','Monitoring.View');
WITH p(key) AS (VALUES ('Users.View'),('Users.Manage'),('Groups.View'),('Groups.Manage'),('Roles.View'),('Roles.Manage'),('Settings.View'),('Settings.Manage'),('Log.Read'),('Log.FlushDirectory'),('Templates.View'),('Templates.Manage'),('Audit.View'),('Tenants.View'),('Tenants.Manage'),('Tenants.Invite'),('Node.View'),('Node.Manage'),('Cluster.View'),('Cluster.Schedule'),('Server.View'),('Server.Create'),('Server.Edit'),('Server.Delete'),('Server.Start'),('Server.Stop'),('Server.Restart'),('Server.Kill'),('Server.Update'),('Console.View'),('Console.Send'),('Files.View'),('Files.Edit'),('Files.Upload'),('Files.Download'),('Files.Delete'),('Files.Rename'),('FTP.View'),('FTP.Manage'),('Ports.View'),('Ports.Manage'),('Monitoring.View'),('RemoteServer.View'),('RemoteServer.Manage'),('RemoteConsole.View'),('RemoteConsole.Send'),('RemoteFiles.View'),('RemoteFiles.Edit'),('RemoteFiles.Upload'),('RemoteFiles.Download'),('RemoteFiles.Delete'),('RemoteFiles.Rename'),('RemoteMonitoring.View'))
INSERT OR IGNORE INTO role_permissions(role_id,permission_key) SELECT 'builtin-gamenode-admin', key FROM p;
INSERT OR IGNORE INTO role_permissions(role_id,permission_key) VALUES ('builtin-gamenode-operator','Server.View'),('builtin-gamenode-operator','Server.Start'),('builtin-gamenode-operator','Server.Stop'),('builtin-gamenode-operator','Server.Restart'),('builtin-gamenode-operator','Monitoring.View'),('builtin-gamenode-operator','Console.View');
INSERT OR IGNORE INTO role_permissions(role_id,permission_key) VALUES ('builtin-gamenode-auditor','Audit.View'),('builtin-gamenode-auditor','Log.Read'),('builtin-gamenode-auditor','Server.View'),('builtin-gamenode-auditor','Monitoring.View');
WITH p(key) AS (VALUES ('Users.View'),('Groups.View'),('Roles.View'),('Settings.View'),('Log.Read'),('Templates.View'),('Audit.View'),('Tenants.View'),('Node.View'),('Cluster.View'),('Server.View'),('Console.View'),('Files.View'),('Files.Download'),('FTP.View'),('Ports.View'),('Monitoring.View'),('RemoteServer.View'),('RemoteConsole.View'),('RemoteFiles.View'),('RemoteFiles.Download'),('RemoteMonitoring.View'))
INSERT OR IGNORE INTO role_permissions(role_id,permission_key) SELECT 'builtin-gamenode-reader', key FROM p;
