#!/usr/bin/env bash
# One-time setup for the Tapo Plug key: a Python environment with python-kasa,
# which the Stream Deck plugin runs to talk to the plugs.
#
# Lives next to Lightwave's own data (not inside the plugin) so reinstalling the
# plugin does not throw it away. Safe to re-run.
set -euo pipefail

# Released python-kasa cannot log in to Tapo plugs on current firmware (the TPAP
# protocol), so this installs a fork that can, at a commit known to work: it
# reports a wrong password as such, with the attempts the plug has left, and
# logs in again when a plug drops the session. Every rejected login counts
# toward the plug locking itself, so change this only to a commit you have tried.
KASA="python-kasa @ git+https://github.com/ZeliardM/python-kasa.git@a1a6c3854ace705b21357b357354d4ae8b6307b4"

if [[ "$(uname -s)" == Darwin ]]; then
  VENV="$HOME/Library/Application Support/Lightwave/tapo-venv"
else
  echo "error: use streamdeck/setup-tapo.ps1 on Windows" >&2
  exit 1
fi
mkdir -p "$(dirname "$VENV")"

# python-kasa needs a recent Python (3.11+), newer than macOS's own.
if command -v uv >/dev/null 2>&1; then
  uv venv --python 3.12 --allow-existing "$VENV"
  uv pip install --python "$VENV/bin/python" "$KASA"
else
  PY=""
  for c in python3.13 python3.12 python3.11 python3; do
    if command -v "$c" >/dev/null 2>&1 && "$c" -c 'import sys; sys.exit(sys.version_info < (3, 11))'; then
      PY="$c"; break
    fi
  done
  if [[ -z "$PY" ]]; then
    echo "error: need Python 3.11+ (or uv: brew install uv)" >&2
    exit 1
  fi
  "$PY" -m venv "$VENV"
  "$VENV/bin/python" -m pip install "$KASA"
fi

"$VENV/bin/python" -c 'import kasa; print("python-kasa", kasa.__version__ if hasattr(kasa, "__version__") else "installed")'
echo "Tapo support ready: $VENV"
echo "Add a 'Tapo Plug' key in Stream Deck, then enter your TP-Link login in its inspector."
