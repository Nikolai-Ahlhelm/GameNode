# Architecture

GameNode is a single Go process. The transport layer is `internal/api`; authentication and setup logic live in `internal/auth`; local-user and group administration lives in `internal/identity`; server application logic and persistence live in `internal/servers`; OS-specific native process implementations are isolated in `internal/runtime`; server-root filesystem policy and read operations live in `internal/filesystem`; SQLite access and migrations live in `internal/database`. The React UI is a separate development application and is built into `cmd/gamenode/webassets` for releases.

The identity migration extends the existing `users` table and adds groups plus a cascading membership join table. Groups are stable membership containers; role assignments attached to them participate in RBAC evaluation.

The RBAC core provides a static permission catalog, allow-only roles, user/group role assignments, and global or server scopes in `internal/rbac`. Disabled users are denied before the administrator bypass; enabled administrators bypass the evaluator. `internal/api` centrally applies the evaluator to Server, Console, Files, and existing Users/Groups/Roles management endpoints. Platform permissions use only global scope. The backend remains authoritative and the UI only uses capabilities for affordances.

Milestone 4 adds server-root-scoped directory listing, bounded text reads, safe create/edit/move/delete operations, streaming upload/download transport through `internal/filesystem`, and a Files tab with a bounded Monaco text editor. Filesystem sandboxing stays independent from RBAC: RBAC authorizes an action while `internal/filesystem` validates every path. Archive browsing, monitoring, ports, cluster system, Docker, and SteamCMD remain out of scope.
