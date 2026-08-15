# Eco Dedicated Server

This schema-v2 template installs SteamCMD App ID `739590` anonymously and
launches `EcoServer.exe` on Windows or `EcoServer` on Linux as a direct native
process. No wrapper script or shell is used.

The reviewed launch is deliberately limited to `--nogui -offline`. Current Eco
online servers require an SLG user token or account credentials on the process
command line. GameNode does not put secrets into argv, so the template must not
offer a token/password variable until Eco provides a credential channel that is
compatible with GameNode's secret-handling boundary. Eco clients must also be
offline to connect to this server mode.

The template reserves Eco's documented defaults: UDP `3000` for game traffic
and TCP `3001` for the web interface. `Configs/Network.eco` is generated on the
first start and is not currently modified by GameNode. Linux installations may
require the host-provided `libgdiplus` package.

SteamCMD is used for initial installation only. Automatic updates, online
authentication, RCON management, mod management, firewall changes, and backups
remain outside this template.
