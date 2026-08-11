-- SQLite treats NULL values as distinct in composite UNIQUE constraints.
-- The original assignment tables use NULL scope_id for global assignments,
-- so these partial indexes enforce the intended one-assignment semantics for
-- both existing and newly migrated databases.
DELETE FROM user_role_assignments
    WHERE scope_type = 'global'
      AND id NOT IN (
          SELECT MIN(id)
          FROM user_role_assignments
          WHERE scope_type = 'global'
          GROUP BY user_id, role_id
      );

DELETE FROM group_role_assignments
    WHERE scope_type = 'global'
      AND id NOT IN (
          SELECT MIN(id)
          FROM group_role_assignments
          WHERE scope_type = 'global'
          GROUP BY group_id, role_id
      );

CREATE UNIQUE INDEX user_role_assignments_global_unique_idx
    ON user_role_assignments(user_id, role_id)
    WHERE scope_type = 'global';

CREATE UNIQUE INDEX group_role_assignments_global_unique_idx
    ON group_role_assignments(group_id, role_id)
    WHERE scope_type = 'global';
