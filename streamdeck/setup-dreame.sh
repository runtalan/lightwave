#!/usr/bin/env bash
# One-time setup for Dreame vacuum control: a Python environment, plus the
# Dreame cloud client from Tasshack/dreame-vacuum (MIT), fetched at a pinned
# commit. Dreame has no public API; that project reverse-engineers the one the
# Dreamehome app uses, and keeps it working. Safe to re-run.
set -euo pipefail

# The commit the files below are taken from. Move it deliberately, not
# implicitly: the protocol code is reverse-engineered and changes upstream.
SHA="f5b8472b8e2e4bb3dff7bc2a81a5fd594eca9585"
RAW="https://raw.githubusercontent.com/Tasshack/dreame-vacuum/$SHA"

if [[ "$(uname -s)" == Darwin ]]; then
  BASE="$HOME/Library/Application Support/Lightwave/dreame"
else
  echo "error: macOS only for now" >&2
  exit 1
fi
VENV="$BASE/venv"
PKG="$BASE/vendor/dreame"
mkdir -p "$PKG"

# Only the cloud protocol is needed. The rest of the integration (maps, the
# Home Assistant glue) pulls in numpy and a JavaScript engine for nothing here.
for f in protocol.py exceptions.py types.py; do
  curl -fsSL "$RAW/custom_components/dreame_vacuum/dreame/$f" -o "$PKG/$f"
done
curl -fsSL "$RAW/LICENSE" -o "$BASE/vendor/LICENSE.dreame-vacuum"
: > "$PKG/__init__.py"   # not the upstream one, which imports the whole device layer

if command -v uv >/dev/null 2>&1; then
  uv venv --python 3.12 --allow-existing "$VENV"
  uv pip install --python "$VENV/bin/python" --upgrade requests paho-mqtt pycryptodome cryptography python-miio
else
  PY=""
  for c in python3.13 python3.12 python3.11 python3; do
    if command -v "$c" >/dev/null 2>&1 && "$c" -c 'import sys; sys.exit(sys.version_info < (3, 11))'; then
      PY="$c"; break
    fi
  done
  [[ -n "$PY" ]] || { echo "error: need Python 3.11+ (or uv: brew install uv)" >&2; exit 1; }
  "$PY" -m venv "$VENV"
  "$VENV/bin/python" -m pip install --upgrade requests paho-mqtt pycryptodome cryptography python-miio
fi

PYTHONPATH="$BASE/vendor" "$VENV/bin/python" -c 'from dreame.protocol import DreameVacuumDreameHomeCloudProtocol; print("Dreame client ready")'
echo "Installed to $BASE"
echo "Next: $VENV/bin/python streamdeck/dreame_probe.py"
