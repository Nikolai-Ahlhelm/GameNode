CREATE TABLE minecraft_message_settings (
    server_id TEXT PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    settings_json TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE minecraft_message_deliveries (
    server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    delivery_key TEXT NOT NULL,
    delivered_at TEXT NOT NULL,
    PRIMARY KEY(server_id, delivery_key)
);
