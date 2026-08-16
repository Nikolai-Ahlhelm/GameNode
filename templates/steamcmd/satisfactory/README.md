# Satisfactory Dedicated Server — GameNode template

This schema-v2 Official template installs Steam App ID `1690800` anonymously
and currently supports a reviewed native Windows launch.

Windows uses the upstream `FactoryServer.exe` entry point. Linux is deliberately
not advertised by this first template version; no shell wrapper is executed.

The template exposes only settings with a verified direct effect: primary port,
reliable messaging port, maximum players, and optional experimental branch.
Server claim, name, administrator password, and player password live in
Satisfactory's server settings save and must be configured through the in-game
Server Manager. They are deliberately not ineffective Wizard variables.

There is no GameConfig adapter in this version. Advanced INI files are generated
after runtime use and use sectioned Unreal configuration, while the authoritative
server settings are stored in `ServerSettings.PORT.sav`. GameNode does not edit
that binary file or call the HTTPS management API.

Stop now uses GameNode's compiled `console_interrupt` runtime stop type
(version `1.1.0`), matching the interactive Ctrl-C path Satisfactory documents
on Windows. GameNode delivers a targeted Windows console control event
(`CTRL_BREAK_EVENT`) scoped to `FactoryServer.exe`'s own process group — never
a broadcast to the whole console, never stdin text, and never GameNode's
`taskkill`-based terminate path directly. If the process does not exit before
the configured timeout, GameNode falls back to its existing bounded
force-kill path, exactly as it does for every other stop method.

A server rediscovered after a GameNode restart cannot receive a safely
addressed console interrupt (its original console association is gone), so
that one stop falls back to GameNode's terminate lifecycle instead; this is a
documented, controlled limitation, not a defect.

Compatibility stays `partially_compatible` until a real Windows dedicated
server has been observed exiting before the timeout, without a force-kill,
through this stop path; see `compatibility.findings` in `template.json`.

Upstream references used for the reviewed contract:

- <https://satisfactory.wiki.gg/wiki/Dedicated_servers>
- <https://satisfactory.wiki.gg/wiki/Dedicated_servers/Configuration_files>
