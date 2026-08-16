# GameNode — Tenant Foundation & Isolation

## Ziel

GameNode soll echte Mandantenfähigkeit auf einem einzelnen Node erhalten.

Ein Tenant/Mandant repräsentiert einen logisch getrennten Kunden- oder Organisationsbereich.

Beispiel:

```text
GameNode
├── Tenant A
│   ├── User Alice
│   ├── User Bob
│   ├── Minecraft Server
│   └── Project Zomboid Server
│
└── Tenant B
    ├── User Charlie
    └── Palworld Server
```

Ziel dieses Milestones ist die **Tenant Foundation**:

- persistente Tenants
- Tenant-Mitgliedschaften
- Server gehören genau einem Tenant
- physisch getrennte Managed-Server-Verzeichnisse
- Tenant als neuer RBAC-Scope
- Tenant-aware Provisioning
- Tenant-aware Dashboard/API/UI
- sichere Cross-Tenant-Isolation

Noch NICHT Bestandteil:

- Billing
- Payments
- Abonnements
- CPU-/RAM-Quotas
- Storage-Accounting
- automatische Ressourcenzuteilung
- Multi-Node Controller
- Cluster
- Container
- eigener OS-Account pro Tenant
- harte VM-/Container-artige Isolation
- Template-Quotas
- automatische Serverkontingente

Ein späterer Milestone kann darauf aufbauend `Tenant Quotas & Self-Service Provisioning` implementieren.

---

# Grundprinzipien

## Tenant ist eine Domain Entity

Tenants gehören in SQLite.

NICHT:

```yaml
tenants:
  customer-a:
    ...
```

GameNode YAML bleibt lokale Node-/Startup-Konfiguration.

Tenant-Daten gehören in die persistente Domain.

## Rollen bleiben scope-neutral

Die bestehende RBAC-Architektur soll erhalten bleiben.

Aktuell:

```text
Role
→ Permissions

Assignment
→ Subject
→ Role
→ Scope
```

Bisherige Scopes:

```text
global
server
```

Erweitern auf:

```text
global
tenant
server
```

Keine neuen tenant-owned Rollen erzeugen.

Eine Role bleibt wiederverwendbar.

## Server besitzt genau einen Tenant

Jeder normale GameNode Server besitzt:

```text
tenant_id
```

Kein produktiver Server ohne Tenant.

Bestehende Server müssen beim Upgrade automatisch einem Default Tenant zugeordnet werden.

## Managed Storage ist tenant-isoliert

Bestehender Managed-Provisioning-Root:

```text
<data>/servers/<directory>
```

soll für neue managed/provisioned Server ersetzt werden durch:

```text
<data>/tenants/<tenant-storage-id>/servers/<directory>
```

Beispiel:

```text
data/
└── tenants/
    ├── 5f0b.../
    │   └── servers/
    │       ├── minecraft-prod/
    │       └── project-zomboid/
    │
    └── ca31.../
        └── servers/
            └── palworld/
```

Tenant-Namen oder Slugs dürfen NICHT authoritative Filesystem-Identitäten sein.

Verwende stabile IDs.

Tenant-Rename darf keine Server-Dateien verschieben.

## Security Boundary

Tenant Isolation bedeutet zunächst:

**Application-level isolation.**

Sie schützt:

- API
- RBAC
- Files
- Console
- Configuration
- Ports
- Monitoring
- Dashboard
- Provisioning
- Server Access

gegen Cross-Tenant-Zugriff.

Sie ist NICHT automatisch eine harte OS-Sicherheitsgrenze.

Native Spielprozesse laufen weiterhin gemäß bestehendem Runtime-Modell unter dem GameNode-Servicekontext.

Dokumentiere klar:

```text
Tenant isolation does not provide VM/container/process-account isolation
against a malicious native game server process.
```

Keine falschen Security Claims.

---

# Arbeitsschritt 1 — Tenant Domain, Datenmodell und Migration

## 1.1 Bestehenden Stand untersuchen

Vor Änderungen lesen:

- `AGENTS.md`
- `README.md`
- `PROJECT_PLAN.md`
- `docs/architecture.md`
- `docs/security.md`
- `docs/api.md`
- `docs/runtime.md`
- aktuelle Migrationen
- `internal/database`
- `internal/identity`
- `internal/rbac`
- `internal/servers`
- `internal/provisioning`
- `internal/filesystem`
- Dashboard/API/UI

Code und Tests sind Source of Truth.

Keine bestehende Domainlogik anhand dieses Dokuments überschreiben, ohne sie vorher zu prüfen.

## 1.2 Tenant Domain

Neue transport-unabhängige Tenant Domain erstellen.

Bevorzugt:

```text
internal/tenants
```

oder passend zur bestehenden Repository-Struktur.

Model ungefähr:

```text
Tenant
- ID
- Name
- Slug
- Status
- CreatedAt
- UpdatedAt
```

`ID` immutable.

`slug` darf für Darstellung/URLs hilfreich sein, aber NICHT als Security- oder Storage-Identifier dienen.

Status mindestens:

```text
active
disabled
```

Falls `disabled` im aktuellen Scope unnötige Komplexität verursacht, darf Status zunächst nur `active` sein.

Nicht spekulativ ein Lifecycle-System bauen.

## 1.3 Memberships

Tenant-Mitgliedschaft:

```text
tenant_memberships

tenant_id
user_id
created_at
```

Ein User darf mehreren Tenants angehören.

Ein Tenant darf mehrere User enthalten.

Mitgliedschaft allein gewährt KEINE Berechtigung.

RBAC bleibt authoritative.

Membership bedeutet nur:

```text
User belongs to Tenant
```

nicht:

```text
User has automatic Tenant access
```

## 1.4 Server Tenant Ownership

`servers` um verpflichtendes:

```text
tenant_id
```

erweitern.

Jeder Server gehört exakt einem Tenant.

Foreign Key.

Deletion-Semantik bewusst entscheiden.

Bevorzugt:

Tenant mit existierenden Servern kann nicht gelöscht werden.

Keine Cascade, die Server oder Dateien löscht.

## 1.5 Upgrade bestehender Installationen

Neue Migration muss upgrade-safe sein.

Bestehende Installation kann bereits viele Server besitzen.

Beim Upgrade:

1. internen Default Tenant erstellen
2. alle bestehenden Server diesem Tenant zuweisen
3. danach `tenant_id` verpflichtend machen

Default Tenant z.B.:

```text
Default
```

oder:

```text
Local
```

Name ist Produktentscheidung.

ID muss stabil in der DB persistiert werden.

Keine `tenant_id IS NULL` Legacy-Semantik dauerhaft behalten.

Keine bestehende publizierte Migration editieren.

Nur neue Migration hinzufügen.

Fresh DB UND Upgrade DB testen.

## 1.6 Tenant CRUD

Backend Service für:

- list
- get
- create
- update
- delete

Delete nur wenn sicher:

- keine Server
- keine abhängigen Assignments/Memberships, oder sauber kontrolliert entfernen

Keine raw SQLite Errors.

## 1.7 Audit

Tenant Mutationen auditieren:

```text
tenant.create
tenant.update
tenant.delete
tenant.member_add
tenant.member_remove
```

Bestehende Audit-Konvention verwenden.

Keine Mitgliederlisten oder sensitive Daten als große Metadata serialisieren.

## Schritt-1-Abnahme

Tests mindestens:

- fresh migration
- upgrade with existing servers
- Default Tenant assignment
- create/get/list/update/delete
- cannot delete tenant with server
- membership add/remove
- duplicate membership
- invalid user
- invalid tenant
- audit

Nach Schritt 1 weiterarbeiten.
Nicht auf User-Bestätigung warten.

---

# Arbeitsschritt 2 — Tenant-aware Storage und Server Ownership

## 2.1 Zentraler Tenant Storage Resolver

Implementiere einen einzigen zentralen Resolver für managed Server Storage.

Sinngemäß:

```go
TenantServerRoot(dataRoot, tenantID, directoryName)
```

Resultat:

```text
<data>/tenants/<tenant-id>/servers/<directory>
```

Der Resolver muss:

- Tenant ID validieren
- directory_name nach bestehenden Regeln validieren
- traversal verhindern
- absolute paths verhindern
- Windows drive/UNC verhindern
- Root Boundary prüfen

Keine verstreuten `filepath.Join` Konstruktionen.

## 2.2 Provisioning umstellen

Template Provisioning erzeugt neue Server ausschließlich im Tenant Root.

Bisher sinngemäß:

```text
<data>/servers/<directory>
```

Neu:

```text
<data>/tenants/<tenant-id>/servers/<directory>
```

Provisioning Job muss daher Tenant-Kontext persistieren:

```text
tenant_id
```

Job darf keinen freien Hostpfad akzeptieren.

Directory bleibt nur relativer Name.

## 2.3 Reservation Scope

Target reservation darf nicht mehr nur Directory Name betrachten.

Diese Kombinationen müssen erlaubt sein:

```text
Tenant A / minecraft
Tenant B / minecraft
```

aber nicht:

```text
Tenant A / minecraft
Tenant A / minecraft
```

Reservation key sinngemäß:

```text
tenant_id + directory_name
```

## 2.4 Server Create / Custom / Adopt

Unterscheide:

### Managed Server

Provisioned/Create-New:

```text
managed_storage = true
tenant_id required
```

Root liegt im Tenant Storage.

### Adopt Existing

Ein Node Administrator darf weiterhin einen bestehenden externen Serverpfad registrieren und einem Tenant zuordnen.

Beispiel:

```text
D:\ExistingServers\CustomerA\PZ
```

Dieser Server besitzt trotzdem `tenant_id`.

ABER:

Externally adopted servers erfüllen keine garantierte physische Tenant-Storage-Isolation.

Das muss sichtbar/dokumentiert sein.

## 2.5 Tenant User darf keine Hostpfade auswählen

Sehr wichtig.

Falls später Tenant User `Server.Create` erhält:

Er darf KEIN:

- arbitrary working_directory
- absolute path
- adopt existing host path

übermitteln.

Self-Service/Create-New darf nur:

```text
tenant
directory_name
template
variables
```

verwenden.

Custom Application / Adopt Existing bleibt zunächst Node-/Admin-Operation, sofern die bestehende Permission-Struktur nicht bereits einen sicheren Unterschied zulässt.

Keine Hostfilesystem-Eskalation durch Tenant Server.Create.

## 2.6 Filesystem

Bestehende Filesystem-Sandbox bleibt server-root-authoritative.

Keine zweite Tenant-Filesystem-API bauen.

Tenant Check:

```text
RBAC authorizes server
↓
filesystem sandbox authorizes path within server root
```

Beide Gates bleiben unabhängig.

## Schritt-2-Abnahme

Tests mindestens:

- same directory name in different tenants works
- duplicate target in same tenant rejected
- target cannot escape tenant root
- traversal
- absolute path
- drive/UNC
- provisioning stores tenant
- server registration stores tenant
- adopted external path keeps tenant ownership
- server root remains authoritative for filesystem

Danach weiterarbeiten.

---

# Arbeitsschritt 3 — RBAC um Tenant Scope erweitern

## 3.1 Bestehenden Evaluator erweitern

Bestehendes Konzept erhalten:

```text
global
server
```

Neu:

```text
global
tenant
server
```

Für einen Request auf Server S:

```text
S.tenant_id = T
```

Permission ist effektiv, wenn:

```text
active admin bypass
OR
direct global assignment
OR
group global assignment
OR
direct tenant assignment for T
OR
group tenant assignment for T
OR
direct server assignment for S
OR
group server assignment for S
```

Disabled user weiterhin vorher deny.

Keine implicit inheritance zwischen Permission Keys.

`Manage` impliziert weiterhin NICHT `View`.

## 3.2 Permission Catalog erweitern

Bestehendes `allowed_scopes` Modell weiterverwenden.

Serverfähige Permissions sollen grundsätzlich prüfen, ob Tenant Scope sinnvoll ist.

Typische Kandidaten:

```text
Server.View
Server.Edit
Server.Delete
Server.Start
Server.Stop
Server.Restart
Server.Kill

Console.View
Console.Send

Files.View
Files.Edit
Files.Upload
Files.Download
Files.Delete
Files.Rename

Ports.View
Ports.Manage

Monitoring.View
```

Diese sollten wahrscheinlich:

```text
global
tenant
server
```

unterstützen.

Aber nicht blind annehmen.

Bestehenden Permission Catalog prüfen und semantisch korrekt erweitern.

## 3.3 Server.Create

`Server.Create` ist aktuell global-only.

Für Tenant Self-Service Foundation ändern auf:

```text
global
tenant
```

Semantik:

Global:

```text
Server auf jedem Tenant erzeugen
```

Tenant:

```text
Server nur in diesem Tenant erzeugen
```

Server scope für `Server.Create` bleibt unsinnig und darf nicht erlaubt werden.

## 3.4 Platform Permissions bleiben global-only

Folgende Kategorien dürfen NICHT durch Tenant Assignment platformweit wirken:

```text
Users
Groups
Roles
Settings
Templates
Audit
Logs
```

Bestehende reale Permission-Liste prüfen.

Wichtig:

Templates.View kann weiterhin global-only bleiben.

Tenant Self-Service Provisioning benötigt daher zunächst möglicherweise:

```text
Templates.View global
+
Server.Create tenant
```

Wenn das UX-seitig später zu breit ist, ist ein eigener zukünftiger Game-Library/Allowed-Template-Mechanismus möglich.

NICHT jetzt das komplette Template Authorization System umbauen.

## 3.5 Tenant Management Permissions

Neue Permission-Gruppe hinzufügen.

Bevorzugt:

```text
Tenants.View
Tenants.Manage
```

Global-only.

Diese permissions verwalten Tenant Entities selbst.

Sie sind NICHT dasselbe wie Rechte innerhalb eines Tenants.

## 3.6 Tenant Membership vs Access

Membership darf nicht automatisch Server.View gewähren.

Beispiel:

```text
Alice member Tenant A
```

aber ohne Role Assignment:

```text
Alice sees no servers
```

Dann:

```text
Alice
→ Tenant A
→ Role Customer Operator
```

erst dadurch Rechte.

## 3.7 Assignment Validation

Role bleibt scope-neutral.

`server_assignable` bestehende Semantik beibehalten.

Zusätzlich:

```text
tenant_assignable
```

oder eine sauberere allgemeine Scope-Suitability-Ausgabe.

Beispiel:

Role enthält:

```text
Server.View
Console.View
Files.View
```

→ tenant assignable yes
→ server assignable yes

Role enthält:

```text
Users.View
Server.View
```

→ tenant assignable no
→ server assignable no
→ global assignable yes

Keine stillschweigende partielle Role-Auswertung.

Whole-role validation beibehalten.

## 3.8 Role Assignments

Assignment Modell erweitern:

```text
scope_type:
global
tenant
server
```

Tenant Assignment:

```text
scope_id = tenant_id
```

Foreign/resource validation.

Invalid Tenant ID:
controlled error.

## Schritt-3-Abnahme

Tests mindestens:

### Direct assignment

User Alice
→ Tenant A
→ Server.View

Alice sieht alle Server von Tenant A.

Alice sieht keinen Server von Tenant B.

### Group assignment

Alice member Group Operators.

Operators
→ Tenant A
→ Server Operator

Alice erhält Rechte auf alle Server von Tenant A.

### Server override/additional assignment

Alice
→ specific Server B1
→ Console.View

funktioniert unabhängig von Tenant A.

### Global

Global Server.View
→ alle Server aller Tenants.

### Membership only

Tenant membership ohne Role:
keine Serverrechte.

### Scope validation

global-only Role kann nicht tenant/server assigned werden.

### Manage/View independence

bleibt erhalten.

Danach weiterarbeiten.

---

# Arbeitsschritt 4 — Tenant-aware API, Provisioning und vollständige Isolation

## 4.1 Tenant API

Neue Endpoints sinngemäß:

```text
GET    /api/v1/tenants
POST   /api/v1/tenants

GET    /api/v1/tenants/{id}
PATCH  /api/v1/tenants/{id}
DELETE /api/v1/tenants/{id}

GET    /api/v1/tenants/{id}/members
POST   /api/v1/tenants/{id}/members
DELETE /api/v1/tenants/{id}/members/{userId}

GET    /api/v1/tenants/{id}/servers
GET    /api/v1/tenants/{id}/access
```

Mutationen:
CSRF.

Tenant entity management:
global `Tenants.View/Manage`.

Server access innerhalb Tenant:
bestehende RBAC principles.

## 4.2 Server APIs

Server DTO ergänzt mindestens:

```text
tenant_id
tenant_name
```

Nur sichere Identität/Anzeige.

Keine Tenant Storage Root oder absolute Hostpfade exponieren.

## 4.3 Server List

`GET /servers`

muss weiterhin nur Server liefern, für die `Server.View` effektiv ist.

Aber Evaluator berücksichtigt jetzt:

```text
global
tenant
server
```

Kein separater Frontendfilter als Security Boundary.

## 4.4 Dashboard

Dashboard muss serverseitig tenant-aware sein.

Counts:

- visible servers only
- visible running
- monitoring summaries
- ports
- etc.

Tenant A User darf keine aggregierten Werte von Tenant B indirekt sehen.

Beispiel:

Tenant A:
2 Servers

Tenant B:
10 Servers

Alice sieht nur Tenant A.

Dashboard darf nicht:

```text
Servers: 12
Visible: 2
```

anzeigen.

Das wäre Information Leakage.

## 4.5 Server Detail Subsystems

Cross-Tenant Enforcement explizit testen für:

```text
server detail
lifecycle
console
files
configuration
ports
monitoring
access assignments
```

Kenntnis einer Server UUID darf keinen Zugriff geben.

## 4.6 Provisioning API

Provision request benötigt Tenant.

Beispiel:

```json
{
  "tenant_id": "...",
  "server_name": "...",
  "directory_name": "...",
  "variables": {}
}
```

Backend prüft:

```text
Templates.View
AND
Server.Create at selected tenant
```

Admin bypass weiterhin.

Tenant aus Request nie einfach vertrauen.

Existenz + Berechtigung prüfen.

Provision Job an Tenant binden.

## 4.7 Existing job security

Provisioning job ownership bleibt bestehen.

Zusätzlich Tenant-Kontext sicher persistieren.

Job API darf keine Tenant-/Serverinformationen offenlegen, die Actor nicht sehen darf.

Bestehende owner/admin Semantik prüfen.

Keine unnötige neue Cross-user Job Visibility einführen.

## 4.8 Server Move zwischen Tenants

NICHT implementieren.

Ein bestehender Server wechselt in diesem Milestone nicht den Tenant.

Grund:

- Storage
- Assignments
- Audit
- Ports
- Secrets
- ownership

würden daraus eine eigene sicherheitskritische Operation machen.

Tenant ID nach Server Creation zunächst immutable.

## 4.9 Tenant Delete

Tenant Delete nur:

```text
no servers
```

Memberships/Assignments dürfen kontrolliert entfernt werden.

Keine Serverfiles löschen.

Keine implizite recursive cleanup.

## 4.10 Cross-Tenant Security Regression Suite

Erstelle explizite Integrationtests:

```text
Tenant A
User A
Server A

Tenant B
User B
Server B
```

User A darf auf Server B nicht zugreifen über:

- GET detail
- lifecycle
- console websocket
- files read
- files mutate
- download
- upload
- config
- ports
- monitoring
- access listing
- server update
- server delete

Erwartung:
kontrollierter authorization denial / normal resource hiding entsprechend bestehender API-Konvention.

Keine Information Leakage über Fehlermeldungen.

## Schritt-4-Abnahme

Backend Integration Suite muss beweisen:

- Tenant-scoped Server.View
- Global Server.View
- server-scoped Server.View
- group inheritance
- Tenant Create Permission
- tenant provisioning
- cross-tenant denial
- dashboard filtering
- filesystem isolation
- console isolation
- config isolation
- port isolation
- monitoring isolation

Danach weiterarbeiten.

---

# Arbeitsschritt 5 — Tenant Administration UI, Upgrade UX, Dokumentation & Acceptance

## 5.1 Navigation

Neue Admin Navigation:

```text
Tenants
```

nur sichtbar bei:

```text
Tenants.View
```

Backend bleibt authoritative.

## 5.2 Tenant List

Anzeigen:

- Name
- Slug
- Server count
- Member count
- Status falls implementiert

Actions abhängig von Capabilities.

Create Tenant.

## 5.3 Tenant Detail

Tabs ungefähr:

```text
Overview
Servers
Members
Access
```

### Overview

- name
- ID
- created
- server count
- member count

Keine absoluten Storagepfade.

### Servers

alle Server des Tenants, die Actor sehen darf.

### Members

Tenant Mitglieder:

- add
- remove

Membership ist sichtbar getrennt von Access.

### Access

RBAC Assignments auf Tenant Scope.

User oder Group:

```text
Subject
Role
Tenant Scope
```

Bestehende Role Assignment UI wiederverwenden.

Keine zweite RBAC-Implementierung bauen.

## 5.4 Role Editor

Scope-Hinweise erweitern:

```text
Global
Tenant
Server
```

Role Detail zeigt:

```text
Global assignable
Tenant assignable
Server assignable
```

oder eine besser integrierte Darstellung.

Wenn Role nicht Tenant-assignable:

verständlicher Grund.

Keine leeren Dropdowns ohne Erklärung.

## 5.5 Create Server

Create/Provision Flow muss Tenant auswählen.

Admin mit global Server.Create:

```text
Tenant selector
```

User mit Server.Create nur auf genau einem Tenant:

Tenant kann vorausgewählt/gesperrt sein.

Bei mehreren erlaubten Tenants:
nur erlaubte Tenants im Selector.

Backend prüft trotzdem.

## 5.6 Server Detail

Server Overview zeigt:

```text
Tenant: Müller GmbH
```

Tenant-ID optional technisch sichtbar.

Kein Storage Host Path.

## 5.7 User Experience

Wenn User nur Tenant A sehen darf:

Servers Page:

```text
Minecraft
Project Zomboid
```

Keine Hinweise auf Server anderer Tenants.

Falls kein Server sichtbar:

```text
No servers are currently assigned to you.
```

Nicht:

```text
There are 12 servers but you can access none.
```

## 5.8 Default Tenant UX

Nach Upgrade existiert Default Tenant.

Admin soll ihn normal umbenennen können.

Bestehende Server bleiben dort.

Kein Zwang, sofort Tenant-Struktur manuell aufzuräumen.

## 5.9 Dokumentation

Aktualisiere:

- `AGENTS.md`
- `README.md`
- `docs/architecture.md`
- `docs/security.md`
- `docs/api.md`
- `docs/development.md`

Neue ADR erstellen:

```text
docs/adr/0006-tenant-domain-and-isolation.md
```

oder nächste freie Nummer.

ADR muss erklären:

### Problem

Mehrere unabhängige Kunden/Organisationen auf einem GameNode.

### Decision

Tenant domain in SQLite.

Server exactly one tenant.

RBAC scopes:

```text
global
tenant
server
```

Managed storage:

```text
<data>/tenants/<tenant-id>/servers/<directory>
```

### Security

Application-level isolation.

Keine OS-/VM-/Container Isolation.

### Consequences

Future quotas/self-service möglich.

Server Tenant assignment immutable for now.

---

# Späterer Milestone vorbereiten, aber NICHT implementieren

Die Architektur soll später ermöglichen:

```text
Tenant Quotas

max_servers
```

und danach:

```text
Self-Service Provisioning
```

Sinngemäß:

```text
Tenant
max_servers = 3

current_servers = 2

User with:
Server.Create at tenant

→ may create one additional server
```

Aber:

KEINE quota tables oder enforcement jetzt nur aus Spekulation hinzufügen.

Nur sicherstellen, dass Tenant-ID überall sauber vorhanden ist.

---

# Security Review

Vor Abschluss ausdrücklich prüfen:

## Authorization

Kann irgendeine Route eine Server-ID verwenden, ohne Tenant-aware RBAC Evaluation?

## Filesystem

Kann directory_name Tenant Root verlassen?

Kann Tenant A denselben Pfad wie Tenant B überschreiben?

## Provisioning

Kann ein User einen fremden Tenant angeben?

Kann Server.Create tenant scope umgangen werden?

## Adopt

Kann ein Tenant User beliebige Host Paths registrieren?

Das muss verhindert sein.

## API leakage

Enthalten:

- dashboard
- counts
- errors
- audit
- jobs

Informationen anderer Tenants?

## WebSocket

Console View muss tenant-aware bleiben.

## Assignments

Kann eine global-only Role tenant assigned werden?

## Server tenant mutation

Kann Tenant ID über normalen PATCH geändert werden?

Soll in diesem Milestone verboten sein.

---

# Migration / Compatibility

Bestehende Server dürfen durch Upgrade nicht kaputtgehen.

Prüfe insbesondere:

- working_directory
- executable
- arguments
- environment
- ports
- runtime state
- process rediscovery
- config adapter snapshots
- template provenance
- existing access assignments

Bestehende Serverdateien NICHT automatisch nach:

```text
<data>/tenants/...
```

verschieben.

Sehr wichtig:

Existing pre-migration servers bleiben physisch dort, wo sie sind.

Sie erhalten nur logisch:

```text
tenant_id = Default Tenant
```

Nur NEUE managed/provisioned Server nutzen das neue Tenant Storage Layout.

Keine Gigabyte-Dateimigration während SQLite Schema Upgrade.

---

# Tests

Nach Implementierung vollständige Tests.

Backend:

```text
gofmt
go vet ./...
go test ./...
go build ./...
```

Race falls Umgebung unterstützt:

```text
go test -race ./...
```

Frontend:

```text
npm ci
npm run check
npm run test:helpers
npm run build
```

Build:

- Windows amd64
- Linux amd64

Zusätzlich:

```text
git diff --check
```

---

# Real Browser Acceptance

Wenn lokale Runtime verfügbar ist:

## Setup

Erstelle:

```text
Tenant A
Tenant B
```

Users:

```text
alice
bob
```

Servers:

```text
Tenant A:
- Minecraft A
- Project Zomboid A

Tenant B:
- Palworld B
```

Role:

```text
Tenant Operator

Server.View
Server.Start
Server.Stop
Console.View
Files.View
Monitoring.View
```

Assignment:

```text
Alice
→ Tenant A
→ Tenant Operator
```

Bob:

```text
Bob
→ Tenant B
→ Tenant Operator
```

## Alice Acceptance

Alice sieht:

```text
Minecraft A
Project Zomboid A
```

Alice sieht NICHT:

```text
Palworld B
```

Direkte API mit Palworld-B-ID:
denied.

Console:
denied.

Files:
denied.

Monitoring:
denied.

## Bob Acceptance

Inverse Prüfung.

## Global Admin

Admin sieht alle Tenants/Server.

## Global Server Viewer

Nicht-Admin User:

```text
global Server.View
```

sieht alle Server aller Tenants.

## Tenant Create

User:

```text
Server.Create at Tenant A
```

darf neuen Template Server in Tenant A provisionieren.

Darf NICHT Tenant B auswählen.

Darf keinen arbitrary host path angeben.

---

# Scope bewusst begrenzen

NICHT implementieren:

- Billing
- Plans
- subscriptions
- invoices
- payments
- max CPU
- max RAM
- max disk
- template allowlists
- port ranges per tenant
- per-tenant OS account
- process sandbox
- Docker
- containers
- controller
- multi-node
- tenant migration between nodes
- server move between tenants
- tenant export/import
- quotas

Diese Themen sind Folge-Milestones.

---

# Erwarteter Abschlussbericht

Berichte:

## Step 1
- tenant domain
- migration
- Default Tenant
- memberships
- server ownership

## Step 2
- storage resolver
- managed layout
- provisioning changes
- adopted server semantics

## Step 3
- tenant RBAC scope
- permission catalog changes
- Server.Create semantics
- role suitability
- evaluator behavior

## Step 4
- APIs
- provisioning authorization
- dashboard filtering
- subsystem isolation
- cross-tenant regression tests

## Step 5
- UI
- browser acceptance
- documentation
- ADR
- builds/tests

Zusätzlich:

- exact migrations
- security review
- upgrade behavior
- known limitations
- race result
- frontend result
- Windows/Linux build result
- real acceptance result

---

# Final Status

Wenn Tenant Foundation vollständig implementiert und automatisiert getestet ist:

```text
TENANT_FOUNDATION_IMPLEMENTATION_COMPLETE
```

Wenn zusätzlich reale Browser-/Runtime-Acceptance erfolgreich war:

```text
TENANT_FOUNDATION_REALWORLD_ACCEPTED
```

Wenn fundamentale Architektur- oder Security-Probleme offen bleiben:

```text
TENANT_FOUNDATION_BLOCKED
```

mit exaktem Repro und Ursache.

---

# Wichtigste Architektur-Invarianten

Am Ende muss folgendes gelten:

```text
Tenant ist DB-Domain, nicht YAML-Konfiguration.

Server gehört genau einem Tenant.

Role ist scope-neutral.

Assignment trägt Scope.

Scopes:
global
tenant
server

Global permission
→ alle passenden Ressourcen

Tenant permission
→ Ressourcen dieses Tenants

Server permission
→ genau dieser Server

Managed provisioning:
<data>/tenants/<tenant-id>/servers/<directory>

Existing legacy/adopted servers:
dürfen physisch außerhalb liegen,
bleiben aber logisch einem Tenant zugeordnet.

Tenant membership allein:
keine Berechtigung.

Backend:
authoritative.

Frontend:
nur affordance layer.

Keine Cross-Tenant-Leaks.

Keine automatische Server-Dateimigration.

Keine harte OS-Isolation behaupten.
```
