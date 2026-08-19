# GameNode Roadmap: Container Runtime, Egg Runtime, Remote Nodes und Cluster Scheduling

> **Status:** Umsetzungsplan / noch nicht implementiert  
> **Zielgruppe:** Codex und menschliche Maintainer  
> **Ausgangspunkt:** aktueller GameNode-v0.2-Stand mit Native Runtime, Official Game Library, konservativem Egg-Import, SteamCMD-Provisioning, RBAC, Audit, Monitoring, Ports und Filesystem-Sandbox.

---

## 1. Ziel dieser Roadmap

Diese Roadmap erweitert GameNode in vier bewusst getrennten Milestones:

1. **v0.3 – Container Runtime**  
   Eine zweite, Linux-first Runtime neben der bestehenden Native Runtime. Sie liefert Container-Lifecycle, Ressourcenlimits, Port-Publishing, persistente Serverdaten, Console, Monitoring und sichere Image-Verwendung.

2. **v0.4 – Egg Runtime**  
   Pelican/Pterodactyl Eggs werden nicht mehr nur konservativ in native GameNode-Templates normalisiert, sondern können – innerhalb einer kontrollierten Container-Sandbox – als echte Container-Provisionierungs- und Runtime-Pläne verwendet werden.

3. **v0.5 – Remote Nodes**  
   Eine GameNode-Instanz kann andere autonome GameNode-Instanzen sicher verwalten. Ein Windows-Controller kann dadurch beispielsweise Linux-Nodes mit Container-Servern steuern. Es gibt noch kein automatisches Placement.

4. **v0.6 – Cluster Scheduler**  
   Erst nachdem Runtime- und Node-Semantik stabil sind, kommen Capacity Accounting, Resource Reservations, Node Labels, Maintenance/Drain und automatisches Placement hinzu.

Die Reihenfolge ist absichtlich:

```text
Container Runtime
      ↓
Egg Runtime
      ↓
Remote Nodes
      ↓
Cluster Scheduler
```

Ein Cluster wird **nicht** vor der Runtime gebaut. Das Remote-Protokoll soll auf einer bereits stabilen lokalen Runtime- und Provisioning-Semantik aufsetzen.

---

## 2. Bestehende GameNode-Prinzipien, die erhalten bleiben

Die neue Roadmap hebt die bisherige Architektur nicht auf. Folgende Invarianten bleiben verbindlich:

### 2.1 Eine GameNode-Instanz bleibt autonom

Jede Node besitzt weiterhin:

- ihre eigene lokale Web UI;
- ihre eigene API;
- ihre eigene SQLite-Datenbank;
- ihre eigenen Serverdefinitionen;
- ihre eigene Runtime;
- ihre eigenen Serverdateien;
- ihre eigenen Runtime- und Audit-Zustände.

Ein Controller ist optional. Eine Node darf nicht unbrauchbar werden, wenn der Controller nicht erreichbar ist.

### 2.2 Kein gemeinsames SQLite und keine Shared-DB-Architektur

Unzulässig:

```text
Controller ───── shared gamenode.db ───── Node
```

Stattdessen:

```text
Controller DB
  ├── Nodes
  ├── Remote references
  ├── Cluster policy
  └── Scheduler state

Node DB
  ├── tatsächliche Server
  ├── Runtime state
  ├── Ports
  ├── Filesystem metadata
  ├── Provisioning jobs
  └── Audit
```

Die Node bleibt Source of Truth für ihre lokalen Ressourcen.

### 2.3 `servers.Service` bleibt Lifecycle-Autorität

Weder HTTP-Handler, Egg-Code, Provisioning-Code noch ein zukünftiger Controller dürfen direkt Docker/Container- oder native Prozesse starten.

Die gewünschte Richtung ist:

```text
API / Node protocol
      ↓
servers.Service
      ↓
Runtime abstraction
      ↓
Native Runtime | Container Runtime
```

### 2.4 Native Runtime bleibt bestehen

Container ersetzen die Native Runtime nicht.

GameNode soll weiterhin unterstützen:

- Adopt Existing;
- Custom Applications;
- Official native/SteamCMD Templates;
- NeoForge direct Java;
- bestehende native Server.

Container sind eine **zweite Runtime**, kein Rewrite des Produkts.

### 2.5 Kein Host-Shell-Fallback

Das bestehende native Sicherheitsmodell bleibt erhalten:

- Native Server: `Executable + Arguments[]`, kein implizites `cmd.exe /c`, `sh -c` oder beliebiger Shell-String.
- Egg-Shell-Semantik darf später ausschließlich in einer dafür vorgesehenen **Container-Sandbox** ausgeführt werden.
- Ein Egg darf niemals Shell-Code auf dem GameNode-Host ausführen.

### 2.6 Backend bleibt Sicherheitsgrenze

Frontend-Capabilities sind nur UX. RBAC, CSRF, Node-Trust, Filesystem-Sandbox, Runtime-Sicherheit und Secret-Redaction werden serverseitig durchgesetzt.

### 2.7 Keine stillen Migrationen bestehender Server

Bestehende Server behalten ihre Runtime, aufgelösten Launch-Daten, Ports, Adapter und Template-Provenance. Neue Runtime- oder Egg-Versionen verändern vorhandene Server nicht automatisch.

---

# 3. Neue Kernbegriffe

## 3.1 Runtime Type

GameNode erhält mindestens:

```text
native
container
```

Spätere Provider wie Podman dürfen erst hinzukommen, wenn ein konkreter Bedarf besteht.

## 3.2 Container Runtime

Die Container Runtime ist eine GameNode-interne Runtime-Implementierung, die einen Container-Engine-Backend verwendet. Für v0.3 sollte Linux zuerst unterstützt werden.

**Empfehlung für v0.3:** Docker Engine als erster realer Backend-Provider. Die GameNode-Schnittstelle soll klein genug bleiben, dass Podman später ergänzt werden kann, aber es soll keine spekulative generische Plugin-Schicht gebaut werden.

## 3.3 Egg Runtime Plan

Ein Egg Runtime Plan ist eine normalisierte, validierte und persistierbare GameNode-Darstellung der Container-relevanten Egg-Semantik:

- erlaubtes Image;
- Installer-Image / Installer-Entry-Point;
- Installationsskript;
- Startup-Template;
- deklarierte Variablen;
- Ports;
- persistentes Server-Root;
- zulässige Konfigurationsoperationen;
- Kompatibilitätsbefunde.

Das rohe Egg bleibt Importquelle, nicht Runtime-Source-of-Truth.

## 3.4 Remote Node

Eine Remote Node ist eine vollständig autonome GameNode-Instanz, die explizit mit einem Controller gepaart wurde.

## 3.5 Controller

Ein Controller ist eine GameNode-Instanz mit aktivierter Multi-Node-Verwaltung. Er besitzt keine direkte Prozess- oder Filesystem-Autorität über eine andere Maschine; er ruft ausschließlich das dedizierte Node-Protokoll der Remote Node auf.

## 3.6 Cluster Scheduler

Der Scheduler entscheidet auf Basis von deklarierter Kapazität, Reservierungen, Node-Zustand und Constraints über die Ziel-Node eines neuen Servers. Er ist **kein** HA-/Failover-/Live-Migration-System.

---

# 4. Gesamtarchitektur nach v0.6

```text
                               ┌───────────────────────────┐
                               │ GameNode Controller       │
                               │ UI / API / RBAC / SQLite  │
                               │ Scheduler                 │
                               └─────────────┬─────────────┘
                                             │
                                  mTLS Node Protocol
                                             │
                    ┌────────────────────────┴────────────────────────┐
                    │                                                 │
          ┌─────────▼─────────┐                             ┌─────────▼─────────┐
          │ GameNode Linux A  │                             │ GameNode Linux B  │
          │ Local UI / SQLite │                             │ Local UI / SQLite │
          │ servers.Service   │                             │ servers.Service   │
          └─────────┬─────────┘                             └─────────┬─────────┘
                    │                                                 │
          ┌─────────┴──────────┐                            ┌─────────┴──────────┐
          │ Native Runtime     │                            │ Native Runtime     │
          │ Container Runtime  │                            │ Container Runtime  │
          └────────────────────┘                            └────────────────────┘
```

Ein Windows-Controller kann dabei Linux-Nodes verwalten. Der Controller selbst muss keine Linux-Container lokal ausführen können.

---

# 5. Querschnittliche Daten- und Sicherheitsentscheidungen

## 5.1 Runtime-spezifische Daten nicht in einen riesigen `servers`-Datensatz quetschen

Die normale Serverzeile soll gemeinsame Eigenschaften behalten. Runtime-spezifische Details gehören in eigene persistente Strukturen, beispielsweise:

```text
servers
server_container_configs
server_container_mounts
server_ports
server_runtime_state
```

Bestehende native Felder dürfen nur refaktoriert werden, wenn Migration und Rückwärtskompatibilität klar sind.

## 5.2 Runtime-Instanzidentität generalisieren

Native Runtime verwendet heute PID + Start Identity. Container brauchen eine andere Identität.

Zielmodell konzeptionell:

```text
RuntimeInstanceIdentity
├── runtime_type
├── generation
└── runtime-specific identity
    ├── native: pid + start_key
    └── container: engine container id + start generation
```

Wichtig:

- stale finalizer darf nie eine neuere Instanz überschreiben;
- stale console/session darf nicht gegen neue Instanz schreiben;
- restart/kill/status müssen die konkrete Runtime-Instanz verifizieren;
- Container-Namen allein sind keine sichere Instanzidentität.

Wenn diese Refaktorierung die bestehende ADR 0003 grundlegend verändert, muss eine neue ADR erstellt oder ADR 0003 explizit ergänzt werden.

## 5.3 Persistentes Server-Root bleibt hostseitig

Container-Gameserver erhalten weiterhin ein GameNode-eigenes Server-Root auf dem Node-Host.

Beispiel:

```text
<data>/servers/<directory>
```

Dieses Root wird in den Runtime-Container gemountet, beispielsweise semantisch als:

```text
/home/container
```

Die Files API arbeitet weiterhin auf dem hostseitigen GameNode-Root über `internal/filesystem`. Sie browsed nicht beliebig durch das Container-Root-Filesystem.

## 5.4 Ports werden Host-Ressourcen

Container-Ports müssen eindeutig zwischen zwei Ebenen unterscheiden:

```text
host bind address / host port / protocol
              ↓
container port / protocol
```

Die vorhandene GameNode-Portregistrierung bleibt die Source of Truth für GameNode-eigene Host-Portbelegung und Preflight.

Empfohlene Evolution:

- native Server: Host-Port entspricht weiterhin normalem Server-Port;
- container Server: Portrecord erhält optionalen Ziel-/Container-Port;
- Docker-Portpublishing wird ausschließlich aus validierten registrierten Portrecords erzeugt;
- kein Egg darf direkt beliebige Host-Portbindings an der Port Registry vorbei erzeugen.

## 5.5 Resource Policy

GameNode erhält ein persistentes, typisiertes Ressourcenmodell:

```text
memory_limit_bytes
cpu_limit_millis     # 1000 = Kapazität eines vollen CPU-Kerns
cpu_affinity         # optional, später/native-spezifisch
pids_limit           # optional
```

Für v0.3 müssen mindestens Memory und CPU für Container erzwungen werden.

Der Scheduler in v0.6 arbeitet mit **konfigurierten Reservierungen/Limits**, nicht mit momentaner CPU-Auslastung als alleiniger Kapazitätsquelle.

## 5.6 Kein `--privileged`

Container aus GameNode/Eggs dürfen standardmäßig nicht:

- privileged laufen;
- den Docker Socket mounten;
- beliebige Hostpfade mounten;
- Host PID namespace verwenden;
- Host IPC namespace verwenden;
- Host network verwenden;
- Devices frei durchreichen;
- beliebige zusätzliche Linux capabilities anfordern.

Wenn später Ausnahmen benötigt werden, müssen sie als eigener expliziter Security-Milestone behandelt werden.

## 5.7 Image Trust

Ein importiertes Egg darf ein Image vorschlagen, aber nicht still jede beliebige Registry verwenden.

Empfohlene v0.4-Regel:

- Node/Controller besitzt eine Admin-konfigurierte Registry-Allowlist;
- keine Registry-Credentials im ersten Egg-Runtime-Milestone;
- Image-Referenz wird streng validiert;
- Pulls erfolgen nur nach expliziter Provisionierungsentscheidung;
- das tatsächlich verwendete Image/Tag und – wenn Engine verfügbar – der aufgelöste Digest werden mit dem Server-Snapshot persistiert;
- ein später geändertes Egg migriert existierende Server nicht.

## 5.8 Secrets

Bestehende Secret-Regeln bleiben:

- keine Secrets in Audit;
- keine Secrets in Logs;
- keine Secrets in Support Bundle;
- keine Secrets in Provisioning Events;
- API liefert geheime Werte nicht zurück;
- Egg-sensitive defaults werden nicht unkontrolliert persistiert.

Container Environment darf Secrets enthalten, muss aber dieselben Redaction-Grenzen wie die bestehende native Environment-Persistenz besitzen.

## 5.9 Audit

Neue semantische Actions sollten zentral katalogisiert werden, beispielsweise:

```text
container.image_pull
node.pair
node.unpair
node.enable
node.disable
server.remote_create
server.remote_start
cluster.placement
cluster.node_drain
```

Nicht jeder interne Docker API Call wird ein Audit Event. Es bleibt bei einem kontrollierten semantischen Event pro Benutzeroperation.

---

# 6. Milestone v0.3 – Container Runtime

## 6.1 Ziel

GameNode kann auf einem unterstützten Linux-Node einen Game-Server als Container verwalten, ohne die Native Runtime zu beeinträchtigen.

Die Funktion soll bereits unabhängig von Eggs nutzbar sein, damit die Runtime selbst vollständig getestet werden kann.

## 6.2 Definition of Done

Ein Administrator kann einen Container-Server mit folgenden Eigenschaften anlegen:

- Image;
- persistentes Server-Root;
- strukturierte Environment-Werte;
- strukturierter Startprozess;
- Memory Limit;
- CPU Limit;
- deklarierte Host→Container Ports;
- Stop Timeout;
- Auto Restart über die bestehende `servers.Service`-Semantik.

Danach funktionieren:

- Start;
- Stop;
- Kill;
- Restart;
- Status;
- Console output;
- Console input, wenn der Containerprozess stdin unterstützt;
- Monitoring für CPU/RAM;
- Files API auf dem persistenten Server-Root;
- Port collision/preflight;
- Audit;
- RBAC;
- GameNode-Neustart mit sicherer Wiedererkennung eines bereits laufenden Containers.

Native Server funktionieren unverändert weiter.

## 6.3 Scope

### Backend

- neue Container Runtime unter `internal/runtime` oder einem klar abgegrenzten Unterpaket;
- Docker Engine Backend, Linux-first;
- Runtime dispatch nach `runtime_type`;
- persistente Container-Konfiguration;
- Runtime-instance identity für Container;
- Monitoring provider für Container;
- Console attach/stream;
- stop/kill/restart über bestehende Server-Orchestrierung;
- Resource Limits;
- Port publishing;
- Image pull als kontrollierte Operation;
- optionaler Engine availability/provisionability check.

### API

- Runtime-Fähigkeiten in Server-/Creation-APIs;
- Container-spezifische Create/Update-Felder nur bei `runtime_type=container`;
- typisierte Validierung;
- keine freien Docker-CLI-Argumente;
- kein beliebiges Host-Mount-Array.

### Frontend

- Runtime-Auswahl;
- Container-Formular;
- Resource Limits;
- Port Mapping;
- Engine-unavailable state;
- ehrliche Start-/Pull-/Failure-Zustände.

## 6.4 Explizite Non-Goals

Noch nicht implementieren:

- Egg Installationsskripte;
- Pterodactyl/Pelican Startup-Shell-Kompatibilität;
- Remote Nodes;
- Cluster;
- automatisches Scheduling;
- Live Migration;
- Registry Credentials;
- Kubernetes;
- Docker Compose;
- user-provided Docker CLI flags;
- privileged containers;
- arbitrary host mounts;
- automatic server updates;
- Docker Swarm.

## 6.5 Engine-Integration

Codex soll die Engine nicht über `docker ...` Shell-Kommandos steuern.

Bevorzugt:

- Docker Engine API über eine kleine interne Schnittstelle;
- kein Shelling-Out;
- Engine client darf keine HTTP-/API-Typen in `servers.Service` leaken.

Beispielhafte interne Grenze:

```go
type ContainerEngine interface {
    Available(ctx context.Context) error
    PullImage(ctx context.Context, image string, progress ProgressSink) (ImageInfo, error)
    Create(ctx context.Context, spec ContainerSpec) (ContainerIdentity, error)
    Start(ctx context.Context, id ContainerIdentity) error
    Stop(ctx context.Context, id ContainerIdentity, timeout time.Duration) error
    Kill(ctx context.Context, id ContainerIdentity) error
    Inspect(ctx context.Context, id ContainerIdentity) (ContainerState, error)
    Stats(ctx context.Context, id ContainerIdentity) (ContainerStats, error)
    Attach(ctx context.Context, id ContainerIdentity) (ContainerAttach, error)
    Remove(ctx context.Context, id ContainerIdentity) error
}
```

Die tatsächlichen Namen dürfen der bestehenden Codebasis angepasst werden; keine Interface-Abstraktion nur um der Abstraktion willen.

## 6.6 Lifecycle

Container Lifecycle muss semantisch mit der bestehenden Server-Lifecycle-Logik übereinstimmen:

- `servers.Service` prüft Berechtigung/State und koordiniert;
- Port Preflight läuft vor dem Start;
- Console Session gehört zur konkreten Runtime Instance;
- unerwarteter Exit kann Auto Restart auslösen;
- user Stop/Restart ist kein Crash;
- Kill ist unmittelbar;
- stale finalizer kann keinen neuen Container überschreiben.

## 6.7 GameNode-Neustart

Für Container darf – im Gegensatz zur nativen Pipe-Recovery – ein echter Engine-Reattach möglich sein.

Anforderungen:

1. Persistiere sichere Containeridentität.
2. Verifiziere bei Startup, dass Container-ID, GameNode ownership labels und erwartete Server-ID zusammenpassen.
3. Übernimm niemals fremde Container nur wegen eines Namensmatches.
4. Wenn Engine-Attach zuverlässig funktioniert, darf Console für Container wieder attached werden.
5. Native rediscovered Console bleibt weiterhin detached.
6. Wenn Reattach nicht zweifelsfrei möglich ist, zeige einen expliziten detached/degraded Zustand statt ihn zu erfinden.

## 6.8 Ownership Labels

Von GameNode erzeugte Container sollen kontrollierte Labels erhalten, z. B.:

```text
io.gamenode.managed=true
io.gamenode.server_id=<uuid>
io.gamenode.instance_generation=<uuid>
```

Keine sensitiven Werte in Labels.

## 6.9 Persistent Mount

Genau ein GameNode-kontrolliertes Server-Root ist Pflicht.

Beispiel:

```text
<data>/servers/<directory>  →  /home/container
```

Zusätzliche hostseitige Mounts sind in v0.3 nicht erlaubt.

## 6.10 Netzwerk

Standard:

- Bridge-Netzwerk;
- kein Host Network;
- Port-Publishing ausschließlich über registrierte GameNode-Ports;
- Host bind address bleibt kontrolliertes GameNode-Portfeld;
- Container-Port wird explizit gespeichert.

## 6.11 Ressourcenlimits

Mindestens:

- Memory limit;
- CPU quota / NanoCPUs / äquivalente Engine-Semantik.

API/UI sollen beispielsweise anzeigen:

```text
CPU limit: 2000 millicores (2.0 CPUs)
Memory limit: 8192 MiB
```

Monitoring kann dazu `used / limit` anzeigen.

## 6.12 Datenbank

Codex muss vor Änderungen die aktuellen Migrationen prüfen und die nächste freie Nummer verwenden.

Erwartete neue Strukturen können sein:

- Container config;
- Resource policy;
- Container-specific runtime state;
- optional `container_port` an Port Records.

Keine bereits angewandte Migration ändern.

## 6.13 Tests

### Unit

- container spec validation;
- image ref validation;
- resource bound validation;
- port mapping validation;
- ownership label verification;
- stale instance protection;
- engine unavailable handling;
- unsafe mount rejection;
- privileged/host-network rejection.

### Service tests

Fake Engine verwenden für:

- create/start/stop/kill/restart;
- unexpected exit + auto restart;
- manual stop not crash;
- port conflict before start;
- GameNode restart rediscovery;
- stale finalizer race;
- console attach identity;
- resource stats.

### Integration

Opt-in Docker-Test auf Linux, niemals Pflicht für normale Unit CI, solange GitHub Runner/Umgebung nicht ausdrücklich dafür eingerichtet ist.

Mögliche Variable:

```text
GAMENODE_CONTAINER_INTEGRATION=1
```

### Regression

Volle bestehende native Runtime-, API-, RBAC-, Filesystem-, Template- und Provisioning-Suite muss weiter bestehen.

## 6.14 Dokumentation

Aktualisieren:

- `PROJECT_PLAN.md` – v0.3 explizit definieren und Container aus dem generellen Non-Goal für diesen Milestone herausnehmen;
- `AGENTS.md` – neue verbindliche Container-Invarianten;
- `README.md`;
- `docs/architecture.md`;
- `docs/security.md`;
- `docs/runtime.md`;
- `docs/api.md`;
- `docs/development.md`;
- ggf. neue ADR `Container runtime and ownership`.

## 6.15 Acceptance

Fresh Linux acceptance:

1. Docker Engine verfügbar.
2. GameNode startet.
3. Container-Server mit kleinem Test-Image erstellen.
4. Persistentes Server-Root verifizieren.
5. CPU/RAM Limits inspizieren.
6. Port-Publishing verifizieren.
7. stdout/stderr sehen.
8. stdin senden.
9. Stop/Restart/Kill.
10. GameNode während laufendem Container neu starten.
11. sichere Rediscovery verifizieren.
12. Native Testserver weiterhin starten/stoppen.

Kein Acceptance-Marker behaupten, wenn der Flow nicht frisch ausgeführt wurde.

---

# 7. Milestone v0.4 – Egg Runtime

## 7.1 Ziel

GameNode kann Pelican/Pterodactyl v2 Eggs in einer kontrollierten Container-Runtime wesentlich originalgetreuer ausführen.

Der bestehende konservative Importer wird **nicht entfernt**. Stattdessen erhält ein Egg zwei mögliche Compatibility Paths:

```text
Native compatibility
Container compatibility
```

Beispiel:

```text
Native:    partially compatible
Container: compatible
```

## 7.2 Definition of Done

Ein unterstütztes Egg kann:

- analysiert/importiert werden;
- ein zulässiges Container Image wählen;
- seinen Installationsschritt in einem isolierten Installer-Container ausführen;
- Dateien ausschließlich im GameNode-Server-Root erzeugen;
- deklarierte Variablen erhalten;
- einen container-internen Startup-Prozess ausführen;
- Ports über die GameNode-Portregistry publishen;
- Resource Limits verwenden;
- normal als GameNode-Server registriert werden;
- über bestehende Files/Console/Monitoring/Lifecycle APIs verwaltet werden.

Kein Egg-Code wird auf dem Host ausgeführt.

## 7.3 Architektur

```text
Egg JSON
   ↓
bounded structural parser
   ↓
compatibility analysis
   ├── Native Template Plan
   └── Container Egg Runtime Plan
                    ↓
            provisioning.Service
                    ↓
          Installer Container
                    ↓
            persistent server root
                    ↓
             normal Server row
                    ↓
            Container Runtime
```

## 7.4 Rohes Egg bleibt untrusted input

Beibehalten:

- Größenlimits;
- Nestinglimits;
- Variablenlimits;
- kontrollierte Fehlercodes;
- Secret detection/redaction;
- keine Rohdaten in Audit;
- keine Installation auf Analyze;
- keine Runtime-Ausführung beim Import.

## 7.5 Image-Semantik

Egg `docker_images` wird nun als potenzieller Runtime-Input verstanden, aber kontrolliert.

Anforderungen:

- strikte Image-Reference-Validierung;
- nur freigegebene Registries;
- keine Registry-Credentials in v0.4;
- Admin kann bei Provisionierung eines von den Egg-Images wählen;
- kein Image darf Host-Pfade oder Engine-Flags konfigurieren;
- tatsächliches Image und Digest werden im Server-Snapshot gespeichert.

## 7.6 Installer Container

Egg-Installationsskripte dürfen nur in einem kurzlebigen Installer-Container laufen.

Pflichtgrenzen:

- unprivileged;
- kein Docker Socket;
- kein Host network;
- kein beliebiger Host mount;
- nur Server-Root als beschreibbarer persistenter Mount;
- eigenes Temp-Verzeichnis;
- bounded stdout/stderr event forwarding;
- timeout;
- cancellation;
- CPU/RAM/PID limits;
- keine Secrets in Logs/Audit/Job History;
- Installer-Container wird nach Abschluss entfernt;
- Server-Root wird bei Fehler nicht automatisch rekursiv gelöscht, wenn Ownership nicht zweifelsfrei ist.

## 7.7 Container-interne Shell ist explizit erlaubt

Dies ist eine wichtige neue Security-Grenze:

- Host Shell bleibt verboten.
- Ein Egg darf innerhalb seines kontrollierten Container-Namespaces eine Shell/Entrypoint-Semantik verwenden.
- Container-Shell-Ausführung muss als eigenes bewusstes Runtime-Konzept implementiert sein.
- Niemals Egg-Strings in einen Host-`exec.Command("sh", "-c", ...)` geben.

Diese Änderung muss in `docs/security.md` und einer ADR dokumentiert werden.

## 7.8 Startup

Startup-Templates werden container-intern expandiert.

Anforderungen:

- nur bekannte Egg/GameNode Variablen;
- keine Host Environment Expansion;
- bounded replacement;
- Secrets nur dort, wo Runtime sie benötigt;
- kein Argument darf Host Engine Flags erzeugen;
- keine Docker CLI Konstruktion aus Startup Strings.

## 7.9 Egg Configuration Semantics

Nicht versuchen, die komplette Pterodactyl-Config-Sprache in einem Schritt nachzubauen.

Implementiere nur konkret benötigte, testbare Formen. Jeder Parser/Replacer ist compiled GameNode code.

Priorisierung:

1. einfache properties/INI-style replacements;
2. JSON key updates;
3. weitere klar definierte Formate nur mit Fixtures und Limits.

Keine generischen regex-/eval-/script-basierten Host-Rewriter.

Unverstandene Konfiguration erzeugt einen Compatibility Finding und darf nicht still ignoriert werden, wenn sie für einen funktionierenden Server wesentlich ist.

## 7.10 Provisioning Jobs

Bestehendes Job-Modell erweitern statt zweites Jobsystem bauen.

Mögliche Phasen:

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

Alle Events bleiben bounded und secret-safe.

## 7.11 Snapshot-Persistenz

Server speichert eine vollständige normalisierte Laufzeit-Snapshot-Repräsentation:

- Egg ID/source/hash/version metadata;
- selected image;
- resolved digest, wenn verfügbar;
- startup template/normalized runtime plan;
- variable sensitivity metadata;
- ports;
- resource defaults;
- config adapter/runtime config snapshot.

Spätere Egg-Änderungen verändern vorhandene Server nicht.

## 7.12 Compatibility Model

Compatibility wird mindestens zweidimensional:

```text
native_status
container_status
```

oder eine äquivalente strukturierte Darstellung.

UI muss deutlich machen:

- Native supported;
- Container supported;
- only container supported;
- unsupported;
- required image blocked by policy;
- installer unsupported;
- config semantics unsupported.

## 7.13 Non-Goals

Noch nicht:

- Remote Nodes;
- automatische Node-Auswahl;
- Registry Auth;
- privileged Eggs;
- Docker Compose;
- Mods/Plugins als eigener Manager;
- automatic updates;
- arbitrary host mounts;
- custom kernel/device access;
- garantiert 100% jedes existierenden Community Eggs.

Das Ziel ist eine belastbare, wachsende Compatibility Engine – keine falsche 100%-Behauptung.

## 7.14 Tests

### Parser/Analyzer

- valid container Egg;
- invalid image refs;
- unsupported registry;
- shell script preserved only for container plan;
- secrets;
- malformed configs;
- excessive script size;
- unknown required features.

### Provisioning

Mit Fake Engine:

- image pull;
- installer creation;
- script exit 0/non-zero;
- timeout;
- cancellation;
- file remains semantics;
- final server registration;
- failure after install but before DB commit;
- secret redaction;
- port collisions;
- resource limits.

### Real acceptance

Mindestens zwei kleine real-world Eggs auf Linux, davon:

- eines mit Installationsskript;
- eines mit mehreren Variablen/Ports.

Keine multi-GB Real-Game-Downloads in normaler CI.

## 7.15 Dokumentation

Aktualisieren:

- `PROJECT_PLAN.md` – v0.4;
- `AGENTS.md` – Egg Container Security Invariants;
- `docs/templates.md`;
- `docs/security.md`;
- `docs/architecture.md`;
- `docs/api.md`;
- `docs/development.md`;
- Templates/Egg contribution/import docs;
- neue ADR für container-internal shell / untrusted installer execution.

---

# 8. Milestone v0.5 – Remote Nodes

## 8.1 Ziel

Eine GameNode-Instanz kann mehrere andere autonome GameNode-Instanzen sicher verwalten.

Primärer Use Case:

```text
Windows Desktop / Controller
          ↓
       GameNode
          ↓ mTLS
Linux Server / GameNode Node
          ↓
Container Gameserver
```

Es gibt noch keine automatische Platzierung.

## 8.2 Grundsatz

Remote Node ist **kein dummer Agent**.

Jede Node bleibt lokal nutzbar und kann weiterhin unabhängig verwaltet werden.

## 8.3 Rollen

Eine Instanz kann konzeptionell folgende Capabilities besitzen:

```text
standalone
controller-capable
remote-management-enabled
```

Nicht zwingend getrennte Binaries.

## 8.4 Node Pairing

Empfohlenes Modell:

1. Admin aktiviert Remote Management auf der Node.
2. Node erzeugt kurzlebigen Pairing-Code/Token.
3. Controller verbindet sich zur explizit angegebenen Node-Adresse.
4. Beide Seiten tauschen Identitäten/Zertifikate aus.
5. Danach mTLS mit gespeicherten Node-/Controller-Identitäten.
6. Pairing-Secret verfällt und wird nicht erneut verwendet.

Anforderungen:

- kein Default Shared Secret;
- kein dauerhaftes Bearer Token im Browser;
- Zertifikate/Private Keys nicht in Audit oder Support Bundle;
- Unpair/Rotate möglich;
- Node kann Controller-Zugriff lokal widerrufen.

## 8.5 Node Protocol

Nicht einfach Browser-API 1:1 öffnen.

Dediziertes versioniertes Service-to-Service-Protokoll, z. B.:

```text
/api/node/v1/...
```

oder eine andere klar abgegrenzte Route.

HTTP+JSON sowie WebSocket/streaming darf wiederverwendet werden. gRPC nur wenn ein konkreter Vorteil die zusätzliche Komplexität rechtfertigt.

## 8.6 Node Capabilities

Controller kann lesen:

- Node ID;
- GameNode version;
- OS/architecture;
- Runtime capabilities (`native`, `container`);
- Container engine available;
- CPU capacity;
- memory capacity;
- disk summary;
- node health;
- last seen.

Keine unkontrollierte Host-Inventarisierung.

## 8.7 Remote Server Operations

Mindestens:

- list/get servers;
- create/provision;
- start;
- stop;
- restart;
- kill;
- delete;
- monitoring;
- ports;
- configuration;
- console stream/input;
- files list/read/edit/upload/download.

Die Node ruft für all diese Operationen ihre normalen lokalen Services auf.

## 8.8 Autorisierung

Empfohlenes Trust-Modell:

- Benutzer authentifiziert sich am Controller.
- Controller prüft Controller-RBAC.
- Controller handelt gegenüber der Node als gepaarte Controller-Service-Identity.
- Node prüft, ob dieser Controller für Remote Management autorisiert ist.
- Node erhält zusätzlich einen kontrollierten Actor Snapshot für Audit-Zwecke.
- Der Actor Snapshot ist niemals alleinige Autorisierungsgrundlage auf der Node.

Damit wird verhindert, dass ein manipulierter `actor_username=admin` Header Rechte erzeugt.

## 8.9 Audit Attribution

Node Audit soll unterscheiden:

```text
origin = local | remote_controller
controller_id
actor_user_id (snapshot, wenn vorhanden)
actor_username (snapshot)
```

Keine Passwörter/Sessions/CSRF vom Controller übertragen.

Controller Audit zeichnet ebenfalls die Benutzeroperation auf.

Eine Remote-Operation kann daher bewusst je ein semantisches Event auf Controller und Node besitzen.

## 8.10 Controller-Datenmodell

Controller speichert nicht die gesamte Node-DB nach.

Mindestens:

```text
nodes
node_trust
remote_server_refs
```

Remote Server Ref:

```text
controller-side id/reference
node_id
node_server_id
optional bounded cached display summary
```

Node bleibt Source of Truth.

## 8.11 Offline-Semantik

Controller UI muss ehrlich unterscheiden:

```text
online
unreachable
authentication_failed
version_incompatible
disabled
```

Keinen alten Running-State als live darstellen.

Lesbare gecachte Metadaten dürfen als `stale` markiert werden.

## 8.12 Streaming

Console und große File Transfers müssen gestreamt werden.

Controller darf nicht komplette große Downloads/Uploads oder Console History unbounded puffern.

Backpressure-Grenzen bleiben auf beiden Seiten erhalten.

## 8.13 Versionierung

Node Protocol benötigt explizite Version/Capability Negotiation.

Controller muss einen neueren/älteren Node kontrolliert ablehnen oder Features ausblenden können, statt unbekannte Felder unsicher zu interpretieren.

## 8.14 Non-Goals

Noch nicht:

- automatisches Server Placement;
- shared filesystem;
- live migration;
- automatic failover;
- remote node OS upgrades;
- distributed SQLite;
- consensus/Raft;
- HA Controller;
- central DB requirement;
- transparentes Verschieben laufender Server.

## 8.15 Tests

### Unit/Service

- pairing lifecycle;
- expired code;
- wrong controller identity;
- certificate rotation/revocation;
- protocol version mismatch;
- capability negotiation;
- offline states;
- actor snapshot cannot grant auth;
- audit attribution;
- bounded proxying.

### Integration

Mindestens zwei GameNode-Prozesse mit getrennten SQLite/Data roots in Tests starten.

Prüfen:

- pair;
- list node;
- create lightweight server remote;
- lifecycle;
- monitoring;
- console;
- files;
- disconnect;
- reconnect;
- unpair.

### Platform acceptance

Fresh acceptance:

- Controller auf Windows;
- Node auf Linux;
- Container Server auf Linux Node;
- Start/Console/Files/Stop vom Windows Controller aus.

Nur behaupten, wenn genau dieser Cross-OS-Flow frisch ausgeführt wurde.

## 8.16 Dokumentation

- `PROJECT_PLAN.md` – v0.5;
- `AGENTS.md` – Node trust/protocol invariants;
- `docs/architecture.md`;
- `docs/security.md`;
- `docs/api.md` oder eigenes `docs/node-protocol.md`;
- ADR für Node Identity + Pairing + mTLS;
- Deployment docs für Controller/Node.

---

# 9. Milestone v0.6 – Cluster Scheduler

## 9.1 Ziel

Der Controller kann einen neuen Server automatisch auf einer geeigneten Node platzieren.

Das ist der erste echte Cluster-Milestone.

## 9.2 Definition of Done

Beim Create/Provisioning kann der Benutzer wählen:

```text
Placement
○ Manual node
● Automatic
```

Automatic berücksichtigt mindestens:

- Node online/healthy;
- Runtime capability;
- OS/architecture constraint;
- Container Engine availability;
- Memory capacity + reservations;
- CPU capacity + reservations;
- Disk free threshold;
- Port availability/allocation;
- Node labels/constraints;
- maintenance/drain state.

Die Platzierungsentscheidung wird atomar genug reserviert, dass zwei parallele Creates nicht dieselbe Kapazität doppelt vergeben.

## 9.3 Resource Accounting

Scheduler rechnet mit deklarierter Kapazität und Reservierungen.

Beispiel:

```text
Node capacity
CPU:    16000 millicores
Memory: 64 GiB

Reservations
Server A: 4000 millicores / 8 GiB
Server B: 2000 millicores / 4 GiB

Available for placement
CPU:    10000 millicores
Memory: 52 GiB
```

Momentane Monitoring-Auslastung ist Zusatzinformation, aber keine alleinige Reservierungsgrundlage.

## 9.4 Placement Constraints

Mindestens:

- required runtime type;
- OS;
- architecture;
- node labels;
- minimum free disk;
- required image policy/registry capability;
- optional preferred node/label.

Keine beliebige Expression Language. Strukturierte Constraints only.

## 9.5 Node Labels

Admin-definierte bounded key/value labels:

```text
region=home
storage=nvme
purpose=games
internet=public
```

Keine Labels mit impliziten Rechten.

## 9.6 Maintenance / Drain

Node states:

```text
active
cordoned
maintenance
```

`cordoned`:

- keine neuen Placements;
- laufende Server bleiben.

`maintenance`:

- keine neuen Placements;
- UI warnt vor vorhandenen Servern;
- keine automatische Migration in v0.6.

## 9.7 Port Allocation

Optionaler controllerseitiger Port-Pool pro Node:

```text
27000-27999 UDP/TCP
```

Scheduler kann freie Host Ports reservieren.

Aber:

- Node-Portregistry bleibt letztgültige lokale Prüfung;
- Reservation braucht Ablauf/rollback bei fehlgeschlagener Provisionierung;
- TOCTOU mit externen Prozessen bleibt möglich und muss kontrolliert fehlschlagen.

## 9.8 Placement Transaction

Empfohlener Flow:

```text
validate request
  ↓
load eligible nodes
  ↓
score/filter
  ↓
create bounded reservation on controller
  ↓
ask node to reserve target/ports
  ↓
start provisioning
  ↓
commit placement reference
```

Bei Fehler:

- Reservations sauber freigeben;
- keine Ghost Server;
- installierte Dateien nach bestehender Ownership-Semantik behandeln;
- kein stilles Retry auf einer anderen Node, wenn dadurch doppelte Installationen entstehen könnten.

## 9.9 Scheduler Algorithmus

Für v0.6 einfach und deterministisch.

Empfehlung:

1. harte Constraints filtern;
2. nodes mit zu wenig reservierbarer CPU/RAM ausschließen;
3. maintenance/cordoned ausschließen;
4. Kandidaten nach einem dokumentierten Score sortieren;
5. stabiler Tie-Breaker, z. B. Node ID.

Kein Machine Learning, kein komplexer Bin-Packing-Solver.

Ein möglicher Score:

- bevorzugt ausreichend freie Memory Reserve;
- danach CPU Reserve;
- optional Node preference;
- stabiler tie-break.

## 9.10 Failure Semantics

Node offline nach Placement:

- Server wird als remote/unreachable angezeigt;
- nicht automatisch auf anderer Node neu erzeugen;
- kein automatischer Daten-Failover ohne Storage-/Replication-Milestone.

Dies ist besonders wichtig: **Cluster Scheduling ist nicht High Availability.**

## 9.11 Non-Goals

Noch nicht:

- live migration;
- shared storage;
- replicated server files;
- automatic failover;
- rolling move between nodes;
- multi-controller consensus;
- controller HA;
- Kubernetes-style reconciliation;
- autoscaling nodes;
- cloud VM provisioning.

## 9.12 Tests

- capacity calculations;
- parallel reservation races;
- deterministic placement;
- label constraints;
- runtime capability constraints;
- cordon/maintenance;
- port pool allocation/rollback;
- node goes offline during provision;
- controller restart with reservations;
- no double placement after retry;
- no auto-failover.

## 9.13 Acceptance

Mindestens zwei Linux Nodes + Controller:

1. unterschiedliche CPU/RAM capacity konfigurieren;
2. mehrere Container-Server automatisch platzieren;
3. nachweisen, dass Reservations reduziert werden;
4. Node cordon;
5. neues Placement landet auf anderer Node;
6. Node offline → kein automatischer Doppelserver;
7. Node wieder online → Originalserver bleibt Source of Truth.

---

# 10. Empfohlene Migrationsstrategie für bestehende v0.2-Server

## 10.1 Keine automatische Konvertierung

Alle vorhandenen Server bleiben `native`.

Keine automatische Containerisierung von:

- Official SteamCMD Servern;
- importierten Egg-Servern;
- NeoForge;
- Adopt Existing;
- Custom Applications.

## 10.2 Neue Server können Runtime wählen

Je nach Quelle:

```text
Custom Application
  ├── native
  └── container (wenn Engine verfügbar)

Official Template
  ├── bestehender native path
  └── nur wenn Template später explizit Container unterstützt

Imported Egg
  ├── native compatibility path
  └── container egg path
```

## 10.3 Spätere Migration ist separater Milestone

Ein „Convert existing native server to container“ Workflow wird nicht in v0.3/v0.4 versteckt. Er braucht eigene Regeln für Files, Ports, Environment, Startup und Rollback.

---

# 11. Empfohlene Repository-Struktur

Nur als Richtung; Codex soll aktuelle Packages respektieren und nicht blind umstrukturieren.

```text
internal/
├── runtime/
│   ├── native...
│   ├── container.go
│   └── container_docker...
├── containers/              # nur falls Engine-Details sauberer getrennt sind
├── servers/
├── templates/
├── provisioning/
├── nodes/                   # v0.5
├── cluster/                 # v0.6 scheduler/capacity
└── ...
```

Kein neues Package ohne echte Verantwortungsgrenze.

---

# 12. API/UI-Roadmap

## v0.3

Server Create:

```text
Runtime
○ Native
● Container

Container Image
Resource Limits
Port Mappings
```

## v0.4

Template/Egg Detail:

```text
Compatibility
Native:    Partial
Container: Supported

Create with
○ Native plan
● Container Egg runtime
```

## v0.5

Navigation:

```text
Dashboard
Servers
Nodes
Templates
...
```

Server Create:

```text
Node
● Local
○ linux-01
○ linux-02
```

## v0.6

```text
Placement
● Automatic
○ Select node manually

Resources
CPU reservation
Memory reservation

Constraints
Labels
Runtime
```

---

# 13. Security Review Gate pro Milestone

Vor Abschluss jedes Milestones explizit beantworten:

1. Kann irgendein neuer Pfad Host-Shell-Code aus untrusted Input ausführen?
2. Kann ein Benutzer/Egg beliebige Engine Flags setzen?
3. Kann ein Container den Docker Socket erreichen?
4. Kann ein Container beliebige Hostpfade mounten?
5. Kann ein Container privileged/host-network/host-pid werden?
6. Kann ein Image aus einer nicht erlaubten Registry gezogen werden?
7. Können Secrets in Engine errors, pull output, installer logs, audit, API oder support bundle leaken?
8. Kann eine stale Runtime Instance eine neuere Instanz finalisieren?
9. Kann ein Remote Actor Snapshot Berechtigungen vortäuschen?
10. Kann ein Node-Pairing wiederverwendet/replayed werden?
11. Kann ein Controller eine fremde/unpaired Node ansprechen?
12. Kann ein Scheduler Kapazität doppelt reservieren?
13. Kann Offline-/Retry-Verhalten doppelte Server erzeugen?
14. Bleibt der server-root sandbox boundary erhalten?
15. Bleibt jede Node autonom und lokal bedienbar?

---

# 14. CI-Strategie

## Normale CI

Muss ohne echte Docker Engine und ohne Multi-Host-Umgebung zuverlässig laufen können.

Daher:

- Fake Container Engine für Unit/Service/API Tests;
- Docker Engine Integration opt-in;
- Remote Nodes Integration mit mehreren lokalen GameNode-Prozessen;
- Scheduler Tests deterministisch/in-memory bzw. SQLite-test-backed;
- Windows Build muss Container-unsupported sauber kompilieren;
- Linux Build enthält Container Backend.

## Optionaler Integration Job später

Wenn stabil:

- Linux GitHub Runner mit Docker;
- kleiner Testcontainer;
- keine externen Game-Downloads;
- keine Registry-Credentials.

## Cross-OS Acceptance

Für v0.5 muss ein echter Windows→Linux Flow manuell oder in geeigneter Infrastruktur dokumentiert werden.

---

# 15. Release-/Versionierungsdisziplin

- v0.3 startet erst, nachdem `PROJECT_PLAN.md` den Milestone ausdrücklich autorisiert.
- Jeder Milestone endet vollständig, bevor der nächste implementiert wird.
- Keine „kleinen Vorbereitungen“ für Scheduler in v0.3, außer wirklich benötigte stabile Runtime-Schnittstellen.
- Keine Remote APIs in v0.4.
- Keine automatische Platzierung in v0.5.
- Keine Live Migration in v0.6.

Codex muss nach jedem Milestone stoppen und einen Evidence-basierten Abschlussbericht liefern.

---

# 16. Empfohlene ADRs

Je nach tatsächlichem Code-Impact:

1. **Container runtime ownership and lifecycle**
2. **Container image trust and untrusted Egg execution boundary**
3. **Remote node identity, pairing and mTLS**
4. **Controller/node source-of-truth model**
5. **Cluster resource reservations and placement semantics**

Nicht jede kleine Implementierungsentscheidung braucht eine ADR; nur dauerhafte Architekturentscheidungen.

---

# 17. Erfolgskriterium der gesamten Roadmap

Nach v0.6 soll dieser Flow möglich sein:

```text
Windows GameNode Controller
        ↓
Import Pelican/Pterodactyl Egg
        ↓
Select Container Runtime
        ↓
CPU: 2 CPUs
RAM: 8 GiB
Ports: automatic/manual
        ↓
Automatic Placement
        ↓
Linux GameNode Node
        ↓
Installer Container
        ↓
Persistent Server Files
        ↓
Runtime Container
        ↓
Console / Files / Monitoring / Lifecycle
        ↓
managed from Windows Controller
```

Und gleichzeitig weiterhin:

```text
Standalone GameNode
        ↓
Native Adopt Existing / Custom Application / Official Template
```

funktionieren.

Das ist die zentrale Produktidee: **Container und Cluster erweitern GameNode, sie ersetzen nicht die autonome Native-Node.**
