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

Stop remains a known limitation. Satisfactory documents HTTPS shutdown or
an interactive Ctrl-C as graceful paths on Windows, while GameNode currently
provides its ordinary native terminate lifecycle. Do not describe it as graceful.

Upstream references used for the reviewed contract:

- <https://satisfactory.wiki.gg/wiki/Dedicated_servers>
- <https://satisfactory.wiki.gg/wiki/Dedicated_servers/Configuration_files>
