CREATE TABLE server_config_values (
    server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    adapter_id TEXT NOT NULL,
    field_key TEXT NOT NULL,
    value TEXT NOT NULL,
    sensitive INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY(server_id, adapter_id, field_key)
);
CREATE INDEX server_config_values_adapter_idx ON server_config_values(server_id, adapter_id);
