#!/usr/bin/env bash
# Launch eclipse.jdt.ls on stdio. Args: <workspace-data-dir> <workspace-root>
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIST="${WEBIDEA_JDTLS_HOME:-$ROOT/dist}"
DATA_DIR="${1:?data dir required}"
WS_ROOT="${2:?workspace root required}"
mkdir -p "$DATA_DIR"

LAUNCHER="$(ls "$DIST"/plugins/org.eclipse.equinox.launcher_*.jar 2>/dev/null | head -1 || true)"
if [[ -z "$LAUNCHER" ]]; then
  echo "jdtls launcher jar not found in $DIST/plugins — run scripts/fetch-jdtls.sh" >&2
  exit 1
fi

OS="$(uname -s)"
ARCH="$(uname -m)"
case "$OS" in
  Darwin)
    if [[ "$ARCH" == "arm64" ]]; then
      CONFIG="$DIST/config_mac_arm"
    else
      CONFIG="$DIST/config_mac"
    fi
    ;;
  Linux)
    if [[ "$ARCH" == "aarch64" || "$ARCH" == "arm64" ]]; then
      CONFIG="$DIST/config_linux_arm"
    else
      CONFIG="$DIST/config_linux"
    fi
    ;;
  *)
    CONFIG="$DIST/config_linux"
    ;;
esac
if [[ ! -d "$CONFIG" ]]; then
  echo "jdtls config dir missing: $CONFIG" >&2
  exit 1
fi

JAVA_BIN="${JAVA_HOME:+$JAVA_HOME/bin/java}"
JAVA_BIN="${JAVA_BIN:-java}"
XMX="${WEBIDEA_JDTLS_XMX:-1g}"
READ_ONLY_FLAGS=()
if [[ "${WEBIDEA_READ_ONLY:-false}" == "true" ]]; then
  # JDT LS reads this as a JVM system property. The similarly named
  # initialization setting controls preferences but does not stop the
  # filesystem bridge from materializing .project/.classpath/.settings.
  READ_ONLY_FLAGS+=("-Djava.import.generatesMetadataFilesAtProjectRoot=false")
fi

exec "$JAVA_BIN" \
  -Declipse.application=org.eclipse.jdt.ls.core.id1 \
  -Dosgi.bundles.defaultStartLevel=4 \
  -Declipse.product=org.eclipse.jdt.ls.core.product \
  -Dlog.level=ERROR \
  -Xmx"$XMX" \
  "${READ_ONLY_FLAGS[@]}" \
  --add-modules=ALL-SYSTEM \
  --add-opens=java.base/java.util=ALL-UNNAMED \
  --add-opens=java.base/java.lang=ALL-UNNAMED \
  -jar "$LAUNCHER" \
  -configuration "$CONFIG" \
  -data "$DATA_DIR"
