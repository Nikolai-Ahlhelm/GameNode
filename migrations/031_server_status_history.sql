CREATE TABLE server_status_history (
    server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    checked_at TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('up', 'degraded', 'down')),
    state TEXT NOT NULL,
    PRIMARY KEY (server_id, checked_at)
);

CREATE INDEX server_status_history_checked_at_idx
    ON server_status_history(checked_at);
