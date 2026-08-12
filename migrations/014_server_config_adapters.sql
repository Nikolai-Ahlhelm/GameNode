CREATE TABLE server_config_adapters (
    server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    adapter_id TEXT NOT NULL,
    adapter_schema_version INTEGER NOT NULL,
    adapter_version TEXT NOT NULL,
    template_id TEXT NOT NULL,
    template_version TEXT NOT NULL,
    definition_json TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY(server_id, adapter_id)
);
CREATE INDEX server_config_adapters_template_idx ON server_config_adapters(template_id, template_version);
