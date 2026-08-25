# Builds the Lightwave Stream Deck plugin and optionally installs it.
# Usage: .\build.ps1 [-Install]
param(
    [switch]$Install
)

$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $MyInvocation.MyCommand.Path
$Bundle = Join-Path $Root "com.dinksf.lightwave.sdPlugin"
$BinDir = Join-Path $Bundle "bin"
$Dest = Join-Path $env:APPDATA "Elgato\StreamDeck\Plugins\com.dinksf.lightwave.sdPlugin"

New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
Push-Location (Join-Path $Root "plugin")
try {
    $env:GOOS = "windows"
    go build -trimpath -o (Join-Path $BinDir "lightwave-sd.exe") .
} finally {
    Pop-Location
}

if ($Install) {
    Stop-Process -Name "StreamDeck" -ErrorAction SilentlyContinue
    Start-Sleep -Seconds 2
    if (Test-Path $Dest) {
        Remove-Item -Recurse -Force $Dest
    }
    New-Item -ItemType Directory -Force -Path (Split-Path $Dest) | Out-Null
    Copy-Item -Recurse $Bundle $Dest
    Write-Host "Installed to $Dest"
    $Profile = Join-Path $Root "Lightwave.streamDeckProfile"
    if (Test-Path $Profile) {
        Copy-Item $Profile (Join-Path $Dest "Lightwave.streamDeckProfile") -Force
        Write-Host "Profile copied. Double-click to import:"
        Write-Host "  $Profile"
    }
    $sd = Join-Path ${env:ProgramFiles} "Elgato\StreamDeck\StreamDeck.exe"
    if (Test-Path $sd) {
        Start-Process $sd
        Write-Host "Stream Deck relaunched."
    }
}

Write-Host "Built $BinDir\lightwave-sd.exe"
