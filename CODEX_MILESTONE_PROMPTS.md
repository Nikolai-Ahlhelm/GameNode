# GameNode – Codex Prompts für die einzelnen Milestones

> Diese Datei bündelt die vier eigenständig ausführbaren Codex-Prompts. Starte immer nur den Prompt des aktuell freigegebenen Milestones.


---

# Codex Prompt – GameNode v0.3 Container Runtime

Du arbeitest im GameNode-Repository. Implementiere **ausschließlich Milestone v0.3 – Container Runtime** aus `CONTAINER_EGG_REMOTE_ROADMAP.md`.

## Verbindliche Vorbereitung

Lies zuerst vollständig:

- `AGENTS.md`
- `PROJECT_PLAN.md`
- `README.md`
- `docs/architecture.md`
- `docs/security.md`
- `docs/runtime.md`
- `docs/api.md`
- `docs/development.md`
- `docs/ci.md`
- relevante ADRs unter `docs/adr/`
- `CONTAINER_EGG_REMOTE_ROADMAP.md`

Inspektiere danach den aktuellen Code und die Tests, insbesondere:

- `internal/servers`
- `internal/runtime`
- `internal/console`
- `internal/monitoring`
- `internal/ports`
- `internal/filesystem`
- `internal/api`
- `internal/audit`
- `internal/provisioning`
- `internal/templates`
- `migrations`
- relevante Frontend-Komponenten unter `web`

Die aktuelle Repository-Dokumentation verbietet Container noch als Future Scope. **Als erster Schritt dieses Milestones** muss `PROJECT_PLAN.md` v0.3 ausdrücklich definieren und die entsprechenden harten Container-Non-Goals in `AGENTS.md` in konkrete neue Container-Invarianten umwandeln. Ändere dabei nicht rückwirkend die historischen v0.1/v0.2 Aussagen; ergänze den neuen Milestone sauber.

## Ziel

GameNode erhält neben der bestehenden Native Runtime eine zweite **Linux-first Container Runtime**. Native Server bleiben unverändert funktionsfähig.

Der Milestone ist erfolgreich, wenn ein GameNode-Server mit `runtime_type=container` einen Docker-basierten Container sicher erstellen, starten, stoppen, killen, restarten, überwachen und über Console/Files/Ports verwalten kann.

## Architekturregeln

1. `servers.Service` bleibt Autorität für normalen Server-Lifecycle.
2. API-Handler dürfen nicht direkt Docker steuern.
3. Kein Shelling-Out zu `docker` oder beliebigen CLI-Kommandos. Verwende eine kleine interne Docker Engine API Grenze.
4. Native Runtime darf nicht regressieren.
5. Container sind eine zweite Runtime, kein Ersatz.
6. Persistentes Server-Root bleibt GameNode-hostseitig und wird kontrolliert in den Container gemountet.
7. Files API arbeitet weiterhin über `internal/filesystem` auf dem Server-Root, nicht als generischer Container-Filesystem-Browser.
8. Port-Publishing wird ausschließlich aus validierten GameNode-Portrecords erzeugt.
9. Kein `--privileged`, kein Docker Socket Mount, kein Host Network, kein Host PID/IPC, keine beliebigen Host Mounts, keine freien Engine Flags.
10. Secrets dürfen nicht in Logs, Audit, Support Bundle, API-Fehler, Pull-Progress oder Runtime-Diagnostics leaken.
11. Stale Runtime-Instanzen dürfen niemals State einer neueren Instanz überschreiben.
12. GameNode-owned Container müssen über sichere Labels und konkrete Container-ID verifiziert werden; Containername allein reicht nicht.

## Scope

Implementiere mindestens:

- `runtime_type = native | container` bzw. eine rückwärtskompatible äquivalente Modellierung;
- persistente Container-Konfiguration;
- kleine Docker Engine Abstraktion;
- Linux Docker Backend;
- Windows/unsupported stub, sodass Windows Builds weiterhin funktionieren;
- Container create/start/stop/kill/restart/status;
- sichere Runtime-Instance-Identity für Container;
- sichere Rediscovery nach GameNode-Neustart;
- Console output + input über Engine attach, falls supported;
- CPU/RAM Monitoring;
- Memory Limit;
- CPU Limit in millicores oder einer sauber dokumentierten äquivalenten Einheit;
- persistentes Server-Root Mount;
- Host→Container Port Mapping;
- Port Preflight über bestehende Registry;
- Auto Restart über die bestehende `servers.Service` Semantik;
- RBAC/CSRF/Audit für neue Mutationen;
- API und UI für Container Server;
- Engine availability/provisionability state;
- kontrolliertes Image Pulling;
- Migrations und Tests.

## Image-Regeln für v0.3

Dieser Milestone ist noch kein Egg Runtime Milestone. Implementiere eine konservative Image-Eingabe für explizit erstellte Container Server.

- validiere Image References strikt;
- keine Registry Credentials;
- keine freien Pull URLs;
- persistiere tatsächliche Image-Referenz und, wenn verfügbar, Digest;
- kein automatisches Update/Pull bei jedem Start;
- ein existierender Server bleibt an seiner persistierten Konfiguration gebunden.

## Runtime Identity

Refaktoriere die native PID+StartKey-Semantik nur so weit wie nötig. Das Ziel ist eine generische Runtime-Instanzidentität mit denselben stale-generation Garantien.

Container-Identität muss mindestens enthalten:

- Container Engine ID;
- GameNode server ID / ownership label;
- GameNode instance generation.

Wenn diese Änderung eine dauerhafte Architekturentscheidung ist, ergänze/erstelle eine ADR.

## Container Restart / Rediscovery

Beim GameNode-Neustart:

- laufende native Prozesse bleiben nach bestehender Semantik console-detached;
- laufende GameNode-owned Container dürfen nur nach Ownership-/Identity-Verifikation rediscovered werden;
- Engine re-attach darf verwendet werden, wenn zuverlässig implementiert;
- bei Unsicherheit explizit detached/degraded, niemals erfundene Attachment-Semantik.

## Datenbank

Prüfe die aktuelle höchste Migration und füge die nächste freie Migration hinzu. Ändere keine angewandte Migration.

Bevorzuge runtime-spezifische Tabellen/Felder gegenüber einer unkontrollierten Erweiterung der gemeinsamen Serverzeile.

## API

Keine Docker CLI Payloads und keine generischen Engine JSON Pass-throughs.

API soll typisierte GameNode-Felder akzeptieren, z. B.:

- runtime type;
- image;
- resource limits;
- container port mappings;
- normale environment variables;
- normaler server lifecycle.

Backend bleibt autoritativ.

## Frontend

Füge Container-Unterstützung in die bestehende UI ein, ohne Routing/Framework unnötig umzubauen.

Pflichtzustände:

- Engine unavailable;
- image pull/preparation;
- validation errors;
- resource limits;
- port mappings;
- lifecycle;
- monitoring used/limit;
- ehrliche detached/degraded states.

## Tests

Füge Regressionstests für jede neue Invariante hinzu.

Mindestens:

- container spec validation;
- resource bounds;
- port mapping;
- unsafe mount rejection;
- privileged/host network rejection;
- ownership label verification;
- stale finalizer/generation;
- engine unavailable;
- start/stop/kill/restart via fake engine;
- unexpected exit + auto restart;
- manual stop != crash;
- port conflict blocks start before runtime mutation;
- rediscovery;
- console identity;
- monitoring metrics;
- RBAC/CSRF/Audit;
- native runtime regressions.

Erstelle optional einen echten Docker Integration Test, der standardmäßig übersprungen wird, z. B. über `GAMENODE_CONTAINER_INTEGRATION=1`. Normale CI darf nicht von einer lokalen Docker Engine abhängen, solange die Workflows nicht ausdrücklich geändert werden.

## Verification

Führe passend zur Umgebung mindestens aus:

```text
gofmt auf geänderten Go-Dateien
go vet ./...
go test ./...
go build ./...
```

Wenn verfügbar:

```text
go test -race ./...
```

Frontend:

```text
cd web
npm ci
npm run check
npm run test:helpers
npm run build
```

Danach Windows amd64 und Linux amd64 Build gemäß Repository-Dokumentation.

Wenn Docker verfügbar ist, führe einen kleinen opt-in Real-Container-Smoke aus und dokumentiere exakt, was tatsächlich getestet wurde. Behaupte keine Fresh Acceptance, die nicht ausgeführt wurde.

## Dokumentation

Synchronisiere mindestens:

- `PROJECT_PLAN.md`
- `AGENTS.md`
- `README.md`
- `docs/architecture.md`
- `docs/security.md`
- `docs/runtime.md`
- `docs/api.md`
- `docs/development.md`
- ggf. ADR.

## Harte Stop-Grenze

**Nicht** implementieren:

- Egg Installationsskripte;
- echte Pterodactyl/Pelican Container-Egg-Ausführung;
- Remote Nodes;
- Controller;
- Cluster;
- Scheduler;
- automatische Node-Auswahl;
- Registry Credentials;
- privileged containers;
- arbitrary host mounts;
- Docker Compose;
- Kubernetes;
- Live Migration.

Wenn v0.3 vollständig implementiert und verifiziert ist: STOP. Fahre nicht mit v0.4 fort.

## Abschlussbericht

Liefere am Ende einen evidence-basierten Bericht mit:

- Scope implementiert / finaler Status;
- Architekturentscheidungen;
- Security Review;
- Dateien geändert;
- Migrationen;
- Backend Tests;
- Race Result;
- Frontend Checks/Build;
- Windows/Linux Build;
- Docker Integration/Acceptance, falls wirklich ausgeführt;
- bekannte Limits/Skips;
- explizite Bestätigung, dass v0.4 nicht begonnen wurde.


---

# Codex Prompt – GameNode v0.4 Egg Runtime

Du arbeitest im GameNode-Repository. Implementiere **ausschließlich Milestone v0.4 – Egg Runtime** aus `CONTAINER_EGG_REMOTE_ROADMAP.md`.

## Precondition

Dieser Milestone darf nur beginnen, wenn v0.3 Container Runtime bereits implementiert ist und die Repository-Dokumentation sie als aktuellen unterstützten Scope beschreibt.

Wenn die Container Runtime nicht vollständig vorhanden ist, STOP und berichte die fehlenden Voraussetzungen. Implementiere v0.3 nicht nebenbei.

## Vorbereitung

Lies vollständig:

- `AGENTS.md`
- `PROJECT_PLAN.md`
- `README.md`
- `docs/templates.md`
- `docs/architecture.md`
- `docs/security.md`
- `docs/runtime.md`
- `docs/api.md`
- `docs/development.md`
- relevante ADRs
- `CONTAINER_EGG_REMOTE_ROADMAP.md`

Inspektiere Code/Tests insbesondere in:

- `internal/templates`
- `internal/provisioning`
- `internal/runtime`
- Container Engine Backend aus v0.3
- `internal/gameconfig`
- `internal/servers`
- `internal/api`
- `internal/audit`
- `web`
- `migrations`

## Ziel

Erweitere den bestehenden konservativen Pelican/Pterodactyl Egg Import um einen **Container Egg Runtime Path**.

Bestehender Native-Egg-Import bleibt erhalten.

Ein Egg kann künftig getrennt bewertet werden:

```text
Native compatibility
Container compatibility
```

Unterstützte Eggs dürfen ihre Installations- und Startup-Semantik innerhalb einer kontrollierten Container-Sandbox verwenden. Kein Egg-Code darf auf dem GameNode-Host ausgeführt werden.

## Kernarchitektur

```text
Egg JSON
  ↓
bounded parser
  ↓
compatibility analyzer
  ├── Native Template Plan (bestehend)
  └── Container Egg Runtime Plan (neu)
                  ↓
          provisioning.Service
                  ↓
          Installer Container
                  ↓
          persistent server root
                  ↓
          normal container Server
```

Kein zweites Lifecycle-System und keine Egg-spezifische Runtime außerhalb der normalen Server-/Container-Runtime.

## Security Boundary

Der zentrale neue Architekturentscheid ist:

- Host Shell bleibt strikt verboten.
- Egg Installations-/Startup-Shell-Semantik darf ausschließlich **innerhalb eines kontrollierten unprivileged Containers** ausgeführt werden.
- Niemals untrusted Egg-Strings an Host `sh -c`, `bash -c`, PowerShell, cmd.exe oder Docker CLI weitergeben.

Dokumentiere diese Grenze explizit in `docs/security.md` und über eine ADR, wenn noch nicht vorhanden.

## Image Policy

Egg `docker_images` darf als Runtime-Input verwendet werden, aber nur unter Policy.

Implementiere mindestens:

- strikt validierte Image References;
- Admin-konfigurierbare Registry-Allowlist oder eine klar begrenzte gleichwertige Policy;
- keine Registry Credentials in v0.4;
- Auswahl eines zulässigen Egg Images im Provisioning UI;
- Pull nur als expliziter Provisionierungsschritt;
- persistierte tatsächlich ausgewählte Image Ref und nach Möglichkeit Digest;
- keine automatische Migration bestehender Server bei Egg-Änderung.

Ein Egg mit ausschließlich geblockten Images ist container-unprovisionable und bekommt einen stabilen Compatibility/Provisionability Reason.

## Installer Container

Implementiere Egg Installationsskripte in einem kurzlebigen Installer-Container.

Pflicht:

- unprivileged;
- kein Docker Socket;
- kein host network;
- kein host PID/IPC;
- keine devices;
- keine arbitrary host mounts;
- ausschließlich GameNode Server-Root als persistenter beschreibbarer Mount;
- bounded temp space;
- CPU/RAM/PID limits;
- timeout;
- cancellation;
- bounded progress events;
- secret redaction;
- controlled error summaries;
- Container cleanup nach Ende;
- keine automatische recursive cleanup des Server-Roots, wenn Ownership/Commit-Semantik dies nicht sicher erlaubt.

## Startup

Normalisiere den Egg Startup String in eine **container-interne** Ausführung.

Anforderungen:

- expandiere nur deklarierte/validierte Egg Variablen und GameNode-eigene sichere Werte;
- keine Host Environment Expansion;
- keine Engine Flags aus Startup erzeugen;
- bounded strings;
- Secrets nur zur Runtime, nicht in API/Audit;
- container shell/entrypoint wird bewusst und testbar erzeugt.

## Variablen

Behalte bestehende typed validation und sensitive classification bei.

Erweitere nur, wenn reale Egg-Regeln dies benötigen. Unbekannte Validierungsregeln erzeugen kontrollierte Findings; nicht still emulieren.

## Egg Configuration

Implementiere nur konkrete, sichere Konfigurationsformen.

Priorität:

1. einfache properties / key=value;
2. JSON key replacement;
3. weitere Formate nur mit klarer Grammatik, Bounds und Fixtures.

Keine hostseitige generische regex/eval/script Engine.

Wenn ein Egg zwingende Config-Semantik benutzt, die GameNode nicht versteht, darf der Container Compatibility Status nicht fälschlich `compatible` sein.

## Provisioning

Erweitere das bestehende Provisioning Job-System. Kein zweites Jobsystem.

Phasen dürfen u. a. umfassen:

```text
validating_egg_runtime
checking_image_policy
pulling_install_image
creating_installer
running_install_script
validating_installation
pulling_runtime_image
resolving_container_startup
registering_server
completed
```

Jobs bleiben bounded, restart-safe gemäß bestehender Semantik und secret-safe.

## Persistierter Snapshot

Ein provisionierter Server muss unabhängig vom späteren Egg weiter funktionieren.

Persistiere normalisierte Snapshot-Daten, mindestens:

- source/provenance/hash/version metadata;
- selected runtime image;
- digest wenn verfügbar;
- normalized startup plan;
- variable sensitivity metadata;
- resolved ports;
- resource defaults/policy;
- config runtime snapshot.

Keine automatische Migration bei späterem Reimport/Update.

## Compatibility API/UI

Zeige Native und Container getrennt.

Beispiele:

```text
Native: Partial
Container: Supported
```

oder

```text
Native: Unsupported
Container: Blocked by image policy
```

Keine falsche "100% Egg compatibility" Behauptung. Unverstandene Funktionen werden als stabile Findings dargestellt.

## API/Frontend

Erweitere Template Detail/Create Flow so, dass der Operator den Container Egg Runtime Path explizit auswählt.

Zeige:

- selected image;
- image policy;
- variables;
- ports;
- CPU/RAM;
- installer progress;
- compatibility findings;
- `files_may_remain` und kontrollierte Fehler wie beim bestehenden Provisioning.

## Tests

Mindestens:

### Analyzer

- valid container Egg;
- invalid/blocked image;
- multiple images;
- installer script boundedness;
- secrets;
- unsupported required config;
- malformed startup;
- native path regressions.

### Provisioning mit Fake Engine

- pull install image;
- installer success;
- installer non-zero;
- timeout;
- cancel;
- container cleanup;
- persistent files;
- final runtime image;
- DB registration;
- DB failure after installation;
- no ghost server;
- secret redaction;
- port collisions;
- resources.

### API/RBAC/CSRF/Audit

- import/analyze unchanged;
- provision container egg;
- image policy errors;
- owner-only jobs;
- secret-safe events.

### Real integration

Wenn Docker verfügbar ist, führe opt-in mindestens zwei kleine Egg Fixtures/realistische Eggs aus, einschließlich eines mit Installationsskript. Keine großen Real-Game-Downloads in normaler CI.

## Dokumentation

Synchronisiere:

- `PROJECT_PLAN.md` – v0.4 Status/Scope;
- `AGENTS.md` – untrusted Egg container execution invariants;
- `README.md`;
- `docs/templates.md`;
- `docs/security.md`;
- `docs/architecture.md`;
- `docs/api.md`;
- `docs/development.md`;
- ggf. ADR.

## Harte Stop-Grenze

Nicht implementieren:

- Remote Nodes;
- Controller;
- mTLS Node Protocol;
- automatic placement;
- scheduler;
- live migration;
- shared storage;
- registry credentials;
- privileged Eggs;
- Docker Compose;
- Kubernetes;
- automatische server updates.

Nach vollständigem v0.4 STOP. Nicht mit v0.5 beginnen.

## Verification und Abschluss

Führe die vollständigen Backend-/Frontend-/Build-Prüfungen aus, die in `AGENTS.md`/`docs/development.md` verlangt werden. Führe Docker Acceptance nur aus, wenn die Umgebung sie wirklich unterstützt.

Berichte abschließend:

- implementierter Scope;
- Compatibility-Modell;
- Security Boundary für Egg Scripts;
- Dateien/Migrationen;
- Tests/Race/Frontend;
- Windows/Linux Builds;
- reale Egg/Docker Acceptance, falls wirklich ausgeführt;
- Limits/Skips;
- Bestätigung, dass v0.5 nicht begonnen wurde.


---

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


---

# Codex Prompt – GameNode v0.6 Cluster Scheduler

Du arbeitest im GameNode-Repository. Implementiere **ausschließlich Milestone v0.6 – Cluster Scheduler** aus `CONTAINER_EGG_REMOTE_ROADMAP.md`.

## Precondition

Remote Nodes aus v0.5 müssen bereits vollständig implementiert, dokumentiert und stabil sein.

Wenn nicht: STOP. Implementiere v0.5 nicht nebenbei.

## Vorbereitung

Lies vollständig:

- `AGENTS.md`
- `PROJECT_PLAN.md`
- `README.md`
- `docs/architecture.md`
- `docs/security.md`
- Node Protocol Dokumentation/ADR
- Runtime/Container/Egg ADRs
- `docs/api.md`
- `docs/development.md`
- `CONTAINER_EGG_REMOTE_ROADMAP.md`

Inspektiere:

- Node model/protocol;
- controller remote server refs;
- provisioning;
- ports;
- resource limits;
- monitoring;
- settings;
- migrations;
- current UI Create Server flow.

## Ziel

Controller kann neue Server automatisch auf einer geeigneten Remote Node platzieren.

Dies ist **Scheduling**, nicht HA und nicht Live Migration.

## Kerninvarianten

1. Node bleibt Source of Truth für lokale Server.
2. Controller besitzt Scheduler-/Reservation-State.
3. Capacity Accounting basiert auf deklarierter Node Capacity und reservierten Server-Ressourcen.
4. Momentane Monitoring-Auslastung ist Zusatzsignal, nicht alleinige Reservierungsgrundlage.
5. Parallel Creates dürfen Kapazität/Ports nicht doppelt reservieren.
6. Failed provisioning muss Reservations freigeben.
7. Retry darf keine doppelten Server erzeugen.
8. Node offline führt nicht zu automatischem Failover auf eine zweite Node.
9. Kein Shared Storage/Distributed DB.
10. Scheduler ist deterministisch und testbar; kein ML/komplexer Solver.

## Datenmodell

Implementiere mindestens:

- node capacity snapshot/config;
- node scheduling state (`active`, `cordoned`, `maintenance` oder äquivalent);
- bounded node labels;
- resource reservations;
- placement record;
- optional node port pools/reservations.

Resource units:

- memory in bytes;
- CPU in millicores (`1000 = 1 CPU`) oder eine klar dokumentierte gleichwertige Einheit.

## Capacity

Node meldet/Controller speichert sichere Kapazität:

- CPU logical capacity;
- total/allocatable memory;
- disk free/allocatable summary;
- supported runtimes;
- OS/arch;
- engine capabilities.

Definiere klar, ob Admin einen allocatable headroom abziehen kann, damit nicht 100% Host RAM verplant wird.

Empfehlung:

```text
allocatable_memory = total_memory - node_reserved_memory
allocatable_cpu = total_cpu_millis - node_reserved_cpu_millis
```

## Reservations

Server Create mit automatic placement muss vor Provisioning Ressourcen reservieren.

Reservation mindestens:

- operation/request ID;
- node ID;
- CPU;
- memory;
- ports, wenn automatisch;
- expiry/terminal state;
- associated server/provision job nach Commit.

Controller restart darf aktive Reservations nicht unkontrolliert vergessen. Persistiere sie in SQLite und reconcile kontrolliert anhand Node/Job state.

## Placement Constraints

Strukturierte Constraints, mindestens:

- runtime type;
- OS;
- architecture;
- required node labels;
- optional preferred labels;
- minimum free disk;
- image/registry capability, wenn relevant;
- sufficient CPU/memory.

Keine beliebige Expression Language.

## Scheduler Algorithmus

Einfach, deterministisch und dokumentiert.

Vorgehen:

1. unreachable/disabled/cordoned/maintenance Nodes entfernen;
2. hard capability constraints anwenden;
3. CPU/memory/disk/port capacity prüfen;
4. required labels anwenden;
5. preferred labels/available capacity score;
6. stabiler Tie-Breaker nach Node ID.

Keine spekulative Optimierung.

## Manual vs Automatic

Create UI/API:

```text
placement_mode: manual | automatic
```

Manual bleibt verfügbar und nutzt den v0.5 Flow.

Automatic wählt Node und erstellt Reservation.

## Node Labels

Admin kann bounded labels setzen, z. B.:

```text
region=home
storage=nvme
purpose=games
```

Validiere key/value Länge und Syntax.

Labels erzeugen keine RBAC-Rechte.

## Cordon / Maintenance

Implementiere klare States.

`cordoned`:

- keine neuen automatic placements;
- vorhandene Server laufen.

`maintenance`:

- ebenfalls keine neuen placements;
- UI warnt deutlich vor vorhandenen Servern;
- keine automatische Migration.

## Port Pools

Optional, aber empfohlen, wenn sauber in Scope passend:

Controller/Node kann einen host port range pool definieren.

Automatic placement kann freie Ports reservieren.

Wichtig:

- Node `ports.Service` bleibt letzte lokale Kollisionsprüfung;
- Reservation muss bei Failure zurückgerollt werden;
- extern belegter Port kann zwischen Probe und Start weiterhin kollidieren;
- kein Firewall/NAT Management.

Wenn Port-Pools den Milestone unverhältnismäßig aufblasen, implementiere zunächst sichere Capacity/Placement ohne automatische Portvergabe und dokumentiere Port-Autoallocation als nächsten klaren Slice. Ändere aber nicht still das Milestone-Ziel; begründe die Abgrenzung.

## Provisioning Flow

Ziel:

```text
validate request
  ↓
calculate eligible nodes
  ↓
select node
  ↓
persist reservation
  ↓
reserve node target/ports
  ↓
start remote provision
  ↓
commit placement/server ref
  ↓
release/convert reservation
```

Bei Failure:

- Reservation terminal/released;
- keine Ghost remote ref;
- keine automatische zweite Installation auf anderer Node, wenn erstes Ergebnis unbekannt ist;
- vorhandene `files_may_remain` Semantik der Node respektieren.

## Failure Semantics

Node offline nach Serverstart:

- remote server = unreachable/stale;
- keine automatische Recreate-Aktion auf anderer Node;
- kein Daten-Failover ohne späteren Storage/Replication Milestone.

Dokumentiere ausdrücklich: Cluster Scheduler != HA.

## UI

Nodes:

- capacity;
- reserved vs allocatable CPU/RAM;
- labels;
- active/cordoned/maintenance;
- running server count;
- last seen.

Create Server:

```text
Placement
● Automatic
○ Manual

CPU reservation
Memory reservation
Constraints
```

Placement Result anzeigen:

```text
Placed on linux-02
Reason: sufficient resources + required label storage=nvme
```

Nur kontrollierte, nicht sensitive Summary.

## Audit

Neue zentrale semantische Events, z. B.:

- `cluster.placement`
- `cluster.reservation_failed`
- `node.cordon`
- `node.maintenance`
- `node.labels_update`

Kein Audit für jeden internen Score-Schritt.

## Tests

Mindestens:

- capacity math;
- allocatable headroom;
- reservation create/commit/release;
- controller restart with active reservation;
- two parallel placement requests;
- deterministic tie-break;
- required labels;
- preferred labels;
- runtime/os/arch constraints;
- insufficient CPU;
- insufficient memory;
- cordoned node;
- maintenance node;
- node goes offline during provision;
- unknown remote outcome does not duplicate;
- no automatic failover;
- RBAC/CSRF/Audit;
- manual placement regression.

Wenn Port Pools implementiert:

- concurrent port reservations;
- rollback;
- local node collision failure;
- pool exhaustion.

## Acceptance

Wenn Infrastruktur vorhanden, frisch testen:

- Controller;
- mindestens zwei Linux Nodes;
- unterschiedliche CPU/RAM capacities;
- automatic placement mehrerer kleiner Container Server;
- Reservations sichtbar;
- cordon einer Node;
- neues Placement auf anderer Node;
- Node offline;
- kein Doppelserver/failover;
- Node wieder online und Originalserver bleibt authoritative.

## Dokumentation

Synchronisiere:

- `PROJECT_PLAN.md` v0.6;
- `AGENTS.md` scheduler/reservation invariants;
- `README.md`;
- `docs/architecture.md`;
- `docs/security.md`;
- Node/cluster docs;
- `docs/api.md`;
- `docs/development.md`;
- ADR für resource reservations/placement.

## Harte Stop-Grenze

Nicht implementieren:

- Live Migration;
- automatic failover;
- replicated server files;
- shared storage;
- multi-controller consensus;
- controller HA;
- Kubernetes reconciliation;
- autoscaling nodes;
- cloud VM provisioning;
- automatic game server updates.

Nach v0.6 STOP.

## Abschlussbericht

Berichte evidence-basiert:

- Scope/Status;
- Scheduler Algorithmus;
- Reservation Modell;
- Failure Semantics;
- Security Review;
- Dateien/Migrationen;
- Tests/Race/Frontend;
- Builds;
- multi-node acceptance falls wirklich ausgeführt;
- bekannte Limits/Skips;
- explizite Aussage, dass kein HA/Live Migration implementiert wurde.

