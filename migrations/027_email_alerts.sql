CREATE TABLE email_alert_settings (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    enabled INTEGER NOT NULL DEFAULT 0,
    smtp_host TEXT NOT NULL DEFAULT '',
    smtp_port INTEGER NOT NULL DEFAULT 587,
    smtp_security TEXT NOT NULL DEFAULT 'starttls',
    smtp_username TEXT NOT NULL DEFAULT '',
    smtp_password TEXT NOT NULL DEFAULT '',
    from_address TEXT NOT NULL DEFAULT '',
    recipients_json TEXT NOT NULL DEFAULT '[]',
    subject_prefix TEXT NOT NULL DEFAULT '[GameNode]',
    notify_started INTEGER NOT NULL DEFAULT 0,
    notify_stopped INTEGER NOT NULL DEFAULT 0,
    notify_crashed INTEGER NOT NULL DEFAULT 1,
    notify_restarted INTEGER NOT NULL DEFAULT 1,
    notify_auto_restart_failed INTEGER NOT NULL DEFAULT 1,
    notify_auto_restart_limit INTEGER NOT NULL DEFAULT 1,
    updated_at TEXT NOT NULL
);

INSERT INTO email_alert_settings(singleton, updated_at)
VALUES(1, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
