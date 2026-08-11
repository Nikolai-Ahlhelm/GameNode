CREATE TABLE provisioning_jobs (
    id TEXT PRIMARY KEY,
    actor_user_id TEXT NOT NULL,
    actor_username TEXT NOT NULL DEFAULT '',
    template_id TEXT NOT NULL,
    template_name TEXT NOT NULL,
    server_name TEXT NOT NULL,
    directory_name TEXT NOT NULL,
    installer_type TEXT NOT NULL,
    app_id INTEGER NOT NULL,
    status TEXT NOT NULL CHECK(status IN ('pending','preparing','downloading_steamcmd','steamcmd_ready','installing','creating_server','completed','failed','cancelled')),
    summary TEXT NOT NULL,
    error_summary TEXT NOT NULL DEFAULT '',
    files_may_remain INTEGER NOT NULL DEFAULT 0,
    server_id TEXT,
    created_at TEXT NOT NULL,
    started_at TEXT,
    completed_at TEXT,
    updated_at TEXT NOT NULL
);
CREATE INDEX provisioning_jobs_actor_idx ON provisioning_jobs(actor_user_id,created_at DESC);
CREATE INDEX provisioning_jobs_status_idx ON provisioning_jobs(status);

CREATE TABLE server_template_variables (
    server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    template_id TEXT NOT NULL,
    variable_key TEXT NOT NULL,
    sensitive INTEGER NOT NULL,
    PRIMARY KEY(server_id,variable_key)
);
CREATE INDEX server_template_variables_template_idx ON server_template_variables(template_id);
