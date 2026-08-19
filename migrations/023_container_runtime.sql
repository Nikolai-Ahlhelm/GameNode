-- v0.3 container runtime. Runtime-specific data intentionally stays out of
-- the shared servers row so native definitions retain their established form.
CREATE TABLE server_container_configs (
    server_id TEXT PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    image TEXT NOT NULL,
    image_digest TEXT NOT NULL DEFAULT '',
    command_json TEXT NOT NULL DEFAULT '[]',
    memory_limit_bytes INTEGER NOT NULL,
    cpu_limit_millis INTEGER NOT NULL,
    ownership_token TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

ALTER TABLE server_ports ADD COLUMN container_port INTEGER;
