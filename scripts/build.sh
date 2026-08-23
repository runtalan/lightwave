#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
export PATH="${HOME}/go/bin:${PATH}"
cd "$ROOT"

wails build "$@"

APP="${ROOT}/build/bin/lightwave.app"
BIN="${APP}/Contents/MacOS/lightwave"
if [[ ! -f "${BIN}" ]]; then
  echo "error: wails build did not produce ${BIN}" >&2
  exit 1
fi

# Finder/open does not use the project cwd. Ship .env next to the binary resources
# so cloud discovery still works. The key is not printed.
if [[ -f "${ROOT}/.env" ]]; then
  mkdir -p "${APP}/Contents/Resources"
  cp "${ROOT}/.env" "${APP}/Contents/Resources/.env"
  chmod 600 "${APP}/Contents/Resources/.env" || true
fi

# Unsigned local Wails bundles are often reported as "damaged" by Gatekeeper.
codesign --force --deep --sign - "${APP}"
xattr -cr "${APP}" || true

echo "Built ${APP}"
echo "Launch: open \"${APP}\""
echo "Binary: ${BIN}"
