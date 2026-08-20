-- Optional tenant-scoped service status pages. Disabled and private by
-- default so upgrading never publishes server names or runtime state.
ALTER TABLE tenants ADD COLUMN status_page_enabled INTEGER NOT NULL DEFAULT 0 CHECK (status_page_enabled IN (0,1));
ALTER TABLE tenants ADD COLUMN status_page_public INTEGER NOT NULL DEFAULT 0 CHECK (status_page_public IN (0,1));
