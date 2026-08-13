CREATE TABLE provisioning_job_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id TEXT NOT NULL REFERENCES provisioning_jobs(id) ON DELETE CASCADE,
    occurred_at TEXT NOT NULL,
    phase TEXT NOT NULL,
    code TEXT NOT NULL,
    summary TEXT NOT NULL
);
CREATE INDEX provisioning_job_events_job_idx ON provisioning_job_events(job_id,id);
