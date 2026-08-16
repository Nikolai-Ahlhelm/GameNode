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

// TestMigration020AssignsExistingServersToDefaultTenant applies every
// earlier migration first and inserts several pre-tenant servers (the shape
// migrations 002-019 produce), so the tenant backfill is verified on an
// upgraded database with existing servers rather than only a fresh one.
func TestMigration020AssignsExistingServersToDefaultTenant(t *testing.T) {
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
		if entry.Name() == "020_tenants.sql" {
			break
		}
		applyMigration(t, db, entry.Name())
	}
	if _, err = db.Exec(`SELECT 1 FROM tenants`); err == nil {
		t.Fatal("tenants must not exist before migration 020")
	}
	for _, id := range []string{"legacy-1", "legacy-2", "legacy-3"} {
		if _, err = db.Exec(`INSERT INTO servers(id,creation_mode,name,working_directory,executable,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, id, "custom", "Server "+id, "/tmp/"+id, "game.exe", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	// Also verify a server's runtime state, ports, and RBAC server
	// assignment - all foreign keys into servers(id) - survive the rebuild
	// untouched.
	if _, err = db.Exec(`INSERT INTO server_ports(id,server_id,name,protocol,bind_address,port,created_at,updated_at) VALUES('port-1','legacy-1','Game','tcp','',25565,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO users(id,username,email,password_hash,is_admin,disabled,display_name,created_at,updated_at) VALUES('user-1','user1','user1@example.test','hash',0,0,'','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO roles(id,name,description,created_at,updated_at) VALUES('role-1','Role1','','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO user_role_assignments(id,user_id,role_id,scope_type,scope_id) VALUES('assign-1','user-1','role-1','server','legacy-1')`); err != nil {
		t.Fatal(err)
	}

	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}

	var tenantCount int
	if err = db.QueryRow(`SELECT COUNT(*) FROM tenants`).Scan(&tenantCount); err != nil {
		t.Fatal(err)
	}
	if tenantCount != 1 {
		t.Fatalf("tenant count after upgrade = %d, want exactly 1 default tenant", tenantCount)
	}
	var defaultTenantID string
	if err = db.QueryRow(`SELECT id FROM tenants`).Scan(&defaultTenantID); err != nil {
		t.Fatal(err)
	}
	if defaultTenantID != "default" {
		t.Fatalf("default tenant id = %q, want %q", defaultTenantID, "default")
	}
	var untenanted int
	if err = db.QueryRow(`SELECT COUNT(*) FROM servers WHERE tenant_id IS NULL OR tenant_id=''`).Scan(&untenanted); err != nil {
		t.Fatal(err)
	}
	if untenanted != 0 {
		t.Fatalf("servers without a tenant after upgrade = %d, want 0", untenanted)
	}
	var assigned int
	if err = db.QueryRow(`SELECT COUNT(*) FROM servers WHERE tenant_id=?`, defaultTenantID).Scan(&assigned); err != nil {
		t.Fatal(err)
	}
	if assigned != 3 {
		t.Fatalf("legacy servers assigned to default tenant = %d, want 3", assigned)
	}

	// tenant_id must be enforced NOT NULL for newly inserted servers, not
	// only backfilled for pre-existing ones.
	if _, err = db.Exec(`INSERT INTO servers(id,creation_mode,name,working_directory,executable,created_at,updated_at) VALUES('no-tenant','custom','No Tenant','/tmp/x','game.exe','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err == nil {
		t.Fatal("insert without tenant_id must fail after migration 020")
	}
	if _, err = db.Exec(`INSERT INTO servers(id,tenant_id,creation_mode,name,working_directory,executable,created_at,updated_at) VALUES('bad-tenant','does-not-exist','custom','Bad Tenant','/tmp/x','game.exe','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err == nil {
		t.Fatal("insert with an unknown tenant_id must fail after migration 020")
	}

	// Child rows that reference servers(id) must have survived the table
	// rebuild untouched, and cascade delete must still work afterward.
	var portName string
	if err = db.QueryRow(`SELECT server_id FROM server_ports WHERE id='port-1'`).Scan(&portName); err != nil {
		t.Fatalf("server_ports row lost during tenant migration: %v", err)
	}
	var assignmentScope string
	if err = db.QueryRow(`SELECT scope_id FROM user_role_assignments WHERE id='assign-1'`).Scan(&assignmentScope); err != nil || assignmentScope != "legacy-1" {
		t.Fatalf("user_role_assignments row lost during tenant migration: scope=%q err=%v", assignmentScope, err)
	}
	if _, err = db.Exec(`DELETE FROM servers WHERE id='legacy-1'`); err != nil {
		t.Fatal(err)
	}
	var remainingPorts, remainingAssignments int
	if err = db.QueryRow(`SELECT COUNT(*) FROM server_ports WHERE id='port-1'`).Scan(&remainingPorts); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM user_role_assignments WHERE id='assign-1'`).Scan(&remainingAssignments); err != nil {
		t.Fatal(err)
	}
	if remainingPorts != 0 || remainingAssignments != 0 {
		t.Fatalf("cascade delete not restored after tenant migration: ports=%d assignments=%d", remainingPorts, remainingAssignments)
	}
}

func TestMigrateFreshDatabaseHasExactlyOneDefaultTenant(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM tenants`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("tenant count on fresh database = %d, want 1", count)
	}
	var id, slug string
	if err = db.QueryRow(`SELECT id, slug FROM tenants`).Scan(&id, &slug); err != nil {
		t.Fatal(err)
	}
	if id != "default" || slug != "default" {
		t.Fatalf("default tenant = (%q, %q), want (\"default\", \"default\")", id, slug)
	}
}

// TestMigration021AssignsExistingProvisioningJobsToDefaultTenant applies
// every earlier migration first and inserts a pre-tenant provisioning job
// (the shape migrations before 021 produce), so the backfill is verified on
// an upgraded database with an existing job rather than only a fresh one.
func TestMigration021AssignsExistingProvisioningJobsToDefaultTenant(t *testing.T) {
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
		if entry.Name() == "021_provisioning_tenant.sql" {
			break
		}
		applyMigration(t, db, entry.Name())
	}
	if _, err = db.Exec(`INSERT INTO provisioning_jobs(id,actor_user_id,template_id,template_name,server_name,directory_name,installer_type,app_id,status,summary,created_at,updated_at,current_phase) VALUES('legacy-job','actor','template','Template','Server','server','steamcmd',1,'installing','Installing','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','installing')`); err != nil {
		t.Fatal(err)
	}
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	var tenantID string
	if err = db.QueryRow(`SELECT tenant_id FROM provisioning_jobs WHERE id='legacy-job'`).Scan(&tenantID); err != nil {
		t.Fatal(err)
	}
	if tenantID != "default" {
		t.Fatalf("legacy provisioning job tenant_id = %q, want %q", tenantID, "default")
	}
	if _, err = db.Exec(`INSERT INTO provisioning_jobs(id,actor_user_id,template_id,template_name,server_name,directory_name,installer_type,app_id,status,summary,created_at,updated_at,current_phase) VALUES('new-job','actor','template','Template','Server','server2','steamcmd',1,'installing','Installing','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','installing')`); err != nil {
		t.Fatal(err)
	}
	var newTenantID string
	if err = db.QueryRow(`SELECT tenant_id FROM provisioning_jobs WHERE id='new-job'`).Scan(&newTenantID); err != nil || newTenantID != "default" {
		t.Fatalf("new provisioning job tenant_id = %q, want %q (err=%v)", newTenantID, "default", err)
	}
}

// TestMigration022PreservesAssignmentsAndEnforcesPerScopeUniqueness applies
// every earlier migration first and inserts pre-tenant-scope global and
// server assignments (the shape migrations before 022 produce), so the
// rebuild is verified on an upgraded database with existing assignments
// rather than only a fresh one. It also proves the per-scope-type partial
// unique indexes correctly reject a duplicate for all three scope types -
// the naive single composite UNIQUE(...,scope_id,tenant_scope_id) this
// migration deliberately avoids would silently stop rejecting duplicates for
// every scope type, since each type always leaves one of those two columns
// NULL.
func TestMigration022PreservesAssignmentsAndEnforcesPerScopeUniqueness(t *testing.T) {
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
		if entry.Name() == "022_rbac_tenant_scope.sql" {
			break
		}
		applyMigration(t, db, entry.Name())
	}
	if _, err = db.Exec(`INSERT INTO users(id,username,email,password_hash,is_admin,disabled,display_name,created_at,updated_at) VALUES('user-1','user1','user1@example.test','hash',0,0,'','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO groups(id,name,description,created_at,updated_at) VALUES('group-1','group1','','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO roles(id,name,description,created_at,updated_at) VALUES('role-1','role1','','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO servers(id,tenant_id,creation_mode,name,working_directory,executable,created_at,updated_at) VALUES('server-1','default','custom','Server','/tmp','exe','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO user_role_assignments(id,user_id,role_id,scope_type,scope_id) VALUES('legacy-global','user-1','role-1','global',NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO user_role_assignments(id,user_id,role_id,scope_type,scope_id) VALUES('legacy-server','user-1','role-1','server','server-1')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO group_role_assignments(id,group_id,role_id,scope_type,scope_id) VALUES('legacy-group-server','group-1','role-1','server','server-1')`); err != nil {
		t.Fatal(err)
	}

	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}

	var scopeType string
	var scopeID, tenantScopeID sql.NullString
	if err = db.QueryRow(`SELECT scope_type,scope_id,tenant_scope_id FROM user_role_assignments WHERE id='legacy-global'`).Scan(&scopeType, &scopeID, &tenantScopeID); err != nil {
		t.Fatal(err)
	}
	if scopeType != "global" || scopeID.Valid || tenantScopeID.Valid {
		t.Fatalf("legacy global assignment after migration: type=%q scope_id=%v tenant_scope_id=%v", scopeType, scopeID, tenantScopeID)
	}
	if err = db.QueryRow(`SELECT scope_type,scope_id,tenant_scope_id FROM user_role_assignments WHERE id='legacy-server'`).Scan(&scopeType, &scopeID, &tenantScopeID); err != nil {
		t.Fatal(err)
	}
	if scopeType != "server" || !scopeID.Valid || scopeID.String != "server-1" || tenantScopeID.Valid {
		t.Fatalf("legacy server assignment after migration: type=%q scope_id=%v tenant_scope_id=%v", scopeType, scopeID, tenantScopeID)
	}
	if err = db.QueryRow(`SELECT scope_type,scope_id,tenant_scope_id FROM group_role_assignments WHERE id='legacy-group-server'`).Scan(&scopeType, &scopeID, &tenantScopeID); err != nil {
		t.Fatal(err)
	}
	if scopeType != "server" || !scopeID.Valid || scopeID.String != "server-1" || tenantScopeID.Valid {
		t.Fatalf("legacy group server assignment after migration: type=%q scope_id=%v tenant_scope_id=%v", scopeType, scopeID, tenantScopeID)
	}

	// Duplicate global and duplicate server assignments must still be
	// rejected exactly as before the rebuild.
	if _, err = db.Exec(`INSERT INTO user_role_assignments(id,user_id,role_id,scope_type,scope_id) VALUES('dup-global','user-1','role-1','global',NULL)`); err == nil {
		t.Fatal("duplicate global assignment accepted after migration 022")
	}
	if _, err = db.Exec(`INSERT INTO user_role_assignments(id,user_id,role_id,scope_type,scope_id) VALUES('dup-server','user-1','role-1','server','server-1')`); err == nil {
		t.Fatal("duplicate server assignment accepted after migration 022")
	}

	// A new tenant-scoped assignment can now be inserted, and a duplicate is
	// rejected the same way.
	if _, err = db.Exec(`INSERT INTO tenants(id,name,slug,created_at,updated_at) VALUES('tenant-1','Tenant One','tenant-one','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO user_role_assignments(id,user_id,role_id,scope_type,tenant_scope_id) VALUES('new-tenant','user-1','role-1','tenant','tenant-1')`); err != nil {
		t.Fatalf("tenant-scoped assignment rejected after migration 022: %v", err)
	}
	if _, err = db.Exec(`INSERT INTO user_role_assignments(id,user_id,role_id,scope_type,tenant_scope_id) VALUES('dup-tenant','user-1','role-1','tenant','tenant-1')`); err == nil {
		t.Fatal("duplicate tenant assignment accepted after migration 022")
	}
	// An unknown tenant_scope_id is rejected by the foreign key.
	if _, err = db.Exec(`INSERT INTO user_role_assignments(id,user_id,role_id,scope_type,tenant_scope_id) VALUES('bad-tenant','user-1','role-1','tenant','does-not-exist')`); err == nil {
		t.Fatal("tenant assignment with an unknown tenant accepted after migration 022")
	}

	// Deleting the tenant cascades its tenant-scoped assignment away, just
	// as deleting the server already cascaded server-scoped assignments.
	if _, err = db.Exec(`DELETE FROM tenants WHERE id='tenant-1'`); err != nil {
		t.Fatal(err)
	}
	var remainingTenantAssignments int
	if err = db.QueryRow(`SELECT COUNT(*) FROM user_role_assignments WHERE id='new-tenant'`).Scan(&remainingTenantAssignments); err != nil || remainingTenantAssignments != 0 {
		t.Fatalf("tenant-scoped assignment not cascaded on tenant delete: count=%d err=%v", remainingTenantAssignments, err)
	}
	if _, err = db.Exec(`DELETE FROM servers WHERE id='server-1'`); err != nil {
		t.Fatal(err)
	}
	var remainingServerAssignments int
	if err = db.QueryRow(`SELECT COUNT(*) FROM user_role_assignments WHERE id='legacy-server'`).Scan(&remainingServerAssignments); err != nil || remainingServerAssignments != 0 {
		t.Fatalf("server-scoped assignment not cascaded on server delete: count=%d err=%v", remainingServerAssignments, err)
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
