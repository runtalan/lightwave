#!/usr/bin/env bash
set -euo pipefail
root_dir=$(cd "$(dirname "$0")/.." && pwd)
plugin_dir="$root_dir/com.dinksf.govee-lightwave.sdPlugin"
mkdir -p "$plugin_dir/bin"
(cd "$root_dir/plugin" && GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o "$plugin_dir/bin/govee-lightwave-arm64" .)
(cd "$root_dir/plugin" && GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o "$plugin_dir/bin/govee-lightwave-amd64" .)
lipo -create -output "$plugin_dir/bin/govee-lightwave" "$plugin_dir/bin/govee-lightwave-arm64" "$plugin_dir/bin/govee-lightwave-amd64"
rm "$plugin_dir/bin/govee-lightwave-arm64" "$plugin_dir/bin/govee-lightwave-amd64"
(cd "$root_dir/plugin" && GOOS=windows GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o "$plugin_dir/bin/govee-lightwave.exe" .)
if command -v streamdeck >/dev/null; then
  (cd "$root_dir" && streamdeck validate "com.dinksf.govee-lightwave.sdPlugin")
fi
