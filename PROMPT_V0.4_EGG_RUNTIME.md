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
