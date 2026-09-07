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
helper_dir="$plugin_dir/bin/Govee Lightwave Discovery.app"
mkdir -p "$helper_dir/Contents/MacOS"
cp "$root_dir/native/bluetooth/Info.plist" "$helper_dir/Contents/Info.plist"
helper_build=$(mktemp -d)
trap 'rm -f "$helper_build/arm64" "$helper_build/amd64"; rmdir "$helper_build"' EXIT
swiftc -O -target arm64-apple-macosx13.0 "$root_dir/native/bluetooth/main.swift" -o "$helper_build/arm64"
swiftc -O -target x86_64-apple-macosx13.0 "$root_dir/native/bluetooth/main.swift" -o "$helper_build/amd64"
lipo -create "$helper_build/arm64" "$helper_build/amd64" -output "$helper_dir/Contents/MacOS/govee-bluetooth-discovery"
codesign --force --sign - "$helper_dir"
if command -v streamdeck >/dev/null; then
  (cd "$root_dir" && streamdeck validate "com.dinksf.govee-lightwave.sdPlugin")
fi
