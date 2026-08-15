package database_test

import (
	"database/sql"
	"io/fs"
	"path/filepath"
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
		{name: "pre-provisioning-reliability", migrations: []string{"001_initial.sql", "002_servers.sql", "003_local_users_groups.sql", "004_rbac_core.sql", "005_rbac_global_assignment_uniqueness.sql", "006_monitoring_runtime_state.sql", "007_auto_restart_policy.sql", "008_server_ports.sql", "009_audit_log.sql", "010_app_settings.sql", "011_game_templates.sql", "012_steamcmd_provisioning.sql", "013_template_provenance.sql", "014_server_config_adapters.sql"}},
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
			for _, table := range []string{"groups", "group_memberships", "roles", "role_permissions", "user_role_assignments", "group_role_assignments", "game_templates", "game_template_variables", "game_template_findings", "provisioning_jobs", "provisioning_job_events", "server_template_variables", "server_config_adapters"} {
				var name string
				if err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name); err != nil {
					t.Fatalf("missing table %s: %v", table, err)
				}
			}
			for _, column := range []string{"current_phase", "last_successful_phase", "failure_phase", "failure_code", "installation_completed", "registration_recoverable", "registration_snapshot_json"} {
				var found int
				if err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('provisioning_jobs') WHERE name=?", column).Scan(&found); err != nil || found != 1 {
					t.Fatalf("provisioning_jobs column %s: found=%d err=%v", column, found, err)
				}
			}
		})
	}
}

func TestBackupIfMigrationPending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gamenode.db")
	db, err := database.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	applyMigration(t, db, "001_initial.sql")
	backup, pending, err := database.BackupIfMigrationPending(db, path, gamenode.MigrationFiles)
	if err != nil || !pending || backup == "" {
		t.Fatalf("backup = %q, pending = %v, err = %v", backup, pending, err)
	}
	backupDB, err := database.Open(backup)
	if err != nil {
		t.Fatal(err)
	}
	defer backupDB.Close()
	var count int
	if err = backupDB.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil || count != 1 {
		t.Fatalf("backup migration count = %d, %v", count, err)
	}
	if _, pending, err = database.BackupIfMigrationPending(db, path, gamenode.MigrationFiles); err != nil || !pending {
		t.Fatalf("pending after backup = %v, %v", pending, err)
	}
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	if backup, pending, err = database.BackupIfMigrationPending(db, path, gamenode.MigrationFiles); err != nil || pending || backup != "" {
		t.Fatalf("unexpected backup after migration: %q, %v, %v", backup, pending, err)
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

func TestMigration018PreservesProvisioningJobsAndEvents(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	entries, err := fs.ReadDir(gamenode.MigrationFiles, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == "018_provisioning_status_phases.sql" {
			break
		}
		applyMigration(t, db, entry.Name())
	}
	if _, err = db.Exec(`INSERT INTO provisioning_jobs(id,actor_user_id,template_id,template_name,server_name,directory_name,installer_type,app_id,status,summary,created_at,updated_at,current_phase) VALUES('job','actor','template','Template','Server','server','steamcmd',1,'installing','Installing','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','installing')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO provisioning_job_events(job_id,occurred_at,phase,code,summary) VALUES('job','2026-01-01T00:00:00Z','installing','PHASE_CHANGED','Installing')`); err != nil {
		t.Fatal(err)
	}
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE provisioning_jobs SET status='registering_server',current_phase='registering_server' WHERE id='job'`); err != nil {
		t.Fatalf("new status rejected after migration: %v", err)
	}
	var events int
	if err = db.QueryRow(`SELECT COUNT(*) FROM provisioning_job_events WHERE job_id='job' AND phase='installing'`).Scan(&events); err != nil || events != 1 {
		t.Fatalf("preserved events=%d err=%v", events, err)
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

// TestMigration019AddsManagedConfigurationValuesOnUpgrade applies every earlier
// migration first, so the new managed configuration store is verified on an
// upgraded database rather than only on a fresh one.
func TestMigration019AddsManagedConfigurationValuesOnUpgrade(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	entries, err := fs.ReadDir(gamenode.MigrationFiles, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == "019_server_config_values.sql" {
			break
		}
		applyMigration(t, db, entry.Name())
	}
	if _, err = db.Exec(`INSERT INTO servers(id,creation_mode,name,working_directory,executable,created_at,updated_at) VALUES('server','template','Existing','/tmp/existing','game.exe','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`SELECT 1 FROM server_config_values`); err == nil {
		t.Fatal("server_config_values must not exist before migration 019")
	}
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO server_config_values(server_id,adapter_id,field_key,value,sensitive,created_at,updated_at) VALUES('server','valheim-settings','SERVER_NAME','My Valheim',0,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("managed value insert failed after upgrade: %v", err)
	}
	if _, err = db.Exec(`INSERT INTO server_config_values(server_id,adapter_id,field_key,value,sensitive,created_at,updated_at) VALUES('server','valheim-settings','SERVER_NAME','Duplicate',0,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err == nil {
		t.Fatal("the primary key must reject a duplicate field")
	}
	if _, err = db.Exec(`DELETE FROM servers WHERE id='server'`); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err = db.QueryRow(`SELECT COUNT(*) FROM server_config_values`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("managed values must cascade with the server: %d %v", remaining, err)
	}
}
