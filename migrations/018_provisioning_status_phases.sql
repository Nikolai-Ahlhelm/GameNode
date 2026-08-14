CREATE TABLE provisioning_jobs_v018 (
    id TEXT PRIMARY KEY,
    actor_user_id TEXT NOT NULL,
    actor_username TEXT NOT NULL DEFAULT '',
    template_id TEXT NOT NULL,
    template_name TEXT NOT NULL,
    server_name TEXT NOT NULL,
    directory_name TEXT NOT NULL,
    installer_type TEXT NOT NULL,
    app_id INTEGER NOT NULL,
    status TEXT NOT NULL CHECK(status IN ('pending','preparing','downloading_steamcmd','steamcmd_ready','installing','steamcmd_completed','validating_installation','installation_validated','resolving_launch','registering_server','server_registered','creating_server','completed','failed','cancelled')),
    summary TEXT NOT NULL,
    error_summary TEXT NOT NULL DEFAULT '',
    files_may_remain INTEGER NOT NULL DEFAULT 0,
    server_id TEXT,
    created_at TEXT NOT NULL,
    started_at TEXT,
    completed_at TEXT,
    updated_at TEXT NOT NULL,
    current_phase TEXT NOT NULL DEFAULT 'queued',
    last_successful_phase TEXT NOT NULL DEFAULT '',
    failure_phase TEXT NOT NULL DEFAULT '',
    failure_code TEXT NOT NULL DEFAULT '',
    installation_completed INTEGER NOT NULL DEFAULT 0,
    registration_recoverable INTEGER NOT NULL DEFAULT 0,
    registration_snapshot_json TEXT NOT NULL DEFAULT ''
);

INSERT INTO provisioning_jobs_v018 (
    id, actor_user_id, actor_username, template_id, template_name,
    server_name, directory_name, installer_type, app_id, status,
    summary, error_summary, files_may_remain, server_id, created_at,
    started_at, completed_at, updated_at, current_phase,
    last_successful_phase, failure_phase, failure_code,
    installation_completed, registration_recoverable,
    registration_snapshot_json
)
SELECT
    id, actor_user_id, actor_username, template_id, template_name,
    server_name, directory_name, installer_type, app_id, status,
    summary, error_summary, files_may_remain, server_id, created_at,
    started_at, completed_at, updated_at, current_phase,
    last_successful_phase, failure_phase, failure_code,
    installation_completed, registration_recoverable,
    registration_snapshot_json
FROM provisioning_jobs;

CREATE TABLE provisioning_job_events_v018 (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id TEXT NOT NULL REFERENCES provisioning_jobs_v018(id) ON DELETE CASCADE,
    occurred_at TEXT NOT NULL,
    phase TEXT NOT NULL,
    code TEXT NOT NULL,
    summary TEXT NOT NULL
);

INSERT INTO provisioning_job_events_v018 (id, job_id, occurred_at, phase, code, summary)
SELECT id, job_id, occurred_at, phase, code, summary
FROM provisioning_job_events;

DROP TABLE provisioning_job_events;
DROP TABLE provisioning_jobs;
ALTER TABLE provisioning_jobs_v018 RENAME TO provisioning_jobs;
ALTER TABLE provisioning_job_events_v018 RENAME TO provisioning_job_events;

CREATE INDEX provisioning_jobs_actor_idx ON provisioning_jobs(actor_user_id,created_at DESC);
CREATE INDEX provisioning_jobs_status_idx ON provisioning_jobs(status);
CREATE INDEX provisioning_job_events_job_idx ON provisioning_job_events(job_id,id);
