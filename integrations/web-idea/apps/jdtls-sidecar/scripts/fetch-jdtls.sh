#!/usr/bin/env bash
# Download Eclipse JDT LS into apps/jdtls-sidecar/dist
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIST="$ROOT/dist"
mkdir -p "$DIST"
# Official snapshot build (update URL if 404)
URL="${WEBIDEA_JDTLS_URL:-https://download.eclipse.org/jdtls/snapshots/jdt-language-server-latest.tar.gz}"
ARCHIVE="$DIST/jdtls.tar.gz"
if [[ -d "$DIST/bin" ]] || ls "$DIST"/plugins/org.eclipse.equinox.launcher_*.jar >/dev/null 2>&1; then
  echo "jdtls already present under $DIST"
  exit 0
fi
echo "Downloading jdtls from $URL"
curl -fsSL -o "$ARCHIVE" "$URL"
tar -xzf "$ARCHIVE" -C "$DIST"
rm -f "$ARCHIVE"
echo "Installed to $DIST"
ls "$DIST" | head
