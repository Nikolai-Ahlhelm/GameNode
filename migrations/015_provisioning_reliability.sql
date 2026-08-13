ALTER TABLE provisioning_jobs ADD COLUMN current_phase TEXT NOT NULL DEFAULT 'queued';
ALTER TABLE provisioning_jobs ADD COLUMN last_successful_phase TEXT NOT NULL DEFAULT '';
ALTER TABLE provisioning_jobs ADD COLUMN failure_phase TEXT NOT NULL DEFAULT '';
ALTER TABLE provisioning_jobs ADD COLUMN failure_code TEXT NOT NULL DEFAULT '';
ALTER TABLE provisioning_jobs ADD COLUMN installation_completed INTEGER NOT NULL DEFAULT 0;
ALTER TABLE provisioning_jobs ADD COLUMN registration_recoverable INTEGER NOT NULL DEFAULT 0;
