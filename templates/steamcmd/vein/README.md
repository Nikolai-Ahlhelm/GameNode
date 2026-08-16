# Vein Dedicated Server — GameNode template

This schema-v2 Official template installs Steam App ID `2131400` anonymously
and launches the upstream `VeinServer-Win64-Test.exe` directly on Windows. No
batch file or shell wrapper is used. Only a 64-bit Windows build is published
upstream, so Linux is not advertised.

The reviewed launch is deliberately limited to the arguments upstream
documents having a verified effect: `-log`, `-port`, `-QueryPort`, and
`-MaxPlayers`. The query port cannot be set between `27020` and `27050`
because Steam reserves that range locally; this is documented as guidance
only and is not enforced by validation.

There is no GameNode configuration adapter in this version. Upstream does not
document a dedicated server configuration file (INI/JSON), admin/whitelist
files, or save management, so none of that is exposed as template variables.
Server console commands and backup procedures are marked "to be determined"
by the upstream wiki as of this writing.

Stop is a known limitation. Upstream does not document a console command,
RCON command, or signal for graceful shutdown, so this template uses
GameNode's ordinary native terminate lifecycle and does not claim a save
guarantee. Upstream also references an optional TCP RCON listener on the
game port without documenting how to enable or authenticate it; this
template registers that port for documentation purposes only and does not
configure or use RCON.

The host must independently have the Visual C++ 2012/2013/2015-2022
redistributables and the DirectX End-User Runtimes installed, and at least
8 GB of RAM free, per upstream requirements. GameNode does not install these
prerequisites.

Upstream reference used for the reviewed contract:

- <https://vein.wiki.gg/wiki/Vein_Dedicated_Server_Setup>
