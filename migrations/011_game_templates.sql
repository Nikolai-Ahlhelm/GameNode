CREATE TABLE game_templates (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    source_type TEXT NOT NULL,
    source_identifier TEXT NOT NULL DEFAULT '',
    source_format_version TEXT NOT NULL DEFAULT '',
    source_metadata_json TEXT NOT NULL,
    installer_json TEXT NOT NULL,
    launch_json TEXT,
    compatibility_status TEXT NOT NULL CHECK(compatibility_status IN ('compatible','partially_compatible','unsupported')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX game_templates_name_idx ON game_templates(name COLLATE NOCASE);
CREATE INDEX game_templates_source_idx ON game_templates(source_type, source_identifier);

CREATE TABLE game_template_variables (
    template_id TEXT NOT NULL REFERENCES game_templates(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    variable_key TEXT NOT NULL,
    default_value TEXT NOT NULL DEFAULT '',
    user_viewable INTEGER NOT NULL,
    user_editable INTEGER NOT NULL,
    variable_type TEXT NOT NULL,
    sensitive INTEGER NOT NULL,
    required INTEGER NOT NULL,
    nullable INTEGER NOT NULL,
    validation_json TEXT NOT NULL,
    raw_rules_json TEXT NOT NULL,
    PRIMARY KEY(template_id, position),
    UNIQUE(template_id, variable_key)
);

CREATE TABLE game_template_findings (
    template_id TEXT NOT NULL REFERENCES game_templates(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    severity TEXT NOT NULL CHECK(severity IN ('info','warning','error')),
    component TEXT NOT NULL,
    code TEXT NOT NULL,
    summary TEXT NOT NULL,
    PRIMARY KEY(template_id, position)
);
