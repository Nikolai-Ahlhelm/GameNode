CREATE TABLE registration_email_verification_settings (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    enabled INTEGER NOT NULL DEFAULT 0,
    lifetime_minutes INTEGER NOT NULL DEFAULT 30,
    resend_cooldown_seconds INTEGER NOT NULL DEFAULT 60,
    max_attempts INTEGER NOT NULL DEFAULT 5,
    updated_at TEXT NOT NULL
);

INSERT INTO registration_email_verification_settings(singleton, updated_at)
VALUES(1, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));

CREATE TABLE registration_email_verifications (
    id TEXT PRIMARY KEY,
    email TEXT NOT NULL COLLATE NOCASE,
    token_hash BLOB NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    expires_at TEXT NOT NULL,
    verified_at TEXT,
    consumed_at TEXT,
    created_at TEXT NOT NULL
);

CREATE INDEX registration_email_verifications_email_created_idx
ON registration_email_verifications(email, created_at DESC);

CREATE INDEX registration_email_verifications_expiry_idx
ON registration_email_verifications(expires_at);
