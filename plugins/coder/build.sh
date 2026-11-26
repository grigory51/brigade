#!/usr/bin/env bash
set -euo pipefail

PLUGIN_ROOT="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$PLUGIN_ROOT/../.." && pwd)"
VERSION="${VERSION:-$(git -C "$REPO_ROOT" describe --tags --abbrev=0)}"
VERSION="${VERSION#v}"
if [[ ! "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$ ]]; then
  echo "VERSION must be a Brigade semver tag (for example VERSION=v1.2.3)." >&2
  exit 1
fi

TARGET_OS="${GOOS:-$(go env GOOS)}"
case "$TARGET_OS" in
  darwin|linux) ;;
  *) echo "Coder supports darwin and linux only." >&2; exit 1 ;;
esac

# Изолированная staging-директория не удаляет артефакты параллельной сборки.
mkdir -p "$PLUGIN_ROOT/dist"
STAGING="$(mktemp -d "$PLUGIN_ROOT/dist/package.XXXXXXXX")"
trap 'rm -rf -- "$STAGING"' EXIT
mkdir -p "$STAGING/package/server" "$STAGING/package/ui"

# Только существующий frontend toolchain; install/сборка выполняются по явному запуску скрипта.
npm --prefix "$PLUGIN_ROOT/ui" ci --no-audit --no-fund
VITE_PLUGIN_VERSION="$VERSION" npm --prefix "$PLUGIN_ROOT/ui" run build
cp "$PLUGIN_ROOT/ui/dist/index.html" "$STAGING/package/ui/mcp-app.html"
cp "$PLUGIN_ROOT/ui/cover.svg" "$STAGING/package/ui/cover.svg"
CGO_ENABLED=0 GOOS="$TARGET_OS" go -C "$REPO_ROOT/backend" build -mod=readonly -trimpath \
  -ldflags "-s -w -X main.version=$VERSION" \
  -o "$STAGING/package/server/brigade-coder" ./cmd/brigade-coder
sed "s/__VERSION__/$VERSION/g" "$PLUGIN_ROOT/manifest.json" > "$STAGING/package/manifest.json"
(cd "$STAGING/package" && zip -qr "$STAGING/brigade-coder.mcpb" .)
mv -f "$STAGING/brigade-coder.mcpb" "$PLUGIN_ROOT/brigade-coder.mcpb"
echo "$PLUGIN_ROOT/brigade-coder.mcpb"
