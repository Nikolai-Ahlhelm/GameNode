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
