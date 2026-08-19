#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
OUT="$ROOT/dist"
PACKAGE="$OUT/package"
rm -rf "$OUT"
mkdir -p "$PACKAGE/server" "$PACKAGE/ui"

npm --prefix "$ROOT/ui" ci
npm --prefix "$ROOT/ui" run build
cp "$ROOT/ui/dist/index.html" "$PACKAGE/ui/mcp-app.html"
cp "$ROOT/ui/cover.svg" "$PACKAGE/ui/cover.svg"

uv sync --locked --project "$ROOT" --group dev
uv run --locked --project "$ROOT" python -m unittest discover "$ROOT/server" -p "*_test.py"
PYINSTALLER_CONFIG_DIR="$OUT/pyinstaller" uv run --locked --project "$ROOT" pyinstaller --onefile --name cad \
  --exclude-module IPython \
  --exclude-module matplotlib \
  --exclude-module vtk \
  --exclude-module vtkmodules \
  --add-data "$PACKAGE/ui/mcp-app.html:ui" \
  --distpath "$PACKAGE/server" \
  --workpath "$OUT/build" \
  --specpath "$OUT" \
  "$ROOT/server/cad.py"
cp "$ROOT/manifest.json" "$PACKAGE/manifest.json"
rm -f "$ROOT/brigade-cad.mcpb"
(cd "$PACKAGE" && zip -qr "$ROOT/brigade-cad.mcpb" .)
