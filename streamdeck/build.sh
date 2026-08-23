#!/usr/bin/env bash
# Builds the Lightwave Stream Deck plugin and installs it for the current user.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")" && pwd)"
BUNDLE="$ROOT/com.dinksf.lightwave.sdPlugin"
DEST="$HOME/Library/Application Support/com.elgato.StreamDeck/Plugins/com.dinksf.lightwave.sdPlugin"

cd "$ROOT/plugin"
# Universal so the plugin runs on Intel and Apple Silicon alike.
GOOS=darwin GOARCH=arm64 go build -trimpath -o "$ROOT/.build-arm64" .
GOOS=darwin GOARCH=amd64 go build -trimpath -o "$ROOT/.build-amd64" .
lipo -create -output "$BUNDLE/bin/lightwave-sd" "$ROOT/.build-arm64" "$ROOT/.build-amd64"
rm -f "$ROOT/.build-arm64" "$ROOT/.build-amd64"
chmod +x "$BUNDLE/bin/lightwave-sd"

# Ad-hoc sign so Gatekeeper does not kill the unsigned binary on launch.
codesign --force --sign - "$BUNDLE/bin/lightwave-sd"
xattr -cr "$BUNDLE" || true

if [[ "${1:-}" == "--install" ]]; then
  # Stream Deck must not hold the old binary open while it is replaced.
  osascript -e 'quit app "Elgato Stream Deck"' 2>/dev/null || true
  sleep 2
  rm -rf "$DEST"
  mkdir -p "$(dirname "$DEST")"
  cp -R "$BUNDLE" "$DEST"
  echo "Installed to $DEST"
  open -a "Elgato Stream Deck" 2>/dev/null || true
  echo "Stream Deck relaunched."
fi
echo "Built $BUNDLE/bin/lightwave-sd"
lipo -info "$BUNDLE/bin/lightwave-sd"
