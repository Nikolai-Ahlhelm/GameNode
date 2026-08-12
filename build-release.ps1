[CmdletBinding()]
param(
    [Parameter(Position = 0)]
    [string]$Version
)

$ErrorActionPreference = 'Stop'
$projectRoot = $PSScriptRoot

if ([string]::IsNullOrWhiteSpace($Version)) {
    $Version = Read-Host 'Release-Version eingeben (z. B. 0.1.0)'
}

$Version = $Version.Trim()
if ($Version -notmatch '^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$') {
    throw "Ungueltige Version '$Version'. Erwartet wird eine semantische Version wie 0.1.0 oder 0.1.0-beta.1."
}

function Invoke-CheckedCommand {
    param(
        [Parameter(Mandatory)]
        [string]$Name,

        [Parameter(Mandatory)]
        [scriptblock]$Command
    )

    Write-Host "`n==> $Name" -ForegroundColor Cyan
    & $Command
    if ($LASTEXITCODE -ne 0) {
        throw "$Name ist mit Exit-Code $LASTEXITCODE fehlgeschlagen."
    }
}

foreach ($command in @('go', 'npm')) {
    if (-not (Get-Command $command -ErrorAction SilentlyContinue)) {
        throw "Das benoetigte Programm '$command' wurde nicht gefunden."
    }
}

$outputDirectory = Join-Path $projectRoot (Join-Path 'Builds' $Version)
New-Item -ItemType Directory -Path $outputDirectory -Force | Out-Null

$commit = 'unknown'
if (Get-Command git -ErrorAction SilentlyContinue) {
    $gitCommit = (& git -C $projectRoot rev-parse HEAD 2>$null)
    if ($LASTEXITCODE -eq 0 -and $gitCommit) {
        $commit = $gitCommit.Trim()
    }
}

$buildTime = [DateTime]::UtcNow.ToString('yyyy-MM-ddTHH:mm:ssZ')
$ldflags = "-s -w -X gamenode/internal/diagnostics.Version=$Version -X gamenode/internal/diagnostics.Commit=$commit -X gamenode/internal/diagnostics.BuildTime=$buildTime"

Push-Location $projectRoot
$previousGoOs = $env:GOOS
$previousGoArch = $env:GOARCH
$previousCgoEnabled = $env:CGO_ENABLED

try {
    Push-Location (Join-Path $projectRoot 'web')
    try {
        Invoke-CheckedCommand 'Frontend-Abhaengigkeiten installieren' { npm ci }
        Invoke-CheckedCommand 'Frontend fuer Produktion bauen' { npm run build }
    }
    finally {
        Pop-Location
    }

    $env:CGO_ENABLED = '0'
    $env:GOARCH = 'amd64'

    $env:GOOS = 'windows'
    $windowsBinary = Join-Path $outputDirectory 'gamenode-windows-amd64.exe'
    Invoke-CheckedCommand 'Windows-amd64-Release bauen' {
        go build -trimpath -ldflags $ldflags -o $windowsBinary ./cmd/gamenode
    }

    $env:GOOS = 'linux'
    $linuxBinary = Join-Path $outputDirectory 'gamenode-linux-amd64'
    Invoke-CheckedCommand 'Linux-amd64-Release bauen' {
        go build -trimpath -ldflags $ldflags -o $linuxBinary ./cmd/gamenode
    }

    $checksumFile = Join-Path $outputDirectory 'SHA256SUMS.txt'
    $checksums = @(
        Get-FileHash -Algorithm SHA256 $windowsBinary
        Get-FileHash -Algorithm SHA256 $linuxBinary
    ) | ForEach-Object { '{0}  {1}' -f $_.Hash.ToLowerInvariant(), (Split-Path $_.Path -Leaf) }
    Set-Content -Path $checksumFile -Value $checksums -Encoding ascii

    Write-Host "`nRelease $Version wurde erfolgreich erstellt:" -ForegroundColor Green
    Write-Host $outputDirectory
}
finally {
    $env:GOOS = $previousGoOs
    $env:GOARCH = $previousGoArch
    $env:CGO_ENABLED = $previousCgoEnabled
    Pop-Location
}
