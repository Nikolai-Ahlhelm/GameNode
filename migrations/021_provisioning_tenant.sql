-- Tenant Foundation, Step 2: provisioning jobs carry the tenant they were
-- started for, matching the target server they will eventually create.
--
-- No REFERENCES clause: provisioning_jobs.template_id already has no foreign
-- key either (a template may be Official/catalog data rather than a
-- persisted row), so this column follows that same established,
-- application-validated-reference convention for this table rather than
-- forcing a table rebuild (see internal/provisioning.Store, which validates
-- the tenant exists before a job is created).
ALTER TABLE provisioning_jobs ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'default';
CREATE INDEX provisioning_jobs_tenant_idx ON provisioning_jobs(tenant_id);
