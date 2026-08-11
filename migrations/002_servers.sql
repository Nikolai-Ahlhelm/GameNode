CREATE TABLE IF NOT EXISTS servers (
    id TEXT PRIMARY KEY,
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
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS server_runtime_state (
    server_id TEXT PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    pid INTEGER,
    process_start_key TEXT,
    process_started_at TEXT,
    last_start_at TEXT,
    last_stop_at TEXT,
    exit_code INTEGER,
    last_crash_at TEXT,
    last_error TEXT NOT NULL DEFAULT '',
    current_state TEXT NOT NULL DEFAULT 'stopped',
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS servers_name_idx ON servers(name);
