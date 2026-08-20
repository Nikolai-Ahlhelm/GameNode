[CmdletBinding()]
param(
    [string]$Config = (Join-Path $PSScriptRoot 'config.yaml')
)

$ErrorActionPreference = 'Stop'
$projectRoot = $PSScriptRoot
$webRoot = Join-Path $projectRoot 'web'

foreach ($command in @('go', 'npm.cmd')) {
    if (-not (Get-Command $command -ErrorAction SilentlyContinue)) {
        throw "Das benoetigte Programm '$command' wurde nicht gefunden."
    }
}

if (-not (Test-Path (Join-Path $webRoot 'node_modules'))) {
    Write-Host 'Frontend-Abhaengigkeiten werden installiert...' -ForegroundColor Cyan
    Push-Location $webRoot
    try {
        npm.cmd ci
        if ($LASTEXITCODE -ne 0) {
            throw "npm ci ist mit Exit-Code $LASTEXITCODE fehlgeschlagen."
        }
    }
    finally {
        Pop-Location
    }
}

$escapedRoot = $projectRoot.Replace("'", "''")
$escapedWeb = $webRoot.Replace("'", "''")
$escapedConfig = ([System.IO.Path]::GetFullPath($Config)).Replace("'", "''")

Write-Host 'Starte GameNode-Backend auf http://127.0.0.1:8443 ...' -ForegroundColor Cyan
Start-Process powershell.exe -ArgumentList '-NoProfile', '-NoExit', '-Command', "Set-Location -LiteralPath '$escapedRoot'; go run ./cmd/gamenode -config '$escapedConfig' -dev"

Write-Host 'Starte Vite-Frontend auf http://127.0.0.1:5173 ...' -ForegroundColor Cyan
Start-Process powershell.exe -ArgumentList '-NoProfile', '-NoExit', '-Command', "Set-Location -LiteralPath '$escapedWeb'; npm.cmd run dev"

Write-Host "`nDie Entwicklungsumgebung wird in zwei Terminals ausgefuehrt." -ForegroundColor Green
Write-Host 'Frontend: http://127.0.0.1:5173'
Write-Host 'Backend:  http://127.0.0.1:8443'
Write-Host 'Dev-Login: Benutzer dev, Passwort dev (nur mit -dev)' -ForegroundColor Yellow
Write-Host "Konfiguration: $([System.IO.Path]::GetFullPath($Config))"
