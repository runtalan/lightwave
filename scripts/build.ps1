# Builds Lightwave on Windows. Requires Go, Node, and Wails
# (https://wails.io) on PATH. Run from a Developer PowerShell or any
# shell that can compile CGO (TDM-GCC / MinGW, as Wails documents).
#
# Usage: .\scripts\build.ps1
param(
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]]$WailsArgs
)

$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
Set-Location $Root

wails build @WailsArgs

$Exe = Join-Path $Root "build\bin\lightwave.exe"
if (-not (Test-Path $Exe)) {
    Write-Error "wails build did not produce $Exe"
}

# The installed exe does not use the project cwd. Ship .env next to it so
# cloud discovery still works. The key is not printed.
$EnvFile = Join-Path $Root ".env"
if (Test-Path $EnvFile) {
    Copy-Item $EnvFile (Join-Path (Split-Path $Exe) ".env")
}

Write-Host "Built $Exe"
Write-Host "Launch: $Exe"
