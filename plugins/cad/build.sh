#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
OUT="$ROOT/dist"
PACKAGE="$OUT/package"
VERSION="${VERSION:-$(git -C "$ROOT" describe --tags --abbrev=0 2>/dev/null || echo v0.0.0)}"
VERSION="${VERSION#v}"
rm -rf "$OUT"
rm -f "$ROOT/brigade-cad.mcpb"
mkdir -p "$PACKAGE/server" "$PACKAGE/ui"

npm --prefix "$ROOT/ui" ci
VITE_PLUGIN_VERSION="$VERSION" npm --prefix "$ROOT/ui" run build
cp "$ROOT/ui/dist/index.html" "$PACKAGE/ui/mcp-app.html"
cp "$ROOT/ui/cover.svg" "$PACKAGE/ui/cover.svg"

uv sync --locked --project "$ROOT" --group dev
uv run --locked --project "$ROOT" python -m unittest discover "$ROOT/server" -p "*_test.py"
PYINSTALLER_CONFIG_DIR="$OUT/pyinstaller" uv run --locked --project "$ROOT" pyinstaller --log-level WARN --name cad \
  --exclude-module matplotlib \
  --exclude-module vtk \
  --exclude-module vtkmodules \
  --collect-binaries lib3mf \
  --add-data "$PACKAGE/ui/mcp-app.html:ui" \
  --distpath "$PACKAGE/server" \
  --workpath "$OUT/build" \
  --specpath "$OUT" \
  "$ROOT/server/cad.py"
uv run --locked --project "$ROOT" python "$ROOT/server/smoke_binary.py" "$PACKAGE/server/cad/cad"
sed "s/__VERSION__/$VERSION/g" "$ROOT/manifest.json" > "$PACKAGE/manifest.json"
(cd "$PACKAGE" && zip -qr "$ROOT/brigade-cad.mcpb" .)
uv run --locked --project "$ROOT" python "$ROOT/server/verify_package.py" "$ROOT/brigade-cad.mcpb" "$VERSION"
