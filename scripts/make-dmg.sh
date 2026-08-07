#!/bin/bash
# Упаковывает готовый Brigade.app в стандартный macOS DMG с ярлыком Applications.
set -euo pipefail

APP="${1:?usage: make-dmg.sh <Brigade.app> <output.dmg>}"
OUT="${2:?usage: make-dmg.sh <Brigade.app> <output.dmg>}"

if [ ! -d "$APP" ]; then
  echo "make-dmg: приложение не найдено: $APP" >&2
  exit 1
fi

REPO="$(cd "$(dirname "$0")/.." && pwd)"
TMP="$(mktemp -d)"
STAGE="$TMP/stage"
FINAL_DMG="$TMP/Brigade.dmg"

cleanup() {
  rm -rf "$TMP"
}
trap cleanup EXIT

mkdir -p "$STAGE/.background" "$(dirname "$OUT")"
ditto "$APP" "$STAGE/Brigade.app"
ln -s /Applications "$STAGE/Applications"
cp "$REPO/packaging/macos/dmg-background.png" "$STAGE/.background/background.png"
cp "$REPO/packaging/macos/dmg.DS_Store" "$STAGE/.DS_Store"

hdiutil create -quiet -volname "Brigade Installer" -srcfolder "$STAGE" \
  -fs HFS+ -format UDZO -imagekey zlib-level=9 "$FINAL_DMG"
mv -f "$FINAL_DMG" "$OUT"

echo "make-dmg: собрано $OUT"
