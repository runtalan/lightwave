# One-time setup for the Tapo Plug key: a Python environment with python-kasa,
# which the Stream Deck plugin runs to talk to the plugs. Safe to re-run.
# Usage: .\setup-tapo.ps1
$ErrorActionPreference = "Stop"
$Venv = Join-Path $env:APPDATA "Lightwave\tapo-venv"
New-Item -ItemType Directory -Force -Path (Split-Path $Venv) | Out-Null

# python-kasa needs Python 3.11+.
if (Get-Command uv -ErrorAction SilentlyContinue) {
    uv venv --python 3.12 --allow-existing $Venv
    uv pip install --python (Join-Path $Venv "Scripts\python.exe") --upgrade python-kasa
} else {
    py -3.12 -m venv $Venv
    & (Join-Path $Venv "Scripts\python.exe") -m pip install --upgrade python-kasa
}
Write-Host "Tapo support ready: $Venv"
Write-Host "Add a 'Tapo Plug' key in Stream Deck, then enter your TP-Link login in its inspector."
