#!/bin/bash
# Собирает Brigade.app из готового darwin-бинаря brigade. Кроме бинаря и иконки бандл включает
# САМОДОСТАТОЧНЫЙ агент-рантайм (node + claude-agent-acp + claude-code, пиннутые как в
# docker-образе), чтобы local-режим не зависел от глобального npm хоста. Иконка генерируется
# встроенными sips + iconutil; node и npm-пакеты тянутся из сети на этапе сборки.
#
#   scripts/make-app.sh <путь-к-бинарю> <выходной-.app>
#
# Пример: scripts/make-app.sh backend/bin/brigade-darwin dist/Brigade.app
set -euo pipefail

# Версии агент-рантайма. claude-agent-acp/claude-code держим в ногу с docker/claude-agent/Dockerfile.
ADAPTER_SPEC="@agentclientprotocol/claude-agent-acp@^0.57.0"
CLAUDE_SPEC="@anthropic-ai/claude-code@latest"

BIN="${1:?usage: make-app.sh <binary> <output.app>}"
OUT="${2:?usage: make-app.sh <binary> <output.app>}"

REPO="$(cd "$(dirname "$0")/.." && pwd)"
PKG="$REPO/packaging/macos"

if [ ! -f "$BIN" ]; then
  echo "make-app: бинарь не найден: $BIN" >&2
  exit 1
fi

# Чистый бандл.
rm -rf "$OUT"
mkdir -p "$OUT/Contents/MacOS" "$OUT/Contents/Resources"

cp "$PKG/Info.plist" "$OUT/Contents/Info.plist"

# Бинарь + launcher (он же CFBundleExecutable): exec заменяет процесс на месте, поэтому
# webview крутится на главном потоке настоящего app-процесса. Имена РАЗЛИЧАЮТСЯ регистром и
# основой (brigade-bin vs Brigade): APFS/HFS регистронезависимы — одинаковые имена схлопнулись
# бы в один файл.
cp "$BIN" "$OUT/Contents/MacOS/brigade-bin"
chmod +x "$OUT/Contents/MacOS/brigade-bin"
cat > "$OUT/Contents/MacOS/Brigade" <<'LAUNCH'
#!/bin/sh
here="$(cd "$(dirname "$0")" && pwd)"
exec "$here/brigade-bin" desktop
LAUNCH
chmod +x "$OUT/Contents/MacOS/Brigade"

# Иконка: 1024-PNG → iconset (стандартные размеры) → .icns.
ICONSET="$(mktemp -d)/AppIcon.iconset"
mkdir -p "$ICONSET"
for sz in 16 32 128 256 512; do
  sips -z "$sz" "$sz"       "$PKG/icon-1024.png" --out "$ICONSET/icon_${sz}x${sz}.png"    >/dev/null
  sips -z $((sz*2)) $((sz*2)) "$PKG/icon-1024.png" --out "$ICONSET/icon_${sz}x${sz}@2x.png" >/dev/null
done
iconutil -c icns "$ICONSET" -o "$OUT/Contents/Resources/AppIcon.icns"

# --- Самодостаточный агент-рантайм: node + claude-agent-acp + claude-code внутри бандла. ---
# Local-режим brigade спавнит claude-agent-acp / claude из PATH; runDesktop ставит эти каталоги
# первыми, поэтому используются встроенные версии, а не глобальный npm хоста.
RES="$OUT/Contents/Resources"
DL="$(mktemp -d)"

# node (latest v22, как node:22 в docker-образе). Точное имя тарбола берём из dist-листинга,
# чтобы не гадать патч-версию.
echo "make-app: скачиваю node (v22, darwin-arm64)…"
NODE_TARBALL="$(curl -fsSL https://nodejs.org/dist/latest-v22.x/ \
  | grep -o 'node-v22[0-9.]*-darwin-arm64.tar.gz' | head -1)"
if [ -z "$NODE_TARBALL" ]; then
  echo "make-app: не удалось определить версию node" >&2
  exit 1
fi
curl -fsSL "https://nodejs.org/dist/latest-v22.x/${NODE_TARBALL}" | tar xz -C "$DL"
NODE_SRC="$DL/${NODE_TARBALL%.tar.gz}"
# Рантайму нужен только бинарь node (самодостаточен, ICU внутри); npm/lib в бандл не кладём.
mkdir -p "$RES/node/bin"
cp "$NODE_SRC/bin/node" "$RES/node/bin/node"

# Агент-пакеты ставим ХОСТОВЫМ npm: node_modules портативен (JS + prebuilt darwin-arm64
# бинарники, нативных ABI-аддонов нет), поэтому сборка любым npm, а рантайм — встроенным node.
# Specs передаём прямо в npm install — он сам впишет их в package.json.
echo "make-app: ставлю агент-пакеты ($ADAPTER_SPEC, $CLAUDE_SPEC)…"
mkdir -p "$RES/agent"
echo '{ "name": "brigade-agent-bundle", "private": true }' > "$RES/agent/package.json"
( cd "$RES/agent" && npm install --omit=dev --no-audit --no-fund --loglevel=error \
    "$CLAUDE_SPEC" "$ADAPTER_SPEC" )

echo "make-app: собрано $OUT (self-contained: node + claude-agent-acp + claude-code)"
