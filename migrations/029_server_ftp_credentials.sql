-- Each local server owns one FTP identity. Passwords are populated only when
-- an authorized operator explicitly enables/rotates access, and are stored as
-- Argon2id hashes by the application.
CREATE TABLE server_ftp_credentials (
    server_id TEXT PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    username TEXT NOT NULL COLLATE NOCASE UNIQUE,
    password_hash TEXT NOT NULL DEFAULT '',
    enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

INSERT INTO server_ftp_credentials(server_id, username, created_at, updated_at)
SELECT id, 'gn-' || replace(id, '-', ''), created_at, updated_at
FROM servers;

-- Provisioning and direct creation both eventually insert an ordinary server
-- row, so one trigger covers all future creation paths without coupling those
-- domains to FTP credential management.
CREATE TRIGGER server_ftp_credentials_after_server_insert
AFTER INSERT ON servers
BEGIN
    INSERT INTO server_ftp_credentials(server_id, username, created_at, updated_at)
    VALUES (
        NEW.id,
        'gn-' || replace(NEW.id, '-', ''),
        NEW.created_at,
        NEW.updated_at
    );
END;
