# GameNode — UI Modernization & Theme Foundation

## Ziel

GameNode soll visuell deutlich moderner, klarer und hochwertiger wirken.

Der aktuelle Dark Mode soll zu einem echten Theme-System erweitert werden. Gleichzeitig sollen visuelle Hierarchie, Oberflächen, Tabellen, Formulare, Navigation und Serveransichten überarbeitet werden.

Dieser Milestone ist ausdrücklich eine **UI-/UX- und Theme-Foundation**.

Er soll keine neue Business-Logik erfinden und keine Backend-Sicherheitsregeln umgehen.

---

# Leitbild

GameNode soll wirken wie eine moderne Infrastruktur-/Admin-Anwendung:

- klare visuelle Ebenen
- hochwertige Dark- und Light-Themes
- gute Lesbarkeit
- moderne Cards, Panels und Tabellen
- konsistente Forms
- bessere Statusdarstellung
- klarer Application Shell
- Tenant-Kontext gut sichtbar
- responsive Desktop-first UI
- keine übertriebene Neon-/Glassmorphism-Optik
- keine dekorativen Fake-Metriken
- keine verspielte Consumer-App-Ästhetik

Orientierung:

- Grafana-artige Informationsdichte
- moderne SaaS-/Infrastructure-Dashboards
- subtile Tiefe
- klare Flächen
- gute Kontraste
- reduzierte, kontrollierte Animationen

---

# Wichtige Architekturregeln

Vor Änderungen lesen:

- `AGENTS.md`
- `README.md`
- `docs/architecture.md`
- `docs/security.md`
- `docs/api.md`
- aktuelle Frontend-Struktur unter `web/`
- bestehende Settings-/Branding-Implementierung
- Tenant UI
- Users / Groups / Roles
- Dashboard
- Servers
- Server Detail
- Templates / Game Library

Code und Tests sind Source of Truth.

Die bestehende React + TypeScript + Vite Architektur bleibt bestehen.

Keinen neuen Router oder neues UI-Framework einführen, nur um das Design zu ändern.

Bestehende Komponenten und Patterns bevorzugt weiterverwenden und evolutionär verbessern.

Backend bleibt Security Boundary.

Capability Checks im Frontend dienen nur der Darstellung.

---

# Scope

Dieser Milestone umfasst:

1. Theme Foundation
2. Light Mode
3. Dark Mode Überarbeitung
4. User Theme Preferences
5. Background-/Wallpaper Foundation
6. Application Shell
7. Sidebar
8. Topbar / Context Header
9. Tenant Context
10. Design Tokens
11. Cards / Panels / Surfaces
12. Tabellen
13. Formulare
14. Dialoge / Drawers
15. Empty / Loading / Error States
16. Status-System
17. Server Cards / Server List
18. Dashboard
19. Server Detail
20. Settings / Users / Groups / Roles / Tenants
21. Toasts / Notifications
22. Responsive Verhalten
23. Accessibility
24. Microinteractions
25. Dokumentation und Tests

Nicht Bestandteil:

- neues RBAC-Modell
- neue Business Features
- neue Monitoring-Daten
- Fake-Charts
- global search backend
- command palette backend
- Plugin-System
- Custom CSS Injection
- Theme Marketplace
- remote wallpapers
- tenant-specific billing
- große Routing-Umbauten

---

# 1. Design System und Tokens

Baue eine zentrale Theme-/Design-Token-Schicht.

Keine verstreuten hardcodierten Farben über Komponenten hinweg.

Definiere mindestens Tokens für:

## Background Layers

- page background
- app shell
- sidebar
- panel
- card
- elevated card
- dialog
- popover
- input background
- hover surface
- selected surface

## Borders

- subtle border
- normal border
- strong border
- focus ring

## Text

- primary
- secondary
- muted
- disabled
- inverse

## Semantic

- accent
- success
- warning
- danger
- info

## Other

- shadow levels
- radius levels
- spacing scale
- transition duration
- overlay
- backdrop
- wallpaper dim
- wallpaper blur

CSS variables bevorzugen.

Beispiel sinngemäß:

```css
:root {
  --bg-page: ...;
  --bg-surface: ...;
  --bg-elevated: ...;
  --border-subtle: ...;
  --text-primary: ...;
  --text-muted: ...;
  --accent: ...;
}
```

Keine Komponenten sollen direkt von "dark = true" abhängen, wenn es über Tokens lösbar ist.

---

# 2. Theme-Modell

Unterstütze mindestens:

- dark
- light
- system

`system` folgt `prefers-color-scheme`.

Theme-Wechsel muss ohne Reload funktionieren.

Theme muss beim initialen Laden früh genug angewendet werden, damit kein störender Flash vom falschen Theme entsteht.

Wenn bereits Settings/User Preferences existieren:

bestehende Architektur verwenden.

Wenn noch keine per-user UI Preferences existieren:

eine kleine, saubere Preference-Lösung ergänzen.

Bevorzugt persistent pro User.

Nicht globale Instance Settings für persönliche Theme-Auswahl missbrauchen.

Falls Backend-Persistenz hierfür aktuell zu groß wäre:

saubere lokale Preference-Schicht als Zwischenlösung möglich, aber klar dokumentieren.

---

# 3. Dark Theme überarbeiten

Dark Mode soll nicht einfach nur "fast schwarz" sein.

Ziel:

- Navy/Slate Foundation
- klare Surface-Hierarchie
- Cards sichtbar vom Page Background abheben
- leichte Schatten
- subtile Borders
- bessere Hover-Zustände
- weniger flache graue Fläche

Vermeide:

- reines Schwarz überall
- Neon Cyan überall
- starke Verläufe
- Glassmorphism auf jeder Card
- harte weiße Borders

---

# 4. Light Theme

Light Mode als vollwertiges Theme implementieren.

Nicht invertieren.

Achte auf:

- kontrastreiche Textfarben
- warme/neutrale helle Backgrounds
- sichtbare, aber dezente Borders
- Cards mit leichter Tiefe
- keine zu grauen Flächen
- klare Inputs
- gute Statusfarben

Alle wichtigen Views müssen in Light Mode visuell funktionieren.

---

# 5. Accent Color Foundation

Wenn bereits Accent Color existiert:

in Theme-System integrieren.

Sonst optional eine kontrollierte Accent-Auswahl vorbereiten.

Keine freie CSS-Farbeingabe notwendig.

Beispiel Presets:

- blue
- cyan
- indigo
- violet
- emerald

Nur falls bestehende Settings-Architektur das sauber unterstützt.

Kein Scope Creep.

---

# 6. User Preferences

Langfristig sinnvolle UI Preferences:

- theme
- accent
- density
- sidebar collapsed
- wallpaper enabled
- wallpaper image
- wallpaper dim
- wallpaper blur
- preferred tenant

In diesem Milestone mindestens:

- theme
- sidebar collapsed
- wallpaper settings soweit implementiert

Density nur implementieren, wenn ohne großen Umbau möglich.

Preferences klar von globalen Instance Settings trennen.

---

# 7. Wallpaper / Background Image Foundation

User soll perspektivisch eigene Background Images verwenden können.

Implementiere hierfür eine sichere Foundation.

Wenn Upload direkt implementiert wird:

- nur Bilder
- MIME/Dateityp validieren
- Größenlimit
- Dimensionen begrenzen
- keine SVGs
- keine Remote URLs
- keine HTML/CSS Injection
- lokale Speicherung
- nur eigener User darf eigene Preference ändern
- keine absolute Hostpfade exponieren

Unterstützte Formate sinnvoll begrenzen auf:

- PNG
- JPEG
- WebP

Optional:

- blur
- dim strength
- position
- cover

Background Layer darf Content-Lesbarkeit nicht zerstören.

Darüberliegende UI-Flächen müssen ausreichend opaque bleiben.

Kein Full-Glass UI.

---

# 8. Application Shell

Überarbeite das Grundlayout:

```text
Sidebar
Topbar / Context Header
Main Content
Overlay / Wallpaper Layer
```

Ziel:

- klarer, moderner
- weniger flach
- bessere Abstände
- Content max-width nur dort, wo sinnvoll
- breite Admin-Ansichten dürfen den Platz nutzen

---

# 9. Sidebar

Sidebar verbessern:

- aktive Navigation klar hervorheben
- Icons + Labels
- gruppierte Navigation
- collapse auf icon-only
- smooth transition
- Tooltip im collapsed state
- User/Profile Bereich unten
- Tenant Context sinnvoll integrieren
- Branding oben

Keine neue Navigation Architecture einführen.

Bestehende State-basierte Navigation beibehalten, falls sie aktuell so funktioniert.

---

# 10. Topbar / Context Header

Einheitlicher Seitenkopf:

- Breadcrumbs oder Context Label
- Page Title
- Subtitle optional
- Tenant Context
- Primary Actions rechts
- Theme Switch
- User Menu

Nicht jede Seite separat anders lösen.

Reusable Component bauen.

---

# 11. Tenant Context

Da GameNode jetzt Tenant-aware ist:

Tenant-Kontext visuell sauber darstellen.

Beispiele:

- Tenant Badge auf Server Cards
- Tenant im Server Detail
- Tenant im Create Server Flow
- Tenant Name in Context Header
- Tenant Access Views

Falls User mehrere Tenants nutzen kann:

Foundation für Tenant Switcher sinnvoll vorbereiten.

Aber keinen künstlichen globalen Workspace-Switch erzwingen, wenn API/Navigation noch nicht darauf ausgelegt ist.

---

# 12. Cards und Surfaces

Cards sollen sich klarer vom Hintergrund abheben.

Verbessern:

- Hintergrund
- Border
- Shadow
- Padding
- Radius
- Hover
- Selected State
- Header/Body/Footer Struktur

Nicht jede kleine Information braucht eine Card.

Card Density sinnvoll halten.

---

# 13. Status-System

Zentrales visuelles Status-System.

Mindestens:

- running
- starting
- stopping
- stopped
- crashed
- detached
- degraded
- unknown

Einheitliche:

- Badge
- Dot/Icon
- Farbe
- Text
- Tooltip

Status darf nicht nur durch Farbe vermittelt werden.

Keine unterschiedlichen Statusfarben pro Seite.

---

# 14. Server Cards / Server List

Serverdarstellung modernisieren.

Mindestens sichtbar:

- Server Name
- Game / Template wenn vorhanden
- Tenant
- Status
- CPU/RAM falls echte Daten vorhanden
- Uptime falls vorhanden
- relevante Ports optional
- Primary Actions

Keine Fake-Daten.

Nicht überladen.

Wichtige Informationen müssen scanbar sein.

---

# 15. Dashboard

Dashboard modernisieren:

- klarere KPI Cards
- bessere Gruppierung
- Statusübersicht
- echte Monitoring-Daten
- Recent Actions wenn erlaubt
- Tenant-aware Counts
- keine Informationen versteckter Server

Optional kleine Sparklines nur wenn echte History vorhanden ist.

Keine dekorativen Fake-Charts.

---

# 16. Tabellen

Modernisiere Tabellen für:

- Users
- Groups
- Roles
- Tenants
- Audit
- ggf. Serverlisten

Features soweit bestehende Daten/API sie erlauben:

- bessere Header
- row hover
- selected state
- actions rechts
- search/filter UI
- sort indication
- responsive overflow
- sticky header wo sinnvoll
- klare Empty State

Keine neue serverseitige Query-Engine nur fürs Design bauen.

---

# 17. Forms

Einheitliche moderne Form-Komponenten.

Verbessern:

- Label
- Description
- Input
- Validation Message
- Required Indicator
- Disabled State
- Focus State
- Secret Input
- Number Input
- Select
- Checkbox
- Toggle
- Textarea

Große Forms in Sections gliedern.

Beispiele:

- General
- Access
- Network
- Advanced
- Danger Zone

Keine Autosaves einführen, wenn bestehende Semantik explizites Save nutzt.

Dirty State sichtbar machen, wenn bestehende Formstruktur dies sinnvoll zulässt.

---

# 18. Sticky Save / Form Actions

Bei langen Edit-Seiten:

optional sticky action footer:

- Save
- Cancel
- unsaved changes indicator

Nur dort, wo passend.

Nicht über jede kleine Form legen.

---

# 19. Dialoge und Drawers

Kleine Aktionen:

- Member hinzufügen
- Role zuweisen
- Port hinzufügen
- Confirm Delete

als konsistente Dialoge/Drawers.

Große Edit-Flows bleiben eigene Views.

Dialoge:

- Focus trap
- Escape
- Keyboard
- klare Danger Actions

---

# 20. Empty States

Jede wichtige Liste/Section braucht einen guten Empty State.

Beispiele:

- No servers
- No tenants
- No members
- No role assignments
- No ports
- No audit events

Empty State:

- Icon
- kurzer Text
- hilfreiche Action wenn erlaubt

Keine Information Leakage.

---

# 21. Loading States

Spinner nicht überall als einzige Lösung.

Nutze:

- Skeletons
- placeholder rows
- stable layout

Keine springenden Layouts.

---

# 22. Error States

Einheitliche Error-Komponente:

- verständliche Summary
- optional Retry
- keine raw backend internals
- keine unhandled promise rejections

Seiten dürfen bei einem API-Fehler nicht nur leere Background-Fläche anzeigen.

---

# 23. Toast / Notification System

Einheitliches Notification Pattern.

Typen:

- success
- info
- warning
- error

Keine unterschiedlichen Ad-hoc Alerts pro Seite.

Toasts:

- kurz
- verständlich
- keine Secrets
- kein roher Stacktrace

---

# 24. Destructive Actions

Einheitliche Danger Zone.

Delete / Kill / Remove Access:

- klare danger color
- Confirmation
- Resource Name anzeigen
- keine versteckten Side Effects

Nicht jede Warnung rot machen.

---

# 25. Settings UI

Settings visuell modernisieren.

Gruppieren:

- Appearance
- Branding
- Monitoring
- Logging
- Security
- Support / Diagnostics

Appearance enthält:

- theme
- accent falls unterstützt
- wallpaper
- blur/dim
- sidebar preference

User Preference und globale Instance Settings klar trennen.

---

# 26. Users / Groups / Roles / Tenants

Diese Admin-Views konsistent modernisieren.

User Detail:

- General
- Groups
- Access
- Security
- Danger Zone

Group Detail:

- General
- Members
- Access

Role Detail:

- General
- Permissions
- Scope Suitability

Tenant Detail:

- Overview
- Servers
- Members
- Access

Keine neue Businesslogik hinzufügen.

---

# 27. Server Detail

Server Detail klarer strukturieren.

Header zeigt mindestens:

- Server Name
- Status
- Tenant
- Game/Template
- Primary Actions

Tabs vorhandene Struktur nutzen:

- Overview
- Console
- Files
- Configuration
- Networking
- Monitoring
- Access

Tabs visuell verbessern.

Keine neue Router-Struktur.

---

# 28. Console UI

Console modernisieren ohne Funktionalität zu ändern.

- bessere Toolbar
- connection state
- attached/detached klar
- input area klar
- scroll behavior erhalten
- monospace
- gute Light/Dark Darstellung

Keine Persistenz von Console Output einführen.

---

# 29. File Browser

Visuell verbessern:

- Breadcrumb
- Table/List
- File/Folder Icons
- Actions
- Monaco Theme passend zum App Theme
- bessere Loading/Error States

Filesystem Security nicht verändern.

---

# 30. Monitoring

Monitoring Cards:

- CPU
- RAM
- Uptime
- PID
- Health

Charts nur echte Daten.

Light/Dark kompatibel.

Keine Fake Health Checks.

---

# 31. Templates / Game Library

Cards und Detailansichten verbessern:

- Game Name
- Source
- Platforms
- Installer
- Compatibility
- Provisionability
- Tags
- Primary Action

Create Flow visuell klarer machen.

Keine Template-Semantik ändern.

---

# 32. Microinteractions

Subtile Animationen:

- hover
- button press
- sidebar collapse
- tab state
- toast appearance
- dialog open
- status transition

Respektiere:

`prefers-reduced-motion`

Keine langen oder verspielten Animationen.

---

# 33. Accessibility

Prüfe mindestens:

- ausreichender Kontrast
- sichtbarer Keyboard Focus
- Labels
- aria-labels bei icon-only buttons
- Dialog Focus
- Keyboard Navigation
- disabled state
- status nicht nur Farbe
- reduced motion
- Light/Dark Kontrast

Keine Accessibility Regression durch neue Farben.

---

# 34. Responsive Verhalten

Desktop bleibt primär.

Aber prüfen:

- Sidebar wird auf kleineren Screens sinnvoll
- Tables scrollen oder wechseln Layout
- Header Actions umbrechen
- Tabs scrollbar
- Dialoge passen
- Forms stacken
- Server Cards funktionieren

Keine komplette Mobile-App bauen.

---

# 35. Performance

Vermeide:

- massive Re-Renders
- riesige Background Images ohne Limit
- unnötige Animationen
- redundante API Calls nur fürs Design

Wallpaper Rendering soll GPU-/CPU-seitig vernünftig bleiben.

---

# 36. Security für Wallpaper Upload

Falls Upload in diesem Milestone enthalten ist:

Backend muss authoritative validieren.

Mindestens:

- authenticated
- ownership
- CSRF
- content size cap
- MIME/content validation
- PNG/JPEG/WebP only
- no SVG
- no arbitrary filename path
- no URL input
- no HTML
- safe generated storage key
- delete/replace old image controlled
- support bundle enthält keine Wallpaper Bytes
- diagnostics exponiert keinen Pfad

Keine Base64-Megabytes in Settings JSON speichern, sofern unnötig.

---

# 37. Testing

Frontend mindestens:

```text
npm ci
npm run check
npm run test:helpers
npm run build
```

Neue Helper Tests für:

- theme resolution
- system theme
- status mapping
- wallpaper preference sanitization
- scope/status presentation
- relevant formatting helpers

Backend nur wenn neue Preference-/Wallpaper APIs nötig sind:

```text
gofmt
go vet ./...
go test ./...
go build ./...
```

API Tests für:

- auth
- CSRF
- wallpaper upload validation
- user ownership
- content type
- size limits
- preference persistence

---

# 38. Visual Acceptance

Manuell im Browser prüfen.

Mindestens:

## Dark Mode

- Dashboard
- Servers
- Server Detail
- Console
- Files
- Users
- Groups
- Roles
- Tenants
- Templates
- Audit
- Settings

## Light Mode

gleiche Views.

## Wallpaper

- disabled
- enabled
- bright wallpaper
- dark wallpaper
- blur
- dim

Content muss lesbar bleiben.

## Responsive

mindestens Desktop + schmaler Browser.

## Browser Console

keine:

- React errors
- unhandled promises
- failed required assets
- broken theme initialization

---

# 39. Keine Fake-Daten

Sehr wichtig:

Kein UI darf:

- erfundene CPU-Werte
- erfundene RAM-Werte
- erfundene Health-Daten
- erfundene Charts
- erfundene Audit Entries

anzeigen.

Wenn Daten nicht vorhanden sind:

Empty/Unavailable State.

---

# 40. Dokumentation

Aktualisiere:

- `AGENTS.md`
- `README.md`
- `docs/architecture.md`
- `docs/development.md`
- ggf. `docs/security.md`
- ggf. `docs/api.md`

Dokumentiere:

- theme model
- user preferences
- wallpaper security
- application shell
- design tokens
- no custom CSS injection
- no remote wallpapers

---

# 41. Scope Guard

Nicht implementieren:

- Theme Marketplace
- arbitrary CSS
- arbitrary JavaScript
- remote image URLs
- HTML widgets
- dashboard plugin system
- global search backend
- command palette backend
- user-defined themes via raw JSON
- per-tenant billing UI
- redesign of RBAC
- redesign of routing
- replacement of React/Vite stack

---

# 42. Abschlussbericht

Berichte:

- theme architecture
- design token system
- dark theme changes
- light mode
- system theme
- preference persistence
- wallpaper foundation
- wallpaper security
- sidebar/topbar changes
- tenant context changes
- card/table/form/dialog changes
- status system
- dashboard/server detail improvements
- admin UI improvements
- accessibility
- responsive behavior
- tests
- browser acceptance
- build results
- known limitations

Final Status:

```text
UI_MODERNIZATION_THEME_FOUNDATION_COMPLETE
```

Wenn zusätzlich reale Browser-Acceptance für Dark/Light/Wallpaper erfolgreich war:

```text
UI_MODERNIZATION_REALWORLD_ACCEPTED
```

Wenn ein grundlegender Architektur-/Security-Blocker offen bleibt:

```text
UI_MODERNIZATION_BLOCKED
```

---

# Wichtigste Invarianten

Am Ende muss gelten:

```text
Dark und Light sind vollwertige Themes.

Theme wird über zentrale Tokens gesteuert.

Keine verstreuten hardcodierten Theme-Farben.

Wallpaper ist rein visuell.

Wallpaper kann keine Business-/Security-Logik beeinflussen.

Keine Remote URLs.

Keine SVG-/HTML-/CSS-Injection.

Frontend bleibt capability-aware.

Backend bleibt authoritative.

Keine Fake-Daten.

Keine neue Router-/Framework-Architektur nur fürs Design.

Tenant Context bleibt sichtbar und verständlich.

Alle wichtigen Views funktionieren in Dark und Light.

UI wirkt klarer, moderner und stärker vom Hintergrund getrennt.
```
