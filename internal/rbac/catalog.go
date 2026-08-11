package rbac

type Permission struct{ Key, Category, Description string }

var Catalog = []Permission{
	{"Server.View", "Server", "View servers"}, {"Server.Create", "Server", "Create servers"}, {"Server.Edit", "Server", "Edit servers"}, {"Server.Delete", "Server", "Delete servers"}, {"Server.Start", "Server", "Start servers"}, {"Server.Stop", "Server", "Stop servers"}, {"Server.Restart", "Server", "Restart servers"}, {"Server.Kill", "Server", "Kill servers"},
	{"Console.View", "Console", "View console"}, {"Console.Send", "Console", "Send console input"},
	{"Files.View", "Files", "View files"}, {"Files.Edit", "Files", "Edit files"}, {"Files.Upload", "Files", "Upload files"}, {"Files.Download", "Files", "Download files"}, {"Files.Delete", "Files", "Delete files"}, {"Files.Rename", "Files", "Rename files"},
	{"Users.View", "Identity", "View users"}, {"Users.Manage", "Identity", "Manage users"}, {"Groups.View", "Identity", "View groups"}, {"Groups.Manage", "Identity", "Manage groups"}, {"Roles.View", "Identity", "View roles"}, {"Roles.Manage", "Identity", "Manage roles"}, {"Settings.View", "Platform", "View settings"}, {"Settings.Manage", "Platform", "Manage settings"},
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
