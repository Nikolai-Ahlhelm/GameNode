package database_test

import (
	"database/sql"
	"io/fs"
	"testing"

	"gamenode"
	"gamenode/internal/database"
)

func TestMigrateFreshAndFromPreIdentityState(t *testing.T) {
	for _, state := range []struct {
		name       string
		migrations []string
	}{
		{name: "fresh"},
		{name: "pre-identity", migrations: []string{"001_initial.sql", "002_servers.sql"}},
		{name: "pre-rbac", migrations: []string{"001_initial.sql", "002_servers.sql", "003_local_users_groups.sql"}},
		{name: "v0.1-upgrade", migrations: []string{"001_initial.sql", "002_servers.sql", "003_local_users_groups.sql", "004_rbac_core.sql", "005_rbac_global_assignment_uniqueness.sql", "006_monitoring_runtime_state.sql", "007_auto_restart_policy.sql", "008_server_ports.sql", "009_audit_log.sql", "010_app_settings.sql"}},
	} {
		t.Run(state.name, func(t *testing.T) {
			db, err := database.Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1)

			for _, migration := range state.migrations {
				applyMigration(t, db, migration)
			}
			if err := database.Migrate(db, gamenode.MigrationFiles); err != nil {
				t.Fatal(err)
			}

			var count int
			if err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
				t.Fatal(err)
			}
			entries, err := fs.ReadDir(gamenode.MigrationFiles, "migrations")
			if err != nil {
				t.Fatal(err)
			}
			if count != len(entries) {
				t.Fatalf("applied migrations = %d, want %d", count, len(entries))
			}
			for _, table := range []string{"groups", "group_memberships", "roles", "role_permissions", "user_role_assignments", "group_role_assignments", "game_templates", "game_template_variables", "game_template_findings", "provisioning_jobs", "server_template_variables"} {
				var name string
				if err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name); err != nil {
					t.Fatalf("missing table %s: %v", table, err)
				}
			}
		})
	}
}

func TestMigrateRepairsLegacyGlobalRoleDuplicates(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, migration := range []string{"001_initial.sql", "002_servers.sql", "003_local_users_groups.sql", "004_rbac_core.sql"} {
		applyMigration(t, db, migration)
	}
	if _, err := db.Exec(`INSERT INTO users(id,username,email,password_hash,is_admin,disabled,display_name,created_at,updated_at) VALUES('user','user','user@example.test','hash',0,0,'','test','test')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO roles(id,name,description,created_at,updated_at) VALUES('role','role','', 'test','test')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO user_role_assignments(id,user_id,role_id,scope_type,scope_id) VALUES('one','user','role','global',NULL),('two','user','role','global',NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	var assignments int
	if err := db.QueryRow("SELECT COUNT(*) FROM user_role_assignments WHERE user_id='user' AND role_id='role' AND scope_type='global'").Scan(&assignments); err != nil {
		t.Fatal(err)
	}
	if assignments != 1 {
		t.Fatalf("legacy global assignments = %d, want 1", assignments)
	}
}

func applyMigration(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	contents, err := fs.ReadFile(gamenode.MigrationFiles, "migrations/"+name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(contents)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO schema_migrations(version, applied_at) VALUES(?, 'test')", name); err != nil {
		t.Fatal(err)
	}
}
