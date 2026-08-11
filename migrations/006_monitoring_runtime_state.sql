ALTER TABLE server_runtime_state ADD COLUMN last_exit_at TEXT;
ALTER TABLE server_runtime_state ADD COLUMN crash_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE server_runtime_state ADD COLUMN restart_count INTEGER NOT NULL DEFAULT 0;
