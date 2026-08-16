-- Tenant Foundation, Step 1: persistent tenants, tenant memberships, and
-- mandatory server ownership. See docs/architecture.md and
-- GameNode_Tenant_Foundation_Prompt.md.
--
-- Membership alone grants no RBAC permission; it only records that a user
-- belongs to a tenant. Deleting a tenant is left to internal/tenants, which
-- refuses to delete a tenant that still owns servers.
CREATE TABLE tenants (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL COLLATE NOCASE UNIQUE,
    slug TEXT NOT NULL COLLATE NOCASE UNIQUE,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE tenant_memberships (
    tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL,
    PRIMARY KEY (tenant_id, user_id)
);
CREATE INDEX tenant_memberships_user_idx ON tenant_memberships(user_id);

-- A stable, fixed (not randomly generated) default tenant so every existing
-- installation upgrades to exactly one well-known owner for its current
-- servers. internal/tenants.DefaultTenantID must match this literal.
INSERT INTO tenants (id, name, slug, created_at, updated_at)
VALUES ('default', 'Default', 'default', '1970-01-01T00:00:00.000000000Z', '1970-01-01T00:00:00.000000000Z');

-- servers.tenant_id must become NOT NULL with a foreign key to tenants(id),
-- and every existing server must be backfilled to the default tenant first.
-- SQLite's ALTER TABLE cannot add a REFERENCES column with a non-NULL
-- default in one step ("Cannot add a REFERENCES column with non-NULL
-- default value"), so this follows the table-rebuild pattern already used by
-- 018_provisioning_status_phases.sql: build the new shape, copy the data,
-- drop the old table, and rename the new one into place.
--
-- Unlike 018, other live tables (server_runtime_state, server_ports,
-- server_template_variables, server_config_adapters, server_config_values,
-- user_role_assignments, group_role_assignments) hold foreign keys straight
-- into servers(id) with ON DELETE CASCADE. SQLite performs an implicit
-- cascading DELETE when a referenced parent table is dropped while foreign
-- key enforcement is on, which would silently destroy all of that unrelated
-- data. internal/database.Migrate applies every migration file with foreign
-- key enforcement temporarily disabled on its dedicated connection for
-- exactly this reason, and verifies referential integrity with
-- PRAGMA foreign_key_check before turning it back on.
CREATE TABLE servers_v020 (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL REFERENCES tenants(id),
    creation_mode TEXT NOT NULL,
    name TEXT NOT NULL COLLATE NOCASE UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    working_directory TEXT NOT NULL,
    executable TEXT NOT NULL,
    arguments_json TEXT NOT NULL DEFAULT '[]',
    environment_json TEXT NOT NULL DEFAULT '{}',
    runtime_type TEXT NOT NULL DEFAULT 'native',
    auto_start INTEGER NOT NULL DEFAULT 0,
    restart_policy TEXT NOT NULL DEFAULT 'never',
    stop_method TEXT NOT NULL DEFAULT 'terminate',
    stop_command TEXT NOT NULL DEFAULT '',
    stop_timeout_seconds INTEGER NOT NULL DEFAULT 15,
    auto_restart_enabled INTEGER NOT NULL DEFAULT 0,
    auto_restart_max_attempts INTEGER NOT NULL DEFAULT 3,
    auto_restart_window_seconds INTEGER NOT NULL DEFAULT 300,
    auto_restart_delay_seconds INTEGER NOT NULL DEFAULT 5,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

INSERT INTO servers_v020 (
    id, tenant_id, creation_mode, name, description, working_directory,
    executable, arguments_json, environment_json, runtime_type, auto_start,
    restart_policy, stop_method, stop_command, stop_timeout_seconds,
    auto_restart_enabled, auto_restart_max_attempts,
    auto_restart_window_seconds, auto_restart_delay_seconds, created_at,
    updated_at
)
SELECT
    id, 'default', creation_mode, name, description, working_directory,
    executable, arguments_json, environment_json, runtime_type, auto_start,
    restart_policy, stop_method, stop_command, stop_timeout_seconds,
    auto_restart_enabled, auto_restart_max_attempts,
    auto_restart_window_seconds, auto_restart_delay_seconds, created_at,
    updated_at
FROM servers;

DROP TABLE servers;
ALTER TABLE servers_v020 RENAME TO servers;

CREATE INDEX servers_name_idx ON servers(name);
CREATE INDEX servers_tenant_id_idx ON servers(tenant_id);
