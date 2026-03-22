#!/usr/bin/env bash
# download-fonts.sh - Safety net only.
# Outfit and JetBrains Mono are bundled in app/assets/fonts/ since v4.0.0.
# Material Symbols Rounded is also bundled. This script is a no-op if all
# three woff2 files are already present (they will be in any normal install).
# It only runs as a repair step if someone manually removes the font files.

set -euo pipefail
FONT_DIR="${1:-/opt/dplaneos/app/assets/fonts}"

all_present=true
for f in outfit.woff2 jetbrains-mono.woff2 MaterialSymbolsRounded.woff2; do
  [ -f "$FONT_DIR/$f" ] || { all_present=false; break; }
done

if [ "$all_present" = "true" ]; then
  echo "  ? All fonts present (bundled)"
  exit 0
fi

echo "  ? One or more fonts missing - attempting npm-based recovery..."

if ! command -v npm &>/dev/null; then
  echo "  ? npm not available, skipping font recovery (UI will use system fallbacks)"
  exit 0
fi

TMP=$(mktemp -d)
trap "rm -rf $TMP" EXIT
cd "$TMP"

npm install --prefer-offline \
  @fontsource-variable/outfit \
  @fontsource-variable/jetbrains-mono 2>/dev/null

[ -f "node_modules/@fontsource-variable/outfit/files/outfit-latin-wght-normal.woff2" ] && \
  cp "node_modules/@fontsource-variable/outfit/files/outfit-latin-wght-normal.woff2" \
     "$FONT_DIR/outfit.woff2" && echo "  ? Outfit recovered"

[ -f "node_modules/@fontsource-variable/jetbrains-mono/files/jetbrains-mono-latin-wght-normal.woff2" ] && \
  cp "node_modules/@fontsource-variable/jetbrains-mono/files/jetbrains-mono-latin-wght-normal.woff2" \
     "$FONT_DIR/jetbrains-mono.woff2" && echo "  ? JetBrains Mono recovered"

echo "Font recovery complete."

