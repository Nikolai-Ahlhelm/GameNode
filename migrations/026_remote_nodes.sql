-- Remote Node Foundation (v0.5A). See docs/architecture.md and
-- docs/adr/0006-remote-node-foundation.md.
--
-- Every GameNode installation keeps this schema locally. It never shares a
-- database with any other GameNode instance:
--   - node_identity holds exactly one row: this installation's own durable
--     NodeID, generated once and independent of hostname/IP/database path.
--   - node_pairing_tokens are short-lived, single-use secrets an operator
--     generates on THIS node so a remote controller can enroll it. Only a
--     salted hash is stored, never the plaintext token.
--   - node_trusted_callers are machine credentials this node has issued to
--     controllers that successfully enrolled through a pairing token. Only a
--     salted hash of the credential is stored.
--   - remote_nodes is the registry of OTHER GameNode instances this
--     installation has enrolled as a controller. It stores configuration and
--     last-known status only - never a remote node's own database content or
--     authoritative server/runtime state (see AGENTS.md).

CREATE TABLE node_identity (
    id TEXT PRIMARY KEY CHECK (id = 'local'),
    node_id TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
);

CREATE TABLE node_pairing_tokens (
    id TEXT PRIMARY KEY,
    token_hash TEXT NOT NULL UNIQUE,
    created_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    used_at TEXT
);

CREATE TABLE node_trusted_callers (
    id TEXT PRIMARY KEY,
    credential_hash TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    last_seen_at TEXT
);

CREATE TABLE remote_nodes (
    id TEXT PRIMARY KEY,
    node_id TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    endpoint TEXT NOT NULL UNIQUE,
    credential TEXT NOT NULL,
    protocol_version INTEGER NOT NULL DEFAULT 0,
    gamenode_version TEXT NOT NULL DEFAULT '',
    os TEXT NOT NULL DEFAULT '',
    arch TEXT NOT NULL DEFAULT '',
    capabilities_json TEXT NOT NULL DEFAULT '[]',
    enabled INTEGER NOT NULL DEFAULT 1,
    trust_status TEXT NOT NULL DEFAULT 'enrolled',
    last_seen_at TEXT,
    last_health TEXT NOT NULL DEFAULT 'unknown',
    last_error_code TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX remote_nodes_enabled_idx ON remote_nodes(enabled);
