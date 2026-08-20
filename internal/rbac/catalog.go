package rbac

type Permission struct{ Key, Category, Description string }

var Catalog = []Permission{
	{"Server.View", "Server", "View servers"}, {"Server.Create", "Server", "Create servers"}, {"Server.Edit", "Server", "Edit servers"}, {"Server.Delete", "Server", "Delete servers"}, {"Server.Start", "Server", "Start servers"}, {"Server.Stop", "Server", "Stop servers"}, {"Server.Restart", "Server", "Restart servers"}, {"Server.Kill", "Server", "Kill servers"}, {"Server.Update", "Server", "Manually update installed SteamCMD server files"},
	{"Console.View", "Console", "View console"}, {"Console.Send", "Console", "Send console input"},
	{"Files.View", "Files", "View files"}, {"Files.Edit", "Files", "Edit files"}, {"Files.Upload", "Files", "Upload files"}, {"Files.Download", "Files", "Download files"}, {"Files.Delete", "Files", "Delete files"}, {"Files.Rename", "Files", "Rename files"},
	{"FTP.View", "FTP", "View a server's FTP connection status"}, {"FTP.Manage", "FTP", "Enable, disable, and rotate a server's FTP credentials"},
	{"TenantAccess.Manage", "Access", "Manage role assignments within an assigned tenant"}, {"ServerAccess.Manage", "Access", "Manage role assignments on an assigned server"},
	{"Ports.View", "Ports", "View server ports"}, {"Ports.Manage", "Ports", "Manage server ports"},
	{"Users.View", "Identity", "View users"}, {"Users.Manage", "Identity", "Manage users"}, {"Groups.View", "Identity", "View groups"}, {"Groups.Manage", "Identity", "Manage groups"}, {"Roles.View", "Identity", "View roles"}, {"Roles.Manage", "Identity", "Manage roles"}, {"Settings.View", "Platform", "View settings"}, {"Settings.Manage", "Platform", "Manage settings"},
	{"Log.Read", "Log", "Read current application log"}, {"Log.FlushDirectory", "Log", "Clear application log files"},
	{"Templates.View", "Templates", "View imported game templates"}, {"Templates.Manage", "Templates", "Analyze, import, and delete game templates"},
	{"Monitoring.View", "Monitoring", "View monitoring"}, {"Audit.View", "Audit", "View audit"},
	{"Tenants.View", "Tenants", "View tenant entities"}, {"Tenants.Manage", "Tenants", "Create, update, and delete tenant entities"}, {"Tenants.Invite", "Tenants", "Invite users to a tenant"},
	{"Node.View", "Node", "View this node's remote node registry and pairing status"}, {"Node.Manage", "Node", "Enroll, rename, enable/disable, and remove remote nodes; generate pairing tokens for this node"},
	{"Cluster.View", "Cluster", "View cluster placement candidates and capacity for a tenant"}, {"Cluster.Schedule", "Cluster", "Compute a cluster placement decision for a tenant"},
	// RemoteServer/RemoteConsole/RemoteFiles/RemoteMonitoring (v0.5B/v0.5C)
	// govern the controller-side forwarding surface against an enrolled
	// remote node's own servers - never local servers, which stay governed
	// by Server.*/Console.*/Files.*/Monitoring.View above. See
	// docs/adr/0010-remote-server-lifecycle-forwarding.md.
	{"RemoteServer.View", "RemoteServer", "View remote servers on enrolled nodes"}, {"RemoteServer.Manage", "RemoteServer", "Create, edit, delete, and control the lifecycle of remote servers on enrolled nodes"},
	{"RemoteConsole.View", "RemoteConsole", "View a remote server's console output"}, {"RemoteConsole.Send", "RemoteConsole", "Send input to a remote server's console"},
	{"RemoteFiles.View", "RemoteFiles", "View files on a remote server"}, {"RemoteFiles.Edit", "RemoteFiles", "Edit and create files on a remote server"}, {"RemoteFiles.Upload", "RemoteFiles", "Upload files to a remote server"}, {"RemoteFiles.Download", "RemoteFiles", "Download files from a remote server"}, {"RemoteFiles.Delete", "RemoteFiles", "Delete files on a remote server"}, {"RemoteFiles.Rename", "RemoteFiles", "Move and rename files on a remote server"},
	{"RemoteMonitoring.View", "RemoteMonitoring", "View monitoring data for a remote server"},
}

func Known(key string) bool {
	for _, p := range Catalog {
		if p.Key == key {
			return true
		}
	}
	return false
}

// GlobalOnly reports permissions that govern platform-wide administration
// rather than any particular tenant or server. They must never become
// effective through a tenant- or server-scoped assignment, even when a
// caller happens to evaluate such a scope.
//
// Tenants.View/Tenants.Manage administer tenant entities themselves (create,
// rename, delete a tenant) and are deliberately global-only: a tenant cannot
// be granted the right to manage tenants, including itself, and this is not
// the same thing as having access to resources inside one tenant. Server.Create
// is deliberately NOT listed here (see AllowedScopes): it supports a "tenant"
// grant so a tenant-scoped operator can create servers inside their own
// tenant, per GameNode_Tenant_Foundation_Prompt.md section 3.3.
func GlobalOnly(key string) bool {
	switch key {
	case "Users.View", "Users.Manage", "Groups.View", "Groups.Manage", "Roles.View", "Roles.Manage", "Settings.View", "Settings.Manage", "Log.Read", "Log.FlushDirectory", "Templates.View", "Templates.Manage", "Audit.View", "Tenants.View", "Tenants.Manage", "Node.View", "Node.Manage":
		return true
	// Cluster.View/Cluster.Schedule are deliberately NOT global-only: a
	// tenant-scoped operator must be able to request/inspect placement for
	// their own tenant without a global grant, matching Server.Create's
	// tenant-assignable convention. See AllowedScopes below.
	default:
		return false
	}
}

// AllowedScopes describes the assignment scopes that can make a permission
// effective:
//
//   - A global-only permission (see GlobalOnly) accepts only "global".
//   - Server.Create accepts "global" and "tenant" only. A server does not
//     exist yet at the moment Server.Create is evaluated, so a per-server
//     scope is meaningless; global means "create a server in any tenant",
//     tenant means "create a server only in this tenant".
//   - Every other permission accepts "global", "tenant", and "server": a
//     global or tenant grant applies to every server it reaches, while a
//     server grant applies only to that one server.
//
// This code is the source of truth. Nothing infers a permission's scopes
// from its category, name prefix, or any other convention; every entry was
// reviewed deliberately.
func AllowedScopes(key string) []string {
	if GlobalOnly(key) {
		return []string{"global"}
	}
	if key == "Server.Create" || key == "Cluster.View" || key == "Cluster.Schedule" {
		// A server does not exist yet when a placement decision is
		// requested (or when Server.Create is evaluated), so a per-server
		// scope is meaningless for either permission.
		return []string{"global", "tenant"}
	}
	if isRemoteServerPermission(key) {
		// Remote servers live in a remote node's own database, not this
		// installation's local servers table, so there is no local
		// per-remote-server assignment row to scope against (see
		// internal/api/remoteservers.go's tenant-based authorization
		// instead of a "server" scope). Only global and tenant grants are
		// meaningful.
		return []string{"global", "tenant"}
	}
	return []string{"global", "tenant", "server"}
}

func isRemoteServerPermission(key string) bool {
	switch key {
	case "RemoteServer.View", "RemoteServer.Manage", "RemoteConsole.View", "RemoteConsole.Send",
		"RemoteFiles.View", "RemoteFiles.Edit", "RemoteFiles.Upload", "RemoteFiles.Download", "RemoteFiles.Delete", "RemoteFiles.Rename", "RemoteMonitoring.View":
		return true
	default:
		return false
	}
}

// ScopeAllowed reports whether a permission may become effective through an
// assignment at the given scope type.
func ScopeAllowed(key, scopeType string) bool {
	for _, allowed := range AllowedScopes(key) {
		if allowed == scopeType {
			return true
		}
	}
	return false
}
