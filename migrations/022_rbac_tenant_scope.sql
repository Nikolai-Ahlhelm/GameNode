-- Tenant Foundation, Step 3: RBAC assignments gain a "tenant" scope
-- alongside the existing "global" and "server" scopes. Roles stay
-- scope-neutral; only assignments carry a scope (see docs/architecture.md).
--
-- scope_id's existing foreign key targets servers(id), so it cannot also
-- hold a tenant ID: a single column cannot carry two different foreign key
-- targets for the same value. tenant_scope_id is therefore a second,
-- mutually exclusive nullable reference into tenants(id), with its own
-- ON DELETE CASCADE. server-scoped rows are completely unaffected: they keep
-- using the original scope_id column and its original foreign key exactly
-- as before. Widening the scope_type CHECK constraint requires a full
-- rebuild (CHECK constraints cannot be altered in place), so this follows
-- the same table-rebuild pattern used by 018/020/021; these tables are not
-- referenced by any other table's foreign key, so the rebuild carries none
-- of 020's cascading-DROP risk.
CREATE TABLE user_role_assignments_v022 (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role_id TEXT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    scope_type TEXT NOT NULL CHECK(scope_type IN ('global','tenant','server')),
    scope_id TEXT REFERENCES servers(id) ON DELETE CASCADE,
    tenant_scope_id TEXT REFERENCES tenants(id) ON DELETE CASCADE,
    CHECK(
        (scope_type='global' AND scope_id IS NULL AND tenant_scope_id IS NULL) OR
        (scope_type='server' AND scope_id IS NOT NULL AND tenant_scope_id IS NULL) OR
        (scope_type='tenant' AND scope_id IS NULL AND tenant_scope_id IS NOT NULL)
    )
);
INSERT INTO user_role_assignments_v022 (id, user_id, role_id, scope_type, scope_id, tenant_scope_id)
SELECT id, user_id, role_id, scope_type, scope_id, NULL FROM user_role_assignments;
DROP TABLE user_role_assignments;
ALTER TABLE user_role_assignments_v022 RENAME TO user_role_assignments;

CREATE TABLE group_role_assignments_v022 (
    id TEXT PRIMARY KEY,
    group_id TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    role_id TEXT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    scope_type TEXT NOT NULL CHECK(scope_type IN ('global','tenant','server')),
    scope_id TEXT REFERENCES servers(id) ON DELETE CASCADE,
    tenant_scope_id TEXT REFERENCES tenants(id) ON DELETE CASCADE,
    CHECK(
        (scope_type='global' AND scope_id IS NULL AND tenant_scope_id IS NULL) OR
        (scope_type='server' AND scope_id IS NOT NULL AND tenant_scope_id IS NULL) OR
        (scope_type='tenant' AND scope_id IS NULL AND tenant_scope_id IS NOT NULL)
    )
);
INSERT INTO group_role_assignments_v022 (id, group_id, role_id, scope_type, scope_id, tenant_scope_id)
SELECT id, group_id, role_id, scope_type, scope_id, NULL FROM group_role_assignments;
DROP TABLE group_role_assignments;
ALTER TABLE group_role_assignments_v022 RENAME TO group_role_assignments;

CREATE INDEX user_role_assignments_user_idx ON user_role_assignments(user_id);
CREATE INDEX group_role_assignments_group_idx ON group_role_assignments(group_id);

-- SQLite treats NULL as distinct in a UNIQUE constraint/index: a row is only
-- considered a duplicate of another when every indexed column compares
-- equal, and NULL never compares equal to NULL. Every scope type here has at
-- least one of (scope_id, tenant_scope_id) NULL by construction (global has
-- both, server has tenant_scope_id, tenant has scope_id), so one plain
-- composite unique constraint across all three scope types would enforce
-- nothing for any of them - not just global, which is why 005 originally
-- needed its own partial index. Three scope-specific partial unique indexes
-- replace it, one per scope type, each keyed only on the columns that are
-- actually non-null (and therefore meaningful for uniqueness) for that scope.
CREATE UNIQUE INDEX user_role_assignments_global_unique_idx
    ON user_role_assignments(user_id, role_id)
    WHERE scope_type = 'global';
CREATE UNIQUE INDEX user_role_assignments_server_unique_idx
    ON user_role_assignments(user_id, role_id, scope_id)
    WHERE scope_type = 'server';
CREATE UNIQUE INDEX user_role_assignments_tenant_unique_idx
    ON user_role_assignments(user_id, role_id, tenant_scope_id)
    WHERE scope_type = 'tenant';

CREATE UNIQUE INDEX group_role_assignments_global_unique_idx
    ON group_role_assignments(group_id, role_id)
    WHERE scope_type = 'global';
CREATE UNIQUE INDEX group_role_assignments_server_unique_idx
    ON group_role_assignments(group_id, role_id, scope_id)
    WHERE scope_type = 'server';
CREATE UNIQUE INDEX group_role_assignments_tenant_unique_idx
    ON group_role_assignments(group_id, role_id, tenant_scope_id)
    WHERE scope_type = 'tenant';

CREATE INDEX user_role_assignments_tenant_idx ON user_role_assignments(tenant_scope_id) WHERE scope_type = 'tenant';
CREATE INDEX group_role_assignments_tenant_idx ON group_role_assignments(tenant_scope_id) WHERE scope_type = 'tenant';
