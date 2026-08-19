package rbac

type Permission struct{ Key, Category, Description string }

var Catalog = []Permission{
	{"Server.View", "Server", "View servers"}, {"Server.Create", "Server", "Create servers"}, {"Server.Edit", "Server", "Edit servers"}, {"Server.Delete", "Server", "Delete servers"}, {"Server.Start", "Server", "Start servers"}, {"Server.Stop", "Server", "Stop servers"}, {"Server.Restart", "Server", "Restart servers"}, {"Server.Kill", "Server", "Kill servers"}, {"Server.Update", "Server", "Manually update installed SteamCMD server files"},
	{"Console.View", "Console", "View console"}, {"Console.Send", "Console", "Send console input"},
	{"Files.View", "Files", "View files"}, {"Files.Edit", "Files", "Edit files"}, {"Files.Upload", "Files", "Upload files"}, {"Files.Download", "Files", "Download files"}, {"Files.Delete", "Files", "Delete files"}, {"Files.Rename", "Files", "Rename files"},
	{"Ports.View", "Ports", "View server ports"}, {"Ports.Manage", "Ports", "Manage server ports"},
	{"Users.View", "Identity", "View users"}, {"Users.Manage", "Identity", "Manage users"}, {"Groups.View", "Identity", "View groups"}, {"Groups.Manage", "Identity", "Manage groups"}, {"Roles.View", "Identity", "View roles"}, {"Roles.Manage", "Identity", "Manage roles"}, {"Settings.View", "Platform", "View settings"}, {"Settings.Manage", "Platform", "Manage settings"},
	{"Log.Read", "Log", "Read current application log"}, {"Log.FlushDirectory", "Log", "Clear application log files"},
	{"Templates.View", "Templates", "View imported game templates"}, {"Templates.Manage", "Templates", "Analyze, import, and delete game templates"},
	{"Monitoring.View", "Monitoring", "View monitoring"}, {"Audit.View", "Audit", "View audit"},
	{"Tenants.View", "Tenants", "View tenant entities"}, {"Tenants.Manage", "Tenants", "Create, update, and delete tenant entities"},
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
	case "Users.View", "Users.Manage", "Groups.View", "Groups.Manage", "Roles.View", "Roles.Manage", "Settings.View", "Settings.Manage", "Log.Read", "Log.FlushDirectory", "Templates.View", "Templates.Manage", "Audit.View", "Tenants.View", "Tenants.Manage":
		return true
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
	if key == "Server.Create" {
		return []string{"global", "tenant"}
	}
	return []string{"global", "tenant", "server"}
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
