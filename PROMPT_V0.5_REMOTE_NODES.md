# Codex Prompt – GameNode v0.5 Remote Nodes

Du arbeitest im GameNode-Repository. Implementiere **ausschließlich Milestone v0.5 – Remote Nodes** aus `CONTAINER_EGG_REMOTE_ROADMAP.md`.

## Precondition

v0.3 Container Runtime und v0.4 Egg Runtime müssen bereits abgeschlossen und dokumentiert sein.

Wenn diese Voraussetzungen fehlen, STOP. Implementiere sie nicht als Teil dieses Milestones.

## Vorbereitung

Lies vollständig:

- `AGENTS.md`
- `PROJECT_PLAN.md`
- `README.md`
- `docs/architecture.md`
- `docs/security.md`
- `docs/runtime.md`
- `docs/api.md`
- `docs/development.md`
- `docs/ci.md`
- relevante ADRs
- `CONTAINER_EGG_REMOTE_ROADMAP.md`

Inspektiere danach insbesondere:

- auth/session/RBAC;
- API composition;
- servers.Service;
- console/files/monitoring/ports/configuration;
- provisioning;
- audit;
- settings/diagnostics;
- current server DTO/capability model;
- migrations;
- frontend navigation.

## Ziel

Eine GameNode-Instanz kann andere autonome GameNode-Instanzen sicher als Remote Nodes verwalten.

Primärer Acceptance Use Case:

```text
Windows Controller
    ↓ secure node protocol
Linux GameNode Node
    ↓
Container Server
```

Noch kein automatisches Scheduling.

## Unverhandelbare Architekturregeln

1. Jede Node bleibt vollständig autonom mit eigener UI, API, SQLite und Runtime.
2. Kein Shared SQLite, kein Network-Share als Datenbank, kein zentraler Prozessmanager.
3. Node bleibt Source of Truth für ihre lokalen Server.
4. Controller darf Node-Services nur über ein dediziertes, versioniertes Node-Protokoll aufrufen.
5. Das normale Browser-Sessioncookie ist kein Node-Credential.
6. Benutzer-Actor-Snapshots dienen Audit/Attribution, nicht der Node-Autorisierung.
7. Pairing muss explizit, widerrufbar und replay-resistent sein.
8. Nach Pairing mTLS oder eine gleichwertige gegenseitig authentisierte Transportgrenze; bevorzugt mTLS.
9. Private Keys, Pairing Secrets, Session/CSRF und Zertifikatsmaterial dürfen nicht in Audit/Support/normalen APIs leaken.
10. Remote Console/File Streams bleiben bounded und backpressure-sicher.
11. Offline Nodes dürfen nicht mit stale live state dargestellt werden.
12. Keine Scheduler-/Placement-Logik in diesem Milestone.

## Node-Modell

Füge ein persistentes Controller-Modell hinzu, mindestens:

```text
nodes
node trust/identity
remote_server_refs
```

Eine Node hat:

- stable node ID;
- display name;
- endpoint;
- trust state;
- certificate/public identity metadata;
- enabled/disabled;
- last successful contact;
- protocol version;
- capabilities.

Speichere keine vollständige Kopie ihrer Serverdaten als zweite Source of Truth.

## Pairing Flow

Implementiere einen sicheren Einmal-Pairing-Flow.

Empfehlung:

1. Node Admin aktiviert remote management.
2. Node erzeugt kurzlebigen one-time pairing code/token.
3. Controller Admin gibt Node endpoint + code ein.
4. Beide Seiten validieren peer identity und etablieren dauerhafte mTLS identities.
5. Pairing code wird invalidiert.
6. Spätere Kommunikation nutzt ausschließlich die gepaarte Identität.
7. Unpair/disable/revoke möglich.

Anforderungen:

- Ablaufzeit;
- single use;
- brute-force bounds/rate limits;
- no default secret;
- safe audit metadata;
- controlled errors.

Wenn die genaue PKI-Struktur eine dauerhafte Entscheidung ist, erstelle eine ADR.

## Node Protocol

Baue ein dediziertes versioniertes Service-to-Service-Protokoll, z. B. `/api/node/v1`.

Nicht einfach alle normalen Browser-Endpunkte ungeschützt wiederverwenden.

Benötigte Bereiche:

### Discovery/Health

- node identity;
- version;
- protocol version;
- OS/arch;
- runtime capabilities;
- container engine availability;
- CPU/memory/disk summary;
- health.

### Servers

- list/get;
- create/adopt/provision as supported;
- lifecycle;
- ports;
- monitoring;
- configuration.

### Files

- list/read/edit/create/move/delete/upload/download entsprechend normaler Node-Services;
- alle bestehenden Filesystem-Sandbox-Garantien bleiben lokal auf der Node autoritativ.

### Console

- stream output;
- bounded history;
- input;
- live authorization/trust checks;
- backpressure.

### Provisioning

- start;
- status/progress;
- cancel;
- result.

## Controller Authorization

Flow:

1. Browser user authenticates to Controller.
2. Controller evaluates normal RBAC.
3. Controller invokes Node as trusted controller service identity.
4. Node verifies controller pairing/permission.
5. Node receives controlled actor snapshot for audit attribution.

Ein manipulierter Actor Snapshot darf niemals zusätzliche Node-Rechte erzeugen.

Definiere klar, ob ein gepaarter Controller Vollzugriff auf die Node erhält oder per Node-spezifischen remote permissions eingeschränkt werden kann. Für v0.5 ist ein expliziter `controller_can_manage_node` Trust Grant akzeptabel, solange er lokal widerrufbar und dokumentiert ist.

## Remote Server References

Controller benötigt stabile Referenzen auf Remote Server:

```text
node_id
node_server_id
optional cached display metadata
```

Kein Duplicate Server State als Autorität.

Bei Node unreachable darf Controller cached metadata nur als stale anzeigen.

## Audit

Controller und Node auditieren semantisch.

Node Audit soll mindestens kontrolliert unterscheiden:

```text
origin=remote_controller
controller_id
actor_user_id snapshot
actor_username snapshot
```

Keine User Session Tokens oder CSRF vom Controller übertragen.

## UI

Füge Nodes-Verwaltung hinzu:

- list;
- add/pair;
- status;
- capabilities;
- disable/unpair;
- detail;
- remote server list.

Server Create erhält manuelle Node-Auswahl:

```text
Node
○ Local
○ linux-01
○ linux-02
```

Noch kein `Automatic` Placement.

Server Detail auf Remote Servern soll bestehende Tabs soweit möglich wiederverwenden, aber alle Daten über Controller→Node transportieren.

## Offline/Failure Semantik

Mindestens:

```text
online
unreachable
authentication_failed
version_incompatible
disabled
```

Keine Operation darf bei unbekanntem Ergebnis blind retryen und dadurch doppelte Creates/Provisioning Jobs erzeugen.

Create/Provisioning benötigen idempotency/operation identity oder eine andere belastbare Duplicate-Schutzstrategie.

## Version Negotiation

Node Protocol muss versioniert sein.

Controller soll Features anhand Node capabilities/protocol version ein-/ausblenden.

Unbekannte inkompatible Versionen kontrolliert ablehnen.

## Streaming

Downloads/Uploads und Console nicht unbounded im Controller puffern.

Verwende streaming mit context cancellation und bounded buffers.

## Tests

### Security

- expired pairing code;
- reuse pairing code;
- wrong node/controller cert;
- revoked/unpaired controller;
- actor spoof cannot authorize;
- version mismatch;
- secret redaction;
- trust disabled.

### Integration

Starte mindestens zwei getrennte GameNode-Instanzen mit getrennten Data/DB roots in Tests:

- pair;
- list capabilities;
- remote create lightweight server;
- lifecycle;
- monitoring;
- console;
- files;
- provisioning status;
- disconnect/reconnect;
- unpair.

### Cross-OS acceptance

Wenn Infrastruktur vorhanden:

- Controller Windows;
- Node Linux;
- Docker Container Server auf Linux;
- vom Windows Controller start/console/files/stop.

Nur als bestanden dokumentieren, wenn frisch ausgeführt.

## Migration/Docs

Prüfe höchste Migration, füge nur neue immutable Migrationen hinzu.

Dokumentiere mindestens:

- `PROJECT_PLAN.md` v0.5;
- `AGENTS.md` node protocol/trust invariants;
- `README.md`;
- `docs/architecture.md`;
- `docs/security.md`;
- `docs/api.md` oder eigenes `docs/node-protocol.md`;
- `docs/development.md`;
- ADR für Pairing/mTLS/source-of-truth.

## Harte Stop-Grenze

Nicht implementieren:

- automatic placement;
- scheduler;
- capacity reservations für Placement;
- node labels für Scheduling;
- live migration;
- shared storage;
- automatic failover;
- controller HA;
- consensus/Raft;
- distributed SQLite;
- node autoscaling.

Nach v0.5 STOP. Nicht mit v0.6 beginnen.

## Abschlussbericht

Berichte:

- Scope/Status;
- Pairing/mTLS Architektur;
- Node Protocol Versionierung;
- Source-of-Truth Modell;
- Security Review;
- Dateien/Migrationen;
- Backend/Race/Frontend;
- Builds;
- Multi-process integration;
- Cross-OS acceptance falls wirklich ausgeführt;
- Limits/Skips;
- Bestätigung, dass Scheduler nicht implementiert wurde.
