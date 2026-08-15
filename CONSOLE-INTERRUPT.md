Arbeite im aktuellen GameNode-Branch.

Ausgangslage:

Das Official Satisfactory Template funktioniert unter Windows für Installation und Start. Beim Stoppen verwendet es aktuell jedoch:

stop_method: terminate

GameNode führt dabei unter Windows zunächst `taskkill /PID <pid> /T` aus und eskaliert nach dem Timeout zu Force-Kill beziehungsweise `TerminateProcess`.

Pelican/Pterodactyl beschreibt für Satisfactory dagegen `^C`, also einen Console Interrupt. In GameNode darf `^C` nicht als normaler stdin-String implementiert werden. Ein echtes Ctrl+C beziehungsweise Ctrl+Break ist unter Windows ein Console-Control-Event.

Ziel dieses Turns:

Eine sichere, wiederverwendbare und servicefähige Windows-Console-Interrupt-Stopmethode implementieren und Satisfactory damit nachweislich graceful stoppen.

Keine Satisfactory-Sonderlogik in der Runtime.
Keine Shell-Ausführung.
Keine PowerShell-/CMD-Wrapper.
Keine HTTP- oder REST-Stop-Hooks.
Keine Script-Engine.
Keine Änderungen an Linux, außer notwendige Interface-Stubs oder Unsupported-Rückgaben.
Keine automatische Interpretation beliebiger Egg-Stopstrings.

Bestehende Sicherheitsprinzipien beibehalten:

- strukturierte Executable plus Arguments[]
- PID plus StartKey als Prozessidentität
- genau einmal ausgeführte Finalisierung
- Timeout mit sicherem Force-Kill-Fallback
- keine Signale an fremde Prozesse oder andere GameNode-Server
- bestehende Server und Stopmethoden bleiben kompatibel
- Templates bleiben Daten, kein ausführbarer Code

## 1. Bestehende Runtime rekonstruieren

Analysiere vollständig:

- `internal/runtime`
- `native_windows.go`
- `native_linux.go`
- `Runtime` Interface
- `StartOptions`
- Windows-Prozessstart
- stdin/stdout/stderr Pipes
- Prozessidentität und `StartKey`
- `servers.Service.Start`
- `servers.Service.Stop`
- `servers.Service.Kill`
- `servers.Service.Restart`
- `servers.Service.finalizeInstance`
- Rediscovery nach GameNode-Neustart
- Stop-Timeout und Force-Kill
- Template Stopmodell
- Template Schema v1/v2
- Launch Resolver
- Provisioning Snapshot
- Serverpersistenz
- API/UI-Darstellung der Stopmethode
- Pelican/Pterodactyl Stop-Normalisierung

Verstehe insbesondere, warum `stdin_command: "^C"` kein echtes Ctrl+C erzeugt.

Keine bestehende Lifecycle-Architektur ersetzen.

## 2. Windows-Control-Event-Verhalten klären

Vor der Implementierung technisch prüfen:

- `GenerateConsoleCtrlEvent`
- `CTRL_C_EVENT`
- `CTRL_BREAK_EVENT`
- `CREATE_NEW_PROCESS_GROUP`
- Verhalten mit gemeinsamem Parent-Console
- Verhalten ohne Console
- Verhalten bei als Windows-Dienst gestartetem GameNode
- Verhalten bei mehreren gleichzeitig laufenden Servern
- Verhalten nach GameNode-Neustart und Process Rediscovery
- Verhalten für Child-Prozesse im Serverprozessbaum

Nicht annehmen, dass `CTRL_C_EVENT` sicher an eine einzelne Process Group adressiert werden kann.

Wenn `CTRL_BREAK_EVENT` die sicherere gezielte Variante ist, darf sie verwendet werden, aber nur wenn Satisfactory sie nachweislich wie Ctrl+C behandelt.

Keine Broadcast-Signale an Process Group 0.

## 3. Architekturentscheidung

Führe einen neuen kompilierten Stop-Typ ein, sinngemäß:

`console_interrupt`

Die genaue Benennung an bestehende Konventionen anpassen.

Das Template darf nur deklarieren:

- Stop-Typ
- Timeout

Das Template darf nicht deklarieren:

- Signalnummern
- Windows API Flags
- Control Characters
- Helper Executables
- Scripts
- freie Stop-Kommandos

Die Auswahl zwischen CTRL_C und CTRL_BREAK bleibt fest in der geprüften Windows-Runtime implementiert.

## 4. Runtime Interface

Erweitere die Runtime möglichst klein und eindeutig.

Mögliche Varianten:

- eine separate `Interrupt`-Methode
- oder ein typisierter Stop-Request

Nicht:

- freie String-Stopmethoden bis tief in die OS-Runtime durchreichen
- Templatewissen in `internal/runtime`
- Satisfactory-spezifische Bedingungen
- HTTP-/API-Typen im Runtime-Package

Die Runtime muss weiterhin nur Prozess- und OS-Lifecycle kennen.

## 5. Prozessstart

Ein Server mit `console_interrupt` muss unter Windows so gestartet werden, dass später ein Control Event ausschließlich seine Process Group erreicht.

Prüfe eine kontrollierte Verwendung von:

`CREATE_NEW_PROCESS_GROUP`

Die Process Group muss eindeutig zum gestarteten Prozess gehören.

Vor jeder Signalzustellung:

- PID prüfen
- StartKey prüfen
- aktuelle Prozessidentität prüfen
- sicherstellen, dass keine neuere Serverinstanz existiert

Bestehende `terminate`- und `stdin_command`-Server dürfen durch neue Creation Flags nicht unbeabsichtigt verändert werden.

Falls nur interruptfähige Server spezielle Startoptionen benötigen, diese Eigenschaft typisiert aus dem normalisierten Stopmodell ableiten.

## 6. Servicefähigkeit

Die Implementierung muss nicht nur in einem interaktiven Terminal funktionieren.

Prüfe mindestens:

- GameNode aus PowerShell/CMD gestartet
- GameNode ohne sichtbares Terminal gestartet
- GameNode als Windows-Dienst oder vergleichbarer Session-0-Prozess
- GameNode mit mehreren parallelen Serverprozessen

Wenn `GenerateConsoleCtrlEvent` ohne vorhandene gemeinsame Console nicht robust funktioniert:

- keine halbfertige Lösung veröffentlichen
- prüfen, ob eine kleine fest kompilierte Supervisor-/Console-Host-Lösung innerhalb der bestehenden GameNode-Binary möglich ist
- keinen externen Script- oder Shell-Helper erzeugen
- keine zweite dauerhafte Servicearchitektur einführen

Ein Supervisor darf nur umgesetzt werden, wenn Prozessidentität, Output-Pipes, stdin, Exitcode, Kill, Restart und Rediscovery sauber erhalten bleiben.

Falls eine servicefähige Lösung im aktuellen Runtime-Modell fundamental nicht sicher möglich ist, Turn als blockiert abschließen und die konkrete Architekturblockade dokumentieren.

## 7. Stop-Ablauf

Für `console_interrupt`:

1. Server Lock und Generation prüfen
2. PID plus StartKey verifizieren
3. Serverzustand auf `stopping` setzen
4. gezieltes Windows Console Event senden
5. auf den normalen Prozess-Exit warten
6. Exit ausschließlich über `finalizeInstance` abschließen
7. bei Timeout den bestehenden sicheren Kill-Pfad verwenden
8. Force-Kill-Nutzung kontrolliert erfassen, ohne sensible Daten

Nicht direkt nach dem Interrupt `taskkill` aufrufen.

Ein erfolgreicher API-Aufruf zum Senden des Events ist noch kein erfolgreicher graceful Shutdown. Entscheidend ist, ob der Prozess vor dem Timeout selbst beendet wird.

## 8. Rediscovered Processes

Definiere das Verhalten nach einem GameNode-Neustart.

GameNode kann bestehende Pipes und Console-Zuordnung nicht automatisch wiederherstellen.

Wenn ein rediscovered Prozess keinen sicher adressierbaren Console Interrupt mehr erhalten kann:

- nicht behaupten, graceful stoppen zu können
- kontrolliert auf den bestehenden `terminate`-/Kill-Lifecycle zurückfallen
- UI/API mit einer sicheren, verständlichen Einschränkung versehen
- keine fremde Console attachen, wenn dadurch GameNode oder andere Prozesse gefährdet werden

Keine falsche Console-Reattachment behaupten.

## 9. Template Contract

Erweitere Official Template Schema v2 um den neuen whitelisted Stop-Typ.

Erlaubte Stoptypen danach sinngemäß:

- `terminate`
- `stdin_command`
- `console_interrupt`

Validierung:

- `console_interrupt` darf kein `stop_command` besitzen
- Timeout muss weiterhin bounded sein
- Unsupported Platforms müssen einen stabilen Fehler liefern
- unbekannte Stoptypen ablehnen
- Schema und Backend müssen identische Inputs akzeptieren
- v1 Templates bleiben lesbar
- bestehende Server ändern sich nicht automatisch

Stable Error Codes sinngemäß:

- `TEMPLATE_UNSUPPORTED_STOP_METHOD`
- `RUNTIME_CONSOLE_INTERRUPT_UNSUPPORTED`
- `RUNTIME_CONSOLE_INTERRUPT_FAILED`

Vorhandene Code-Konventionen bevorzugen.

Keine OS-Rohfehler oder Prozessdetails unkontrolliert an API/Audit weitergeben.

## 10. Satisfactory Template

Erst nach funktionierender Runtime-Unterstützung:

- Satisfactory Templateversion von `1.0.0` auf `1.1.0` erhöhen
- Catalog-Version entsprechend erhöhen
- Windows Stopmethode auf `console_interrupt` ändern
- Timeout sinnvoll bounded lassen
- Help, Compatibility Findings und README aktualisieren

Bestehende provisionierte Satisfactory-Server behalten ihre gepinnte Version und `terminate`.

Neue Server verwenden `console_interrupt`.

Satisfactory nur dann von `partially_compatible` auf `compatible` setzen, wenn ein realer Windows-Test beweist:

- Control Event wurde gezielt gesendet
- Server beendete sich selbst vor dem Timeout
- Force-Kill wurde nicht verwendet
- Save-/Shutdown-Logs zeigen normalen Shutdown
- Restart danach funktioniert

Ohne Realtest:

- Stopmethode kann implementiert sein
- Compatibility bleibt konservativ
- Realworld Acceptance bleibt pending

## 11. Imported Eggs

Pelican/Pterodactyl `^C` nicht automatisch global auf `console_interrupt` abbilden.

Zuerst prüfen:

- ob der Importer `^C` aktuell als stdin command interpretiert
- ob bestehende Findings dadurch entstehen
- ob Plattform und Serverprozess den Interrupt tatsächlich unterstützen

In diesem Turn bevorzugt:

- Official Satisfactory verwendet den geprüften Stop-Typ
- importierte Eggs behalten konservative Findings
- keine pauschale Migration aller `^C`-Eggs

Eine spätere sichere Normalisierung kann ein eigener Milestone sein.

## 12. Runtime Tests

Backendtests mindestens:

### Windows Start

- interruptfähiger Prozess erhält eigene Process Group
- normaler `terminate`-Prozess behält bestehendes Verhalten
- stdin/stdout/stderr funktionieren weiterhin
- Prozessidentität enthält gültigen StartKey

### Interrupt

- gezielter Helper-Prozess empfängt Control Event
- Helper beendet sich selbst
- kein Force-Kill
- Exit wird genau einmal finalisiert
- Stopzustand wird korrekt persistiert
- Restart nach Interrupt funktioniert

### Isolation

- zwei parallele Helper-Prozesse
- Interrupt an Server A beendet nicht Server B
- falsche PID abgelehnt
- falscher StartKey abgelehnt
- stale Instance kann neue Instance nicht stoppen
- Process Group 0 wird niemals verwendet

### Timeout

- Helper ignoriert Control Event
- Timeout läuft bounded ab
- Force-Kill erfolgt
- Prozessbaum wird beendet
- Finalisierung bleibt genau einmal

### Context

- abgebrochener Context
- bereits beendeter Prozess
- Prozess endet während der Signalzustellung
- Signal API schlägt fehl
- Kill findet Prozess bereits beendet

### Rediscovery

- rediscovered Prozess wird nicht als console-attached dargestellt
- unsicherer Interrupt fällt kontrolliert zurück oder liefert den definierten sicheren Fehler
- kein erfundenes Reattachment

## 13. Windows Helper Test

Implementiere einen kleinen Test-Helper innerhalb der Go-Testbinary.

Der Helper soll:

- einen Windows Console Handler registrieren
- das tatsächlich empfangene Event kontrolliert melden
- bei erwarteter Interrupt-Art sauber exitieren
- optional das Event ignorieren, um Timeout/Fallback zu testen
- keine externe Binary oder Scriptdatei benötigen

Tests müssen überspringen, wenn die Umgebung eine notwendige Windows-Console-Funktion nachweislich nicht bereitstellt. Ein Skip ist aber kein Acceptance-Nachweis.

Keine Tests dürfen Ctrl+C an die gesamte Test- oder GameNode-Console broadcasten.

## 14. Server Lifecycle Tests

Erweitere `internal/servers`:

- Validierung von `console_interrupt`
- Start reicht notwendige Interruptfähigkeit an Runtime weiter
- Stop ruft den neuen Runtime-Pfad auf
- Kill bleibt unverändert sofort
- Restart wartet auf vollständige Finalisierung
- manueller Stop gilt nicht als Crash
- Auto-Restart wird beim manuellen Stop nicht ausgelöst
- Timeout-Fallback verwendet normalen Kill
- kein doppeltes Audit-/Lifecycle-Ereignis

## 15. Template- und Golden-Tests

Satisfactory Golden Assertions:

- ID `satisfactory`
- Schema v2
- Version `1.1.0`
- Windows-only
- App ID `1690800`
- `FactoryServer.exe`
- strukturierte Argumente unverändert
- drei Portzeilen unverändert
- `stop_method = console_interrupt`
- kein Stopcommand
- Catalog-Metadaten stimmen exakt
- Linux bleibt unsupported

Allgemeine Tests:

- unbekannter Stoptyp abgelehnt
- `console_interrupt` mit Stopcommand abgelehnt
- `stdin_command` ohne Command abgelehnt
- v1 Templates bleiben kompatibel
- vorhandene 7DTD/PZ/Palworld/Eco/NeoForge Templates bleiben gültig

## 16. API und UI

Keine große UI-Neugestaltung.

UI soll Stopverhalten verständlich darstellen:

- `Console interrupt`
- Timeout
- Force-kill fallback

Nicht behaupten:

- „graceful“, solange kein realer Acceptance-Nachweis besteht
- dass rediscovered Prozesse weiterhin eine Console besitzen

Fehlertexte beispielsweise:

- „Windows console interrupt is unavailable for this process“
- „The server did not exit after the console interrupt and was force-stopped“
- „The process was rediscovered without a controllable console“

Keine Windows Handles, PIDs oder Rohfehler unnötig an normale Benutzer ausgeben.

## 17. Audit und Logging

Audit darf enthalten:

- Stopmethode
- kontrolliertes Ergebnis
- Timeout/Fallback verwendet: ja/nein

Audit darf nicht enthalten:

- vollständige Argumente
- Environment
- Console-Inhalt
- Rohoutput
- Secrets
- Windows Handlewerte

Logs dürfen für Diagnose server ID und kontrollierte Lifecycle-Ergebnisse enthalten, aber keine sensitiven Prozessdaten.

## 18. Dokumentation

Aktualisiere:

- `docs/runtime.md`
- `docs/templates.md`
- `docs/security.md`
- `docs/architecture.md`
- `templates/README.md`
- `templates/steamcmd/satisfactory/README.md`
- bei Bedarf `AGENTS.md`

Dokumentiere:

- Unterschied zwischen stdin Text und Windows Console Event
- warum `^C` kein normaler Stopcommand ist
- `console_interrupt` als kompilierte Runtime-Funktion
- Windows Process Group Isolation
- Timeout/Force-Kill
- Verhalten nach Rediscovery
- Service-/Session-0-Einschränkungen
- keine automatische Egg-`^C`-Normalisierung

## 19. Security Review

Explizit prüfen:

- kein Shell-Aufruf
- keine Template-definierten Signalnummern
- kein Control Event an Process Group 0
- kein Signal an GameNode selbst
- kein Signal an andere Server
- PID-Reuse durch StartKey verhindert
- keine stale Instance stoppt neuere Instance
- kein Handle Leak
- keine unbounded Waits
- keine Secrets in Logs/Audit/API
- Force-Kill bleibt bounded
- kein neues beliebiges Runtime-Kommando
- Windows-spezifische Implementierung bleibt hinter Runtime-Grenze

## 20. Realer Windows Acceptance-Test

Nur nach erfolgreichen Unit-/Integration-Tests.

Mit einem echten, neu provisionierten Satisfactory Server:

1. Windows GameNode starten
2. Satisfactory Official Template `1.1.0` provisionieren
3. Server claimen und Testwelt starten
4. auf stabilen Running-Zustand warten
5. GameNode Stop auslösen
6. beobachten, welches Console Event gesendet wurde
7. prüfen, dass Satisfactory selbst vor Timeout beendet
8. prüfen, dass kein `/F` oder `TerminateProcess` verwendet wurde
9. Logs auf normalen Save-/Shutdown-Ablauf prüfen
10. Server erneut starten
11. Welt/Save laden
12. erneut stoppen

Dokumentieren:

- GameNode Startmodus: interaktiv oder Windows-Dienst
- Eventtyp
- Prozessgruppe
- Stopdauer
- Force-Kill verwendet: ja/nein
- Exitcode
- beobachteter Save-/Shutdown-Hinweis
- Restart erfolgreich: ja/nein

Ein erfolgreicher Helper-Test ersetzt diesen Realtest nicht.

## 21. Full Verification

Backend:

- gofmt für geänderte Go-Dateien
- go vet ./...
- go test ./...
- go build ./...
- relevante Windows Native Tests
- Race Tests, soweit CGO verfügbar

Frontend:

- npm run check
- npm run test:helpers
- npm run build

Builds:

- Windows amd64
- Linux amd64, um Interface-/Stub-Kompilierung zu prüfen

Zusätzlich:

- Official Template Validation
- Satisfactory Golden Test
- git diff --check
- keine temporären Testartefakte im Commit
- neue gehashte Webassets gemeinsam mit entfernten alten Assets stagen

## 22. Abschlussbericht

Berichte:

- vorheriges Windows Stopverhalten
- neue Runtime-Architektur
- verwendeter Eventtyp und Begründung
- Process Group Isolation
- Verhalten ohne Console
- Verhalten als Windows-Dienst
- Rediscovery-Verhalten
- Timeout und Force-Kill
- Template-Schemaänderung
- Satisfactory Versionsänderung
- Imported-Egg-Auswirkung
- Tests
- Windows Helper Acceptance
- echter Satisfactory Stoptest
- Save-/Restart-Ergebnis
- Builds
- Race-Test
- bekannte Einschränkungen
- Migrationen oder „keine“

Finaler Status nur entsprechend tatsächlichem Ergebnis:

Wenn Code und Tests fertig, aber kein realer Satisfactory-Test:

WINDOWS_CONSOLE_INTERRUPT_IMPLEMENTATION_COMPLETE
SATISFACTORY_GRACEFUL_STOP_ACCEPTANCE_PENDING

Wenn echter Satisfactory-Stop ohne Force-Kill inklusive Restart erfolgreich:

WINDOWS_CONSOLE_INTERRUPT_IMPLEMENTATION_COMPLETE
SATISFACTORY_GRACEFUL_STOP_REALWORLD_VERIFIED

Wenn eine sichere servicefähige, prozessisolierte Implementierung nicht möglich ist:

WINDOWS_CONSOLE_INTERRUPT_IMPLEMENTATION_BLOCKED

Nicht `VERIFIED` oder `ACCEPTED` schreiben, wenn nur Helper-/Unit-Tests gelaufen sind.