ALTER TABLE servers ADD COLUMN auto_restart_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE servers ADD COLUMN auto_restart_max_attempts INTEGER NOT NULL DEFAULT 3;
ALTER TABLE servers ADD COLUMN auto_restart_window_seconds INTEGER NOT NULL DEFAULT 300;
ALTER TABLE servers ADD COLUMN auto_restart_delay_seconds INTEGER NOT NULL DEFAULT 5;
