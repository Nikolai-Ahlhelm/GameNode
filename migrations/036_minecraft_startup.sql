CREATE TABLE minecraft_startup_configurations (
    server_id TEXT PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    configuration_json TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
