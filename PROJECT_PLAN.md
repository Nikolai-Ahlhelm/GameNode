# GameNode – Project Plan

## Vision

**GameNode is a self-contained game server management platform for Windows and Linux.**

Every installation is independently usable through its own local web interface. It manages native applications without requiring containers or templates and treats existing installations as first-class resources.

Security, granular authorization, reliable process management and cross-platform operation take priority over marketplace-style automation.

Future releases may federate multiple GameNodes under a central controller, but no node should depend on a controller for local operation.

---

# 1. Project Goal

Develop a standalone, cross-platform game server management application for **Windows and Linux**.

The first usable version focuses entirely on **a single autonomous node**.

Cluster, Controller, Master/Worker and Multi-Node capabilities are deliberately excluded from v0.1. The architecture should not make future clustering unnecessarily difficult, but no cluster-specific abstraction should be built without an immediate need.

The application should feel conceptually similar to a local hypervisor/server management interface:

- every host has its own full management UI;
- every host can operate independently;
- no external controller is required;
- existing game server installations can be adopted;
- arbitrary native applications can be managed without templates.

---

# 2. Technology Stack

## Backend / Node Runtime

Use:

- **Go**
- REST API
- WebSockets for live console and later live metrics
- Prefer the Go standard library and small, established dependencies
- Avoid heavyweight frameworks unless clearly justified

## Frontend

Use:

- **React**
- **TypeScript**
- **Vite**
- component-based UI
- preferably a Radix UI / shadcn-style component approach
- **Monaco Editor** for editing text/configuration files

## Database

Use:

- **SQLite**
- SQL migrations
- preferably **sqlc** for typed queries
- avoid a heavyweight ORM unless there is a clear technical reason

## Deployment

The frontend should be built during the release process and embedded into the Go backend using `go:embed`.

Runtime requirements must not include Node.js.

Target release artifacts:

```text
gamenode-windows-amd64.exe
gamenode-linux-amd64
```

Windows:

- installable/runnable as a Windows Service

Linux:

- installable/runnable as a systemd service

---

# 3. High-Level Architecture

GameNode v0.1 should initially run as **one process**.

```text
GameNode
├── Web UI
├── REST API
├── WebSocket Server
├── Authentication / RBAC
├── Server Management
├── Process Runtime
├── Console Manager
├── File Manager
├── Monitoring
├── Health Checks
├── Port Management
├── Audit Logging
└── SQLite
```

Do **not** introduce:

- Microservices
- Redis
- RabbitMQ
- Kafka
- Kubernetes
- Separate agent service
- PostgreSQL requirement
- External message queues

The GameNode process itself is both:

- the local management backend;
- the local runtime agent.

---

# 4. Architecture Principles

Keep the following layers logically separated:

```text
Transport Layer
REST / WebSocket
       ↓
Application Services
       ↓
Domain
       ↓
Infrastructure
OS / SQLite / Filesystem / Processes
```

Business logic must not depend directly on HTTP, WebSocket or React.

Operating-system-specific code must be hidden behind interfaces.

Avoid spreading constructs such as:

```go
if runtime.GOOS == "windows" {
    ...
}
```

through business logic.

Prefer interfaces such as:

```go
type Runtime interface {
    Start(ctx context.Context, server Server) error
    Stop(ctx context.Context, server Server) error
    Kill(ctx context.Context, server Server) error
    Restart(ctx context.Context, server Server) error
    Status(ctx context.Context, server Server) (ProcessStatus, error)
    Stats(ctx context.Context, server Server) (ProcessStats, error)
}
```

Additional useful abstractions may include:

```text
ProcessManager
FileSystem
PortInspector
MetricsProvider
ServiceManager
HealthChecker
```

Initial runtime implementations:

```text
WindowsNativeRuntime
LinuxNativeRuntime
```

Future runtime implementations may include:

```text
DockerRuntime
PodmanRuntime
```

but Docker and Podman are **not part of v0.1**.

Avoid speculative abstractions that are not required by the current implementation.

---

# 5. Suggested Repository Structure

Use approximately the following repository layout unless implementation experience reveals a clearly better structure:

```text
cmd/
└── gamenode/
    └── main.go

internal/
├── api/
├── auth/
├── users/
├── groups/
├── roles/
├── rbac/
├── servers/
├── runtime/
│   ├── runtime.go
│   ├── windows/
│   └── linux/
├── console/
├── filesystem/
├── monitoring/
├── health/
├── ports/
├── audit/
├── database/
└── platform/

migrations/

web/
├── src/
├── package.json
└── vite.config.ts

docs/
├── architecture.md
├── security.md
├── development.md
├── api.md
└── adr/
```

---

# 6. Functional Requirements – v0.1

## 6.1 Authentication

Implement local user accounts.

Requirements:

- Login
- Logout
- secure session cookies
- CSRF protection where appropriate for the chosen session model
- password hashing with **Argon2id**
- no default administrator password
- initial setup flow on first startup

Example first-run setup:

```text
GameNode Initial Setup

Create administrator

Username:
Email:
Password:
```

---

## 6.2 User Management

Administrators must be able to manage users through the web interface.

Required operations:

- create user
- edit user
- disable user
- delete user
- reset password
- assign groups

Each user must have an immutable unique ID.

Do not base authorization or ownership logic on usernames.

---

## 6.3 Groups

Implement groups.

A group contains:

```text
Group
├── Name
├── Description
└── Members
```

A user may belong to multiple groups.

Groups may later be assigned roles on servers.

---

## 6.4 RBAC

Implement a real Role-Based Access Control system.

Core entities:

```text
User
Group
Role
Permission
RolePermission
UserGroup
ResourceAssignment
```

Permissions must support at least two scopes:

```text
Global
Server
```

### Example global permissions

```text
Platform.View
Platform.Settings

Users.View
Users.Create
Users.Edit
Users.Delete

Groups.View
Groups.Create
Groups.Edit
Groups.Delete

Roles.View
Roles.Create
Roles.Edit
Roles.Delete

Server.Create
```

### Example server-scoped permissions

```text
Server.View
Server.Edit
Server.Start
Server.Stop
Server.Restart
Server.Kill
Server.Delete
Server.Reset

Console.View
Console.Send

Files.View
Files.Upload
Files.Download
Files.Edit
Files.Delete
Files.Rename

Monitoring.View
```

Authorization must always be enforced **server-side**.

Hiding controls in the React frontend does not count as authorization.

---

## 6.5 Game Server Data Model

A server should contain at least:

```text
ID
Name
Description
WorkingDirectory
Executable
Arguments
EnvironmentVariables
RuntimeType
AutoStart
RestartPolicy
StopMethod
StopCommand
StopTimeout
CreatedAt
UpdatedAt
```

Associated resources:

```text
Ports
HealthChecks
AccessAssignments
RuntimeState
```

Prefer storing process arguments as a structured list instead of one shell command string.

---

## 6.6 Add Server Workflows

Support three UI flows:

```text
Add Server

○ Create New
○ Adopt Existing
○ Custom Application
```

For v0.1, `Create New` and `Custom Application` may share substantial implementation.

### Adopt Existing

This is a first-class feature.

A pre-existing server installation must be registerable without reinstalling or modifying it.

Example:

```text
Name:
Project Zomboid

Working Directory:
D:\GameServers\PZ01

Executable:
StartServer64.bat

Arguments:
-port 27000 -udpport 27001
```

Adding an existing server must not unexpectedly overwrite, move or reinstall its files.

---

## 6.7 Custom Applications

Custom applications must not require a template.

Minimum fields:

```text
Name
Working Directory
Executable
Arguments
```

Optional fields:

```text
Environment Variables
Stop Method
Stop Command
Stop Timeout
Health Check
Ports
```

The architecture should support:

```text
Windows EXE
Batch file
PowerShell script
Linux binary
Shell script
Java JAR
```

Do not require Docker.

---

## 6.8 Server Lifecycle

Support:

```text
Start
Stop
Restart
Kill
Delete
Reset
```

`Stop` and `Kill` are separate concepts.

Graceful stop methods may include:

```text
process signal
stdin command
terminate process
```

Kill should immediately terminate the managed process.

### Reset

For v0.1, avoid pretending every custom server can be automatically reinstalled.

`Reset` should be implemented conservatively and clearly documented.

A full reinstall workflow should only be added when installers/templates exist.

---

## 6.9 Process Tracking

A process must be uniquely associated with a server.

Track at least:

```text
PID
Start Timestamp
Exit Timestamp
Exit Code
Last Crash
Last Error
```

The runtime design must account for this scenario:

```text
Game server is running
        ↓
GameNode restarts or is upgraded
        ↓
Game server should ideally continue running
        ↓
GameNode should be able to rediscover its process
```

Do not force an unreliable reattachment hack into the first implementation.

However, do not design the runtime in a way that makes process survival/re-discovery impossible later.

---

# 7. Console

Implement a live console.

Requirements:

```text
stdout capture
stderr capture
stdin input
WebSocket streaming
```

Multiple users may watch the same console simultaneously.

Maintain a per-server in-memory ring buffer, e.g. the latest 1000 lines.

When a client opens the console:

1. send recent buffered output;
2. continue with live output.

Permissions:

```text
Console.View
Console.Send
```

A user with `Console.View` only must not be able to send commands.

---

# 8. File Browser

Every server has a defined filesystem root.

The web file browser must never expose paths outside that root.

Required features:

```text
List directories
Open file
Edit file
Create file
Create directory
Upload
Download
Rename
Move
Delete
```

Use Monaco Editor for text editing.

Typical editable formats:

```text
txt
json
yaml
yml
xml
ini
cfg
properties
```

## Critical Security Requirement

Prevent all forms of path escape.

Test explicitly for:

```text
../../
absolute paths
Windows separator tricks
Linux separator tricks
URL encoded traversal
symlink traversal
junction/reparse-point escape where relevant
```

The backend must canonicalize/resolve requested paths and verify that the resolved target remains inside the authorized server root.

Never rely only on frontend validation.

---

# 9. Port Management

Do not build a complex Pterodactyl-style allocation system in v0.1.

Servers may define ports:

```text
Name
Protocol
Port
Description
```

Example:

```text
Game    TCP    25565
Query   UDP    25566
RCON    TCP    25575
```

Supported protocols:

```text
TCP
UDP
```

Maintain an internal GameNode port registry.

Check:

1. whether another registered GameNode server claims the same protocol/port;
2. whether the operating system reports that port as already in use by an external process.

UI examples:

```text
25565/TCP
Available
```

or:

```text
25565/TCP
In use
```

When safely discoverable, optionally show:

```text
PID
Process Name
```

v0.1 must **not** automatically modify:

```text
Windows Firewall
iptables
nftables
Router NAT
UPnP
Cloud firewall APIs
```

---

# 10. Monitoring

Collect at least per-server:

```text
Process State
CPU Usage
RAM Usage
Uptime
PID
```

Collect host information:

```text
Host CPU
Host RAM
Host Disk
Operating System
GameNode Version
```

Suggested server state model:

```text
Stopped
Starting
Running
Stopping
Crashed
Unhealthy
```

Keep runtime state and health state conceptually separate.

A running process is not automatically healthy.

---

# 11. Health Monitoring

Implement an extensible health-check interface.

v0.1 health check types:

```text
Process
TCP
HTTP
```

Future possibilities:

```text
UDP protocol-specific probes
RCON
Custom command
Game-specific health checks
```

Example configuration:

```text
Health Check Type:
TCP

Port:
25565

Interval:
30 seconds

Timeout:
5 seconds

Failure Threshold:
3
```

---

# 12. Auto Restart

Restart policies:

```text
Never
On Crash
Always
```

Configuration:

```text
Restart Delay
Maximum Restart Attempts
Restart Window
```

Prevent endless restart loops.

Example undesired behavior:

```text
crash
start
crash
start
crash
start
...
```

Implement backoff/rate limiting or restart-window protection.

---

# 13. Dashboards

## Admin Dashboard

Display information such as:

```text
Servers:       8
Running:       5
Stopped:       2
Unhealthy:     1

Host CPU
Host RAM
Host Disk

Recent crashes
Recent actions
```

## User Dashboard

Users see only servers for which they have at least:

```text
Server.View
```

Example:

```text
My Servers

Minecraft
Running
CPU 14 %
RAM 3.7 GB

Project Zomboid
Stopped
```

---

# 14. Audit Log

Audit logging is part of v0.1.

Record at least:

```text
Login
Failed login
Server create
Server edit
Server start
Server stop
Server restart
Server kill
Server delete
File edit
File upload
File download
User changes
Group changes
Role changes
Permission changes
```

Audit event fields:

```text
Timestamp
User ID
Action
Resource Type
Resource ID
Result
Source IP
Metadata
```

Do not store passwords, session tokens, authorization tokens or other secrets in audit metadata.

---

# 15. Security Requirements

Security is part of the architecture, not a later add-on.

Threats to explicitly consider:

```text
Path traversal
Symlink traversal
Windows reparse-point/junction traversal
Command injection
Argument escaping
Shell injection
Unauthorized WebSocket access
RBAC bypass
CSRF
Session fixation
Unsafe file uploads
Privilege escalation
Sensitive data in logs
```

## Process Launching

Do not automatically execute arbitrary server commands through a shell where avoidable.

Prefer:

```text
Executable
Arguments[]
```

and direct OS process execution.

Avoid defaulting to:

```text
cmd.exe /c "<arbitrary user string>"
```

or:

```text
sh -c "<arbitrary user string>"
```

If shell execution is later supported, make it an explicit execution mode with appropriate warnings and permissions.

---

# 16. API Design

Use a versioned API namespace:

```text
/api/v1/...
```

Example endpoints:

```text
POST   /api/v1/auth/login
POST   /api/v1/auth/logout

GET    /api/v1/servers
POST   /api/v1/servers
GET    /api/v1/servers/{id}
PATCH  /api/v1/servers/{id}
DELETE /api/v1/servers/{id}

POST /api/v1/servers/{id}/start
POST /api/v1/servers/{id}/stop
POST /api/v1/servers/{id}/restart
POST /api/v1/servers/{id}/kill

GET /api/v1/servers/{id}/files
GET /api/v1/servers/{id}/files/content
PUT /api/v1/servers/{id}/files/content

GET /api/v1/users
GET /api/v1/groups
GET /api/v1/roles
GET /api/v1/audit
```

Console WebSocket example:

```text
/api/v1/servers/{id}/console/ws
```

Use consistent API conventions and a unified error response format.

Do not leak internal errors or filesystem details unnecessarily.

---

# 17. Database

Use migrations from the beginning.

Potential tables:

```text
users
sessions

groups
user_groups

roles
permissions
role_permissions

resource_assignments

servers
server_environment_variables
server_ports
server_health_checks
server_runtime_state

audit_events
```

Prefer UUIDs for entity IDs unless a strong reason exists otherwise.

Store timestamps in UTC.

Keep migrations deterministic and committed to source control.

Avoid invisible/manual schema mutations.

---

# 18. Frontend Structure

Primary navigation:

```text
Dashboard
Servers
Users
Groups
Roles
Audit Log
Settings
```

Server page tabs:

```text
Overview
Console
Files
Configuration
Networking
Monitoring
Access
```

The frontend should hide actions the current user cannot perform.

Example:

A user without:

```text
Server.Start
```

should not see a Start button.

However, backend authorization remains mandatory.

Desktop administration is the primary target.

Responsive behavior is desirable.

---

# 19. Testing Requirements

Tests are mandatory.

## Unit Tests

At minimum:

```text
RBAC
Path validation
Port collision logic
Restart policy
Health-state transitions
```

## Integration Tests

At minimum:

```text
Login
Server CRUD
Permission enforcement
File operations
Runtime lifecycle
```

## Security Tests

At minimum:

```text
../../ path traversal
encoded traversal
symlink escape
junction/reparse-point escape where testable
unauthorized WebSocket access
server access bypass
```

Platform-specific runtime tests should be isolated appropriately.

Do not require every OS-specific test to run on every development OS.

---

# 20. Logging

Use structured logging.

Required levels:

```text
debug
info
warn
error
```

Never log:

- plaintext passwords;
- session tokens;
- API tokens;
- sensitive environment values unless explicitly marked safe.

Logs should be useful both during development and when GameNode runs as a service.

---

# 21. Configuration

GameNode itself should have a small local configuration.

Example:

```yaml
server:
  listen: "0.0.0.0:8443"

data:
  directory: "./data"

database:
  path: "./data/gamenode.db"

logging:
  level: "info"
```

Game server definitions belong in the database, not YAML configuration files.

---

# 22. Milestones

Do not implement the entire project in one pass.

---

## Milestone 1 — Foundation

Implement:

```text
Go project
React/Vite project
Embedded frontend
SQLite
Migrations
Configuration
Structured logging
Initial setup
Authentication
Initial admin account
Basic dashboard shell
Windows build
Linux build
```

### Definition of Done

On Windows and Linux:

- GameNode can start;
- the local web interface loads;
- first-run admin setup works;
- login/logout works;
- SQLite is initialized through migrations;
- production frontend is served by the Go binary;
- tests pass;
- release builds can be produced.

---

## Milestone 2 — Server Runtime

Implement:

```text
Server CRUD
Custom server
Adopt existing
Windows native runtime
Linux native runtime
Start
Stop
Kill
Restart
Status
```

### Definition of Done

A user can register an arbitrary native application and reliably start/stop it.

At least test:

```text
Windows: simple executable/script
Linux: simple executable/script
```

---

## Milestone 3 — Console

Implement:

```text
stdout capture
stderr capture
stdin
Ring buffer
WebSocket
Console UI
```

### Definition of Done

Two browser sessions can simultaneously view the same live console.

A user with permission can send input.

A user without `Console.Send` cannot send input.

---

## Milestone 4 — Files

Implement:

```text
sandboxed filesystem
file browser
editor
uploads
downloads
rename
move
delete
Monaco Editor
```

### Definition of Done

Server files can be managed completely from the web UI while no operation can escape the configured server root.

---

## Milestone 5 — RBAC

Implement:

```text
Users
Groups
Roles
Permissions
Global scopes
Server scopes
Access assignments
```

### Definition of Done

This scenario must work entirely through backend authorization:

```text
User A
→ Minecraft
→ Start/Stop + Console

User B
→ Minecraft
→ Files read-only

User C
→ no Minecraft access
```

---

## Milestone 6 — Reliability

Implement:

```text
Monitoring
Health checks
Auto restart
Port management
Audit log
Admin dashboard
User dashboard
```

### Definition of Done

GameNode can supervise multiple real game servers over extended runtime and react predictably to configured crash/health states.

---

## Milestone 7 — Operations & Diagnostics

Implement:

- Persistent platform settings
- Settings UI
- Diagnostics / system information
- Sanitized support bundle
- Support bundle download
- Support bundle auditing

Status:

IMPLEMENTATION_COMPLETE
RUNTIME_ACCEPTANCE_PENDING

---

## Milestone 8 — TBD

Milestone 8 is intentionally not yet defined.

Do not infer Milestone 8 from the numbered functional-requirement sections above.
In particular, "8. File Browser" is a functional-requirement chapter and was implemented as part of Milestone 4.

Milestone 8 must be explicitly defined before implementation begins.

---

# 23. Explicit Non-Goals for v0.1

Do not implement the following unless a small supporting abstraction is strictly necessary for current functionality:

```text
Cluster
Master/Worker
Controller Node
Multi-Node Management

Billing
Payments
Subscriptions

Docker Runtime
Podman Runtime

SteamCMD automation
Template marketplace
Game marketplace
Mod manager
Plugin system

Backups
Scheduling

Automatic firewall management
NAT management
UPnP

Kubernetes
Redis
Message Queue
PostgreSQL requirement
```

These may be documented as future ideas but must not delay v0.1.

---

# 24. Future Direction

The architecture should make future additions possible without implementing them prematurely:

```text
Docker runtime
Podman runtime
Game templates
SteamCMD installer
Backups
Scheduled tasks
Plugins
Cluster management
Controller node
Remote nodes
mTLS
gRPC
Central identity
Billing
```

Apply YAGNI.

Do not build generalized distributed-system infrastructure until the single-node product needs it.

---

# 25. Development Workflow

Work iteratively.

Before each milestone:

1. inspect the current architecture;
2. produce a short implementation plan;
3. define or adjust data model/API contracts;
4. implement;
5. add tests;
6. run tests;
7. run linters/formatters;
8. verify Windows and Linux builds where practical;
9. document changes.

Avoid large, untested code drops.

When a fundamental architecture decision is required, create an ADR under:

```text
docs/adr/
```

Each ADR should describe:

```text
Problem
Options
Trade-offs
Decision
Consequences
```

---

# 26. Documentation

Maintain at least:

```text
README.md
docs/architecture.md
docs/security.md
docs/development.md
docs/api.md
docs/adr/
```

README must allow a developer after a fresh clone to quickly understand:

```text
How to run backend
How to run frontend
How to run tests
How to build production artifacts
```

---

# 27. Quality Priorities

The goal is not a UI mockup or disposable prototype.

The goal is a usable first release of a game server control panel.

Priority order:

```text
1. Security
2. Reliable process management
3. Correct authorization
4. Data integrity
5. Cross-platform behavior
6. Operability
7. Usability
8. Visual polish
```

Prefer fewer complete features over many half-implemented features.

---

# 28. Important Design Rule

A GameNode restart or update should not automatically imply that every managed game server must stop.

The initial implementation does not need perfect process reattachment, but the runtime design must preserve a path toward reliable process rediscovery/reconnection on both Windows and Linux.

Document any limitations explicitly rather than hiding them.

---

# 29. Initial Codex Execution Scope

When starting the project, implement **Milestone 1 only**.

Do not automatically continue to Milestone 2.

The first implementation pass should create:

```text
1. Repository structure
2. Architecture documentation
3. Initial database schema
4. Migration system
5. Go HTTP server
6. React + TypeScript + Vite frontend
7. Embedded production frontend
8. Configuration system
9. Structured logging
10. Initial setup flow
11. Authentication
12. Admin dashboard skeleton
```

After implementation:

- run all tests;
- run formatting/linting;
- produce/verify Windows build;
- produce/verify Linux build;
- update documentation;
- inspect the diff for accidental complexity;
- stop before Milestone 2.

Provide a final implementation report containing:

```text
Implemented
Tests
Known limitations
Architecture decisions
Files changed
How to run
How to build
Recommended next step
```

Wait for explicit approval before starting Milestone 2.
# v0.2 — Egg template foundation status

The initial v0.2 template-import foundation is implemented. Pelican/Pterodactyl v2 Eggs are treated solely as untrusted import documents and normalized into a GameNode-owned template model. The milestone includes bounded parsing, typed variables and conservative sensitive classification, deterministic compatibility findings, safe direct-process startup extraction, native SteamCMD plan detection, normalized SQLite persistence, global template RBAC, mutation audit events, import/preview API routes, a Templates UI, and a representative 7 Days to Die golden fixture.

The follow-up native provisioning milestone is also implemented: a fixed-source managed SteamCMD installation, safe archive extraction, structured anonymous app installation, persisted asynchronous jobs with cancellation/restart interruption handling, per-server template values and sensitivity metadata, provisionability/platform checks, transactional normal-server creation, API/RBAC/audit coverage, and a Create Server wizard with progress/failure states.

Still deferred are automatic/update-on-start lifecycle integration, credentialed Steam login and Steam Guard, dependable port suggestions where Eggs do not provide a normalized source, and encrypted-at-rest environment secrets. The v0.3 Docker Container runtime and v0.4 controlled Egg container execution are implemented; Remote Nodes, generic scheduling, URL/repository synchronization, marketplace functionality, and generic host shell startup remain out of scope. Typed local daily/weekly server restart schedules are implemented separately and never schedule remote or cluster work.

# v0.2 — Official Remote Game Library status

The repository now owns `templates/catalog.json` and reviewed Official template JSON below `templates/`. GameNode reads only the fixed HTTPS GitHub Raw `main` source, validates schema/path/content with bounded network behavior, keeps a last-good data-directory cache, isolates malformed entries, and exposes explicit remote/cache/offline state through the Game Library API and UI. Official templates remain read-only transient/cache data; imported Eggs remain DB-backed user resources. Search and filters are client-side, refresh uses `Templates.View` plus CSRF, and server creation continues through the existing resolver/provisioning paths. Template source/version provenance is captured with provisioned-server variable metadata; updates never migrate existing servers.

Minecraft NeoForge is the first Official entry. It adopts an existing installation through a conservative Windows/Linux resolver that derives Java argfiles without executing Batch or shell code. Typed heap values, `nogui`, Java discovery, direct process ownership, attached stdout/stderr/stdin, graceful stdin `stop`, timeout escalation, and restart-safe console session identity are represented in the normal server/runtime model. The local reference resolves as NeoForge `26.2.0.59` / Minecraft `26.2`.

# v0.2 — Official SteamCMD templates status

The Official schema now supports exact platform launch maps, optional safe relative working directories, typed launch arguments, stop metadata, and explicit port declarations. 7 Days to Die remains the first data-driven entry. Project Zomboid is now the second (App ID `380870`) and the first fully real-accepted Windows Steam game: the native service installed and validated the current depot, created a normal server with Official `1.0.0` provenance and declared ports, launched the bundled Java runtime without Batch, reached `SERVER STARTED`, accepted console input, saved, and exited cleanly through `quit`. Template `1.1.0` adds managed INI settings without changing existing servers. Linux remains intentionally undeclared until independently acceptance-tested. Workshop, credentialed Steam, automatic updates, and real-game CI downloads remain out of scope.

# v0.2 — Versioned game configuration adapters status

Official product data is grouped per game. Templates reference same-directory adapter definitions fetched and cached through the fixed catalog source. Compiled formats now include bounded `xml-properties` and strict, sectionless `ini-key-values`. Project Zomboid template `1.1.0` persists its adapter at provisioning but waits for the first game start to generate `Server/gamenode.ini`; the UI reports this pending state and never invents a partial upstream file. Updates preserve unknown keys, comments, ordering, BOM, and line endings. The Configuration tab validates typed edits, masks secrets, keeps a last-file backup, writes atomically, requires `Server.Edit` plus CSRF, records redacted audit metadata, and never restarts a server automatically. Lua configuration remains outside this milestone.

Automatic NeoForge/Minecraft download, version catalogs, EULA mutation, mod/plugin management, generic script execution, and child-process handoff remain deferred. Real start/help/stop acceptance requires a compatible Java installation on the test host.
# v0.2.1 — SteamCMD Server Updates status

This is a small, intermediate release, not a new numbered milestone. Manual, operator-triggered SteamCMD updates are now implemented for already-provisioned SteamCMD-managed servers: `internal/serverupdates` reuses `internal/steamcmd.Manager.Install` (the same structured, argv-based `+force_install_dir`/`+login anonymous`/`+app_update`/`+quit` invocation used by initial provisioning) against an existing server's persisted managed root, gated by a new minimal trusted metadata snapshot (`server_steamcmd_provisioning`, migration `023_server_update_metadata.sql`) captured once at provisioning time. The update requires the server to already be stopped, runs as a persisted, cancellable, bounded job (`server_update_jobs`/`server_update_job_events`), and validates the persisted launch executable afterward through `servers.Service.VerifyLaunchExecutablePresent`. It never re-resolves the template, never touches ports/configuration adapters/server identity, and never migrates a server to a newer catalog template version. A new independent `Server.Update` permission (not inherited from `Server.Edit`, `Server.Start`, or `Templates.Manage`) gates `GET/POST /api/v1/servers/{id}/update` and `GET/POST /api/v1/server-update-jobs/{id}[/cancel]`. Servers provisioned before this metadata existed, or provisioned outside the SteamCMD path, are reported ineligible rather than guessed.

Automatic updates, update-on-start, scheduled/periodic updates, and template migration remain explicitly out of scope and are not implemented by this release.

# Local Scheduled Server Restarts

GameNode supports a deliberately narrow local scheduling feature for recurring
server restarts. Each persisted schedule is daily or weekly, stores an IANA
timezone and a controlled `HH:MM` wall-clock time, and may be enabled or
disabled independently. Multiple schedules may be attached to one server.
The scheduler loads enabled rows at startup, skips missed occurrences, and
re-reads the row before triggering. Execution always delegates to the normal
`servers.Service.Restart` lifecycle, so Native, Container, and Egg-backed
Container servers share the same behavior. There is no generic cron payload,
scheduled command, update, image pull, provisioning job, Remote Node schedule,
or cluster placement.

# v0.3 — Container Runtime

v0.3 adds a Linux-first Docker Engine API runtime alongside Native servers.
It uses typed images, explicit Pull, typed CPU/RAM limits, host-to-container
ports, ConsoleManager attach, and verified server/generation/token ownership.
Native remains first-class. The v0.4 container-backed Egg execution milestone below
extends this runtime without creating a second lifecycle authority.

# v0.4 — Container-backed Egg Runtime

Status: IMPLEMENTATION_COMPLETE, RUNTIME_ACCEPTANCE_PENDING.

v0.4 is implemented. Conservative Pelican/Pterodactyl imports now retain separate
Native and Container compatibility paths. A user must explicitly select Container
provisioning; the node validates declared image references against the administrator
registry allowlist, explicitly pulls the selected game and installer images, and
persists the selected image/digest when the Engine exposes one. Egg installation runs
only in a short-lived unprivileged container with the fixed `/home/container` server
root mount, bounded CPU/memory/PIDs/tmpfs, timeout/cancellation, output redaction,
and guaranteed cleanup. No Docker CLI, host shell, daemon socket mount, host network,
host PID/IPC, devices, capabilities, arbitrary mounts, registry credentials, or raw
Egg JSON is accepted.

Startup expansion is limited to declared template variables and `SERVER_ROOT`; the
normalized snapshot pins provenance, image/digest, startup, sensitivity, ports,
resources, and safe configuration operations. Properties/key-value/JSON configuration
semantics use compiled bounded operations only; unsupported required semantics are
reported as findings. The existing persisted provisioning jobs, RBAC/CSRF/audit
boundaries, `files_may_remain`, and owner/admin registration recovery flow are reused.
The frontend exposes separate compatibility findings, runtime/image/resource
selection, bounded installer progress, and container port mappings.

Remote Nodes, generic scheduling, automatic updates, and template migration remain v0.5+
scope; the narrow local restart schedule is documented above and does not
change the Remote Node boundary. v0.4 and v0.5A were developed concurrently
on separate branches/worktrees and have since been integrated onto a common
base (see "Parallel development note" below); v0.4's Egg Container Runtime is
now advertised as a Remote Node capability (`egg_container_runtime`) without
`internal/nodes`/`internal/remote` importing any Egg internals.

# v0.5A — Remote Node Foundation status

Status: IMPLEMENTATION_COMPLETE.

v0.5A builds the secure, autonomous Remote Node foundation that later remote server management depends on. It intentionally does **not** implement remote server management itself.

Implemented in this milestone:

- **Durable local Node Identity** (`internal/nodeidentity`): a random `NodeID` generated once and persisted in local SQLite, independent of hostname/IP/database path; an optional operator-set display name; a small integer protocol version (currently `1`), independent of the product version string; and a fixed, reviewed list of typed capability identifiers describing what this build actually implements.
- **Secure pairing/enrollment** (`internal/nodes`): an operator on the target node generates a single-use, 15-minute pairing token; an operator on the controller side supplies it and the node's endpoint to enroll. The node atomically consumes the token and issues a new machine credential; only salted hashes of tokens/credentials are ever stored, and plaintext values are returned exactly once, never logged or audited.
- **Authenticated Controller → Node communication**: a machine-authenticated Node API (`GET /api/v1/node/info|health|capabilities`, `POST /api/v1/node/enroll`) structurally separate from the human browser-session/RBAC/CSRF trust domain, and a human-authenticated, RBAC- and CSRF-protected controller-facing registry API (`/api/v1/remote-nodes*`, `POST /api/v1/node/pairing-tokens`).
- **A narrow typed remote client** (`internal/remote`): `Enroll`/`GetNodeInfo`/`GetHealth`/`GetCapabilities` only, bounded timeouts and response size, real TLS verification, endpoint normalization/validation, and no cross-host redirect following (SSRF/credential-leak mitigation).
- **Health/connection state** that is presentation-only and never authoritative over a remote node's own server/runtime lifecycle, refreshed by a bounded, periodic, cleanly-cancellable background loop where one unreachable node never blocks another.
- **RBAC** (`Node.View`/`Node.Manage`, global-only), **audit** for enrollment/registry mutations and pairing-token issuance (never for routine health polls or credentials), and a **read-only Nodes UI** (list, detail, pairing, enrollment).
- A dedicated ADR (`docs/adr/0007-remote-node-foundation.md`) records the trust/protocol decisions.

Deliberately out of scope for v0.5A: remote Server Create/Edit/Start/Stop/Restart/Kill, remote Console/Files/Ports mutation, remote provisioning/Egg installation, template/secret distribution, server migration, placement/scheduling, failover, and any shared/cluster database state. Every GameNode installation - enrolled or not - keeps its own local SQLite, API, UI, `servers.Service`, runtimes, ConsoleManager, Filesystem service, and lifecycle authority; a controller never gains direct access to a remote node's database or Docker/process runtime.

## Future milestones

- **v0.5B — Remote Server Management**: NOT STARTED. Remote server CRUD and lifecycle control through the Node API established in v0.5A.
- **v0.5C — Remote Operational Hardening**: NOT STARTED. Remote console/files, richer health, and operational polish for multi-node management.
- **v0.6 — Cluster / Scheduling (Foundation)**: IMPLEMENTATION_COMPLETE for the scope described below. Placement DECISION, capacity-awareness, and a deterministic scheduling algorithm are implemented; migration, failover, and controller election remain deferred.

# v0.6 — Cluster / Scheduling (Foundation) status

Status: IMPLEMENTATION_COMPLETE for the scope below.

v0.6 adds a deterministic, tenant-isolated, RBAC- and audit-gated cluster placement DECISION engine (`internal/placement`) that evaluates every node this installation knows about - itself plus every node enrolled in the v0.5A Remote Node registry - for one new server of a given runtime type (`native`/`container`) and tenant. `Decide` is pure (no I/O, no clock, no map-order dependency) and is unit-tested with fixed candidate inputs and expected outputs (`internal/placement/placement_test.go`).

**Explicit gap identified before implementation**: v0.5B (Remote Server Create/Edit/Start/Stop) and v0.5C (remote operational hardening) are both NOT STARTED. There is no way, anywhere in this codebase, to create or mutate a server on a Remote Node - only the v0.5A read-only Node API (`GetNodeInfo`/`GetHealth`/`GetCapabilities`) exists. A placement decision therefore cannot be executed for a Remote Node target; it can only be proposed. For the LOCAL node, execution is already possible through the existing, unmodified `servers.Service`/provisioning create path, so a `local_only` decision is actionable today.

**Decision vs. Execution boundary** (see `docs/adr/0009-cluster-scheduling-decision-vs-execution.md`, the minimal necessary preparatory outcome of this milestone): the placement API computes and returns a decision only. It never itself creates, starts, or otherwise mutates a server - not even for a `local_only` result. Every decision carries an `execution` field: `local_only` (selected node is this installation; act on it through the ordinary server-create API, unchanged) or `requires_v0.5b` (selected node is a Remote Node; there is currently no way to act on it).

**Capacity model**: the local node's usage is a live count of every server `servers.Service` already tracks, compared against a fixed, non-configurable `DefaultMaxServersPerNode = 50` constant (there is no existing per-node capacity configuration concept anywhere in the product to build on; making this operator-configurable is future work). A Remote Node's capacity is always reported unknown: v0.5A's Node API does not expose server counts or resource usage, and this milestone deliberately does not add a remote listing/capacity call - that would be new remote-facing surface belonging to v0.5B/v0.5C, not a scheduling decision engine. An unknown-capacity node is still eligible but always ranked behind every node with verified spare capacity.

**API**: `GET /api/v1/cluster/capacity?tenant_id=…` (read-only candidate/capacity listing, `Cluster.View`) and `POST /api/v1/cluster/placement` (`{tenant_id, runtime_type}` → a `Decision`, `Cluster.Schedule`). Both permissions accept `global` and `tenant` scope only (no `server` scope - a server does not exist yet at decision time, the same rule `Server.Create` already uses) and are evaluated against the requested tenant before the tenant's existence is even confirmed, so a caller without the permission learns nothing about tenant existence. Capacity numbers are node-wide, not a per-tenant breakdown, so there is no cross-tenant leak in the response itself.

**Audit**: one `cluster.placement_decide` event per placement request (`Success` when a node was selected, `Failure` with the rejection reason otherwise); the read-only capacity listing and the (nonexistent, by design) remote polling this milestone does not add are never audited, matching the existing "no audit for routine reads/health polls" convention.

**Not implemented, on purpose**: any generic cron/job scheduler; any change to the unrelated v0.4 local restart scheduler (`internal/scheduler`, never touched by this milestone); any Remote Node lifecycle mutation beyond the already-existing v0.5A read-only calls; server migration between nodes; failover; controller election.

## Parallel development note

v0.5A was implemented in a separate git worktree from a concurrently developed v0.4 (Container-backed Egg Runtime), then integrated onto v0.4 as a foundation-only merge (`feature/v0.5-remote-node-foundation`). v0.5A touches no Egg parser/importer/normalization/install-script/startup code, no container-backed execution, no provisioning-job internals, and no template/adapter internals; `internal/nodes`/`internal/remote` still do not import Egg internals after integration. `cmd/gamenode/main.go` preserves v0.4's full startup wiring (config/logging/DB, Native/Container/Egg runtime, provisioning, console, filesystem, monitoring, SteamCMD, restart scheduler, server updates) plus v0.5A's Remote Node registry/client/heartbeat wiring, including clean heartbeat shutdown. `internal/rbac/catalog.go`, `internal/audit/audit.go`, and `internal/api/api.go` gained additive entries from both branches (new permissions/actions/routes/struct fields) rather than either side overwriting the other. The only semantic (non-mechanical) integration step was adding the `egg_container_runtime` capability to `internal/nodeidentity.Capabilities()` now that v0.4 is genuinely present on this branch; the Remote Node protocol version was not bumped, since the wire contract itself did not change. The v0.5A migration `023_remote_nodes.sql` was renumbered to `026_remote_nodes.sql` to avoid colliding with v0.4's `023_container_runtime.sql`/`023_server_update_metadata.sql`, `024_egg_container_runtime.sql`, and `025_server_restart_schedules.sql`.
