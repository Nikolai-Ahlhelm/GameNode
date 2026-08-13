package rbac

type Permission struct{ Key, Category, Description string }

var Catalog = []Permission{
	{"Server.View", "Server", "View servers"}, {"Server.Create", "Server", "Create servers"}, {"Server.Edit", "Server", "Edit servers"}, {"Server.Delete", "Server", "Delete servers"}, {"Server.Start", "Server", "Start servers"}, {"Server.Stop", "Server", "Stop servers"}, {"Server.Restart", "Server", "Restart servers"}, {"Server.Kill", "Server", "Kill servers"},
	{"Console.View", "Console", "View console"}, {"Console.Send", "Console", "Send console input"},
	{"Files.View", "Files", "View files"}, {"Files.Edit", "Files", "Edit files"}, {"Files.Upload", "Files", "Upload files"}, {"Files.Download", "Files", "Download files"}, {"Files.Delete", "Files", "Delete files"}, {"Files.Rename", "Files", "Rename files"},
	{"Ports.View", "Ports", "View server ports"}, {"Ports.Manage", "Ports", "Manage server ports"},
	{"Users.View", "Identity", "View users"}, {"Users.Manage", "Identity", "Manage users"}, {"Groups.View", "Identity", "View groups"}, {"Groups.Manage", "Identity", "Manage groups"}, {"Roles.View", "Identity", "View roles"}, {"Roles.Manage", "Identity", "Manage roles"}, {"Settings.View", "Platform", "View settings"}, {"Settings.Manage", "Platform", "Manage settings"},
	{"Log.Read", "Log", "Read current application log"}, {"Log.FlushDirectory", "Log", "Clear application log files"},
	{"Templates.View", "Templates", "View imported game templates"}, {"Templates.Manage", "Templates", "Analyze, import, and delete game templates"},
	{"Monitoring.View", "Monitoring", "View monitoring"}, {"Audit.View", "Audit", "View audit"},
}

func Known(key string) bool {
	for _, p := range Catalog {
		if p.Key == key {
			return true
		}
	}
	return false
}

// GlobalOnly reports permissions that govern platform management rather than a
// particular server. They must never become effective through a server-scoped
// assignment, even when a caller happens to evaluate a server scope.
func GlobalOnly(key string) bool {
	switch key {
	case "Server.Create", "Users.View", "Users.Manage", "Groups.View", "Groups.Manage", "Roles.View", "Roles.Manage", "Settings.View", "Settings.Manage", "Log.Read", "Log.FlushDirectory", "Templates.View", "Templates.Manage", "Audit.View":
		return true
	default:
		return false
	}
}
