CREATE TABLE roles (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL COLLATE NOCASE UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE role_permissions (
    role_id TEXT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    permission_key TEXT NOT NULL,
    PRIMARY KEY (role_id, permission_key)
);
CREATE TABLE user_role_assignments (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role_id TEXT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    scope_type TEXT NOT NULL CHECK(scope_type IN ('global','server')),
    scope_id TEXT REFERENCES servers(id) ON DELETE CASCADE,
    CHECK((scope_type='global' AND scope_id IS NULL) OR (scope_type='server' AND scope_id IS NOT NULL)),
    UNIQUE(user_id, role_id, scope_type, scope_id)
);
CREATE TABLE group_role_assignments (
    id TEXT PRIMARY KEY,
    group_id TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    role_id TEXT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    scope_type TEXT NOT NULL CHECK(scope_type IN ('global','server')),
    scope_id TEXT REFERENCES servers(id) ON DELETE CASCADE,
    CHECK((scope_type='global' AND scope_id IS NULL) OR (scope_type='server' AND scope_id IS NOT NULL)),
    UNIQUE(group_id, role_id, scope_type, scope_id)
);
CREATE INDEX user_role_assignments_user_idx ON user_role_assignments(user_id);
CREATE INDEX group_role_assignments_group_idx ON group_role_assignments(group_id);
