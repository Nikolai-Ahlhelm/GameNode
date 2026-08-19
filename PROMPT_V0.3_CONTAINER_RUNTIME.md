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
