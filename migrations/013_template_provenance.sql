ALTER TABLE server_template_variables ADD COLUMN template_source TEXT NOT NULL DEFAULT '';
ALTER TABLE server_template_variables ADD COLUMN template_version TEXT NOT NULL DEFAULT '';
