ALTER TABLE email_alert_settings ADD COLUMN provider TEXT NOT NULL DEFAULT 'smtp';
ALTER TABLE email_alert_settings ADD COLUMN graph_tenant_id TEXT NOT NULL DEFAULT '';
ALTER TABLE email_alert_settings ADD COLUMN graph_client_id TEXT NOT NULL DEFAULT '';
ALTER TABLE email_alert_settings ADD COLUMN graph_client_secret TEXT NOT NULL DEFAULT '';
ALTER TABLE email_alert_settings ADD COLUMN graph_sender TEXT NOT NULL DEFAULT '';
