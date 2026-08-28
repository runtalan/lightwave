#!/usr/bin/env bash
# Builds the Lightwave Stream Deck plugin and optionally installs it.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")" && pwd)"
BUNDLE="$ROOT/com.dinksf.lightwave.sdPlugin"

cd "$ROOT/plugin"

os="$(uname -s)"
if [[ "$os" == Darwin ]]; then
  DEST="$HOME/Library/Application Support/com.elgato.StreamDeck/Plugins/com.dinksf.lightwave.sdPlugin"
  # Universal so the plugin runs on Intel and Apple Silicon alike.
  GOOS=darwin GOARCH=arm64 go build -trimpath -o "$ROOT/.build-arm64" .
  GOOS=darwin GOARCH=amd64 go build -trimpath -o "$ROOT/.build-amd64" .
  lipo -create -output "$BUNDLE/bin/lightwave-sd" "$ROOT/.build-arm64" "$ROOT/.build-amd64"
  rm -f "$ROOT/.build-arm64" "$ROOT/.build-amd64"
  chmod +x "$BUNDLE/bin/lightwave-sd"
  codesign --force --sign - "$BUNDLE/bin/lightwave-sd"
  xattr -cr "$BUNDLE" || true
  BIN="$BUNDLE/bin/lightwave-sd"
else
  echo "error: use streamdeck/build.ps1 on Windows" >&2
  exit 1
fi

if [[ "${1:-}" == "--install" ]]; then
  osascript -e 'quit app "Elgato Stream Deck"' 2>/dev/null || true
  sleep 2
  rm -rf "$DEST"
  mkdir -p "$(dirname "$DEST")"
  cp -R "$BUNDLE" "$DEST"
  echo "Installed to $DEST"
  echo "Profile: $ROOT/Lightwave.streamDeckProfile (double-click to import)"
  open -a "Elgato Stream Deck" 2>/dev/null || true
  echo "Stream Deck relaunched."
fi
echo "Built $BIN"
if [[ "$os" == Darwin ]]; then
  lipo -info "$BIN"
fi
