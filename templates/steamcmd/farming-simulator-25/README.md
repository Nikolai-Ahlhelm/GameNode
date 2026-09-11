# Farming Simulator 25 — GameNode template

This schema-v2 Official template adopts an existing Windows installation and
launches `dedicatedServer.exe` directly. Farming Simulator 25 does not expose
a separate anonymous SteamCMD dedicated-server package in the reviewed setup,
so GameNode does not pretend to install it with its fixed anonymous SteamCMD
workflow.

The server directory must contain the game installation and
`dedicatedServer.exe`. GameNode verifies that executable inside the selected
root, then manages the launcher as a normal native server. The launcher owns
the Farming Simulator web administration and game-server process; GameNode does
not execute a shell, rewrite `dedicatedServer.xml`, or claim integration with
the child game process.

The default multiplayer ports are UDP `10823`, `10824`, and `10825`, based on
the GIANTS support guidance. The web administration ports are configured by
Farming Simulator and remain outside the GameNode port declaration.

An additional Farming Simulator license/account is required for a dedicated
server. Steam installations may require a `steam_appid.txt` file containing
`2300320` beside the launcher.

Reviewed references:

- <https://www.farming-simulator.com/support.php?articleId=51&categoryId=4>
- <https://forum.giants-software.com/viewtopic.php?t=209019>
- <https://store.steampowered.com/app/2300320/Farming_Simulator_25/>
