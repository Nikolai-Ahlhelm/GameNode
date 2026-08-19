-- v0.2.1: manual SteamCMD server updates.
--
-- server_steamcmd_provisioning is the minimum trusted, immutable metadata
-- GameNode needs to safely re-run SteamCMD against an already-provisioned
-- server's existing root: App ID, login mode, default validate behavior, and
-- the template provenance that produced it. It is written exactly once, in
-- the same transaction as the server row (see servers.Store.CreateProvisioned
-- and internal/provisioning), only for servers provisioned through the
-- Official SteamCMD installer path. Custom/adopted servers and servers
-- provisioned before this migration have no row here and are therefore
-- reported as not updateable through GameNode rather than guessed from
-- directory contents or a freshly re-resolved template.
CREATE TABLE server_steamcmd_provisioning (
    server_id TEXT PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    installer_type TEXT NOT NULL,
    app_id INTEGER NOT NULL,
    login_mode TEXT NOT NULL DEFAULT 'anonymous',
    validate_default INTEGER NOT NULL DEFAULT 0,
    beta_branch TEXT NOT NULL DEFAULT '',
    template_id TEXT NOT NULL,
    template_version TEXT NOT NULL,
    template_source TEXT NOT NULL,
    created_at TEXT NOT NULL
);

-- server_update_jobs / server_update_job_events mirror provisioning_jobs /
-- provisioning_job_events (see 018_provisioning_status_phases.sql), scoped to
-- the much smaller manual-update flow. Only safe, bounded state is persisted:
-- never raw SteamCMD output, command lines, secrets, or absolute host paths.
CREATE TABLE server_update_jobs (
    id TEXT PRIMARY KEY,
    server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    tenant_id TEXT NOT NULL DEFAULT 'default',
    actor_user_id TEXT NOT NULL,
    actor_username TEXT NOT NULL DEFAULT '',
    template_id TEXT NOT NULL,
    template_version TEXT NOT NULL,
    app_id INTEGER NOT NULL,
    validate INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL CHECK(status IN ('pending','preparing','downloading_steamcmd','steamcmd_ready','updating','steamcmd_completed','validating_installation','completed','failed','cancelled')),
    current_phase TEXT NOT NULL DEFAULT 'pending',
    summary TEXT NOT NULL,
    error_summary TEXT NOT NULL DEFAULT '',
    error_code TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    started_at TEXT,
    completed_at TEXT,
    updated_at TEXT NOT NULL
);
CREATE INDEX server_update_jobs_server_idx ON server_update_jobs(server_id, created_at DESC);
CREATE INDEX server_update_jobs_status_idx ON server_update_jobs(status);

CREATE TABLE server_update_job_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id TEXT NOT NULL REFERENCES server_update_jobs(id) ON DELETE CASCADE,
    occurred_at TEXT NOT NULL,
    phase TEXT NOT NULL,
    code TEXT NOT NULL,
    summary TEXT NOT NULL
);
CREATE INDEX server_update_job_events_job_idx ON server_update_job_events(job_id, id);
