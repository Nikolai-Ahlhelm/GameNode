ALTER TABLE tenants ADD COLUMN owner_user_id TEXT REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE tenants ADD COLUMN user_quota INTEGER NOT NULL DEFAULT 0 CHECK (user_quota >= 0 AND user_quota <= 100000);

CREATE TABLE tenant_invitations (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    email TEXT NOT NULL COLLATE NOCASE,
    token_hash BLOB NOT NULL,
    invited_by TEXT NOT NULL REFERENCES users(id),
    expires_at TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    consumed_at TEXT,
    created_at TEXT NOT NULL
);
CREATE INDEX tenant_invitations_tenant_idx ON tenant_invitations(tenant_id, created_at DESC);
CREATE INDEX tenant_invitations_email_idx ON tenant_invitations(email, created_at DESC);
