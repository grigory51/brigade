#!/bin/bash
# Собирает Brigade.app из готового darwin-бинаря brigade. Кроме бинаря и иконки бандл включает
# Node + npm и манифест агент-рантайма. Claude, Codex и ACP-адаптеры ставятся в каталог
# данных приложения при первом запуске и обновляются на следующих — не раздувают каждый
# Brigade.app. Иконка генерируется встроенными sips + iconutil.
#
#   scripts/make-app.sh <путь-к-бинарю> <выходной-.app>
#
# Пример: scripts/make-app.sh backend/bin/brigade-darwin dist/Brigade.app
set -euo pipefail

BIN="${1:?usage: make-app.sh <binary> <output.app>}"
OUT="${2:?usage: make-app.sh <binary> <output.app>}"

REPO="$(cd "$(dirname "$0")/.." && pwd)"
PKG="$REPO/packaging/macos"
AGENT_DOCKERFILE="$REPO/packaging/docker/agent/Dockerfile"

# Версии агент-рантайма. Версию адаптера НЕ дублируем: единственный источник —
# `ARG ACP_ADAPTER_VERSION` в образе агента, иначе .app и контейнер тихо разъезжаются
# (так и случилось: скрипт застрял на ^0.57, пока образ ушёл на ^0.62). claude-code в
# обоих местах ставится latest.
ADAPTER_VERSION="$(sed -n 's/^ARG ACP_ADAPTER_VERSION=//p' "$AGENT_DOCKERFILE")"
if [ -z "$ADAPTER_VERSION" ]; then
  echo "make-app: не нашёл ARG ACP_ADAPTER_VERSION в $AGENT_DOCKERFILE" >&2
  exit 1
fi
CODEX_VERSION="$(sed -n 's/^ARG CODEX_ACP_VERSION=//p' "$AGENT_DOCKERFILE")"
if [ -z "$CODEX_VERSION" ]; then
  echo "make-app: не нашёл ARG CODEX_ACP_VERSION в $AGENT_DOCKERFILE" >&2
  exit 1
fi
if [ ! -f "$BIN" ]; then
  echo "make-app: бинарь не найден: $BIN" >&2
  exit 1
fi

# Чистый бандл.
rm -rf "$OUT"
mkdir -p "$OUT/Contents/MacOS" "$OUT/Contents/Resources"

# Версия бандла = последний git-тег (единственный источник версии проекта). Её показывает
# системное меню «О программе» — отдельного пункта с версией в интерфейсе нет.
APP_VERSION="$(git -C "$REPO" describe --tags --abbrev=0 2>/dev/null | sed 's/^v//')"
APP_VERSION="${APP_VERSION:-0.0.0}"
sed "s/__VERSION__/$APP_VERSION/g" "$PKG/Info.plist" > "$OUT/Contents/Info.plist"

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

# --- Node + npm для устанавливаемого при первом старте агент-рантайма. ---
RES="$OUT/Contents/Resources"
DL="$(mktemp -d)"

# node (latest v22, как node:22 в docker-образе). Имя тарбола резолвим из dist-листинга (пара
# КБ), сам тарбол (~30 МБ) кешируем между сборками — не тянем каждый раз.
CACHE="${XDG_CACHE_HOME:-$HOME/.cache}/brigade-make-app"
mkdir -p "$CACHE"
NODE_TARBALL="$(curl -fsSL https://nodejs.org/dist/latest-v22.x/ 2>/dev/null \
  | grep -o 'node-v22[0-9.]*-darwin-arm64.tar.gz' | head -1)"
if [ -z "$NODE_TARBALL" ]; then
  # Листинг недоступен (офлайн) — берём самый свежий тарбол из кеша.
  NODE_TARBALL="$(cd "$CACHE" && ls -1 node-v22*-darwin-arm64.tar.gz 2>/dev/null | sort | tail -1)"
fi
if [ -z "$NODE_TARBALL" ]; then
  echo "make-app: не удалось определить версию node (нет сети и пустой кеш)" >&2
  exit 1
fi
NODE_CACHED="$CACHE/$NODE_TARBALL"
if [ -f "$NODE_CACHED" ]; then
  echo "make-app: node из кеша ($NODE_TARBALL)"
else
  echo "make-app: скачиваю node ($NODE_TARBALL)…"
  if ! curl -fsSL "https://nodejs.org/dist/latest-v22.x/${NODE_TARBALL}" -o "$NODE_CACHED.tmp"; then
    rm -f "$NODE_CACHED.tmp"
    echo "make-app: не удалось скачать node" >&2
    exit 1
  fi
  mv "$NODE_CACHED.tmp" "$NODE_CACHED"
fi
tar xzf "$NODE_CACHED" -C "$DL"
NODE_SRC="$DL/${NODE_TARBALL%.tar.gz}"
# npm нужен desktop-приложению для первой установки и обновления агентов. Копируем его из
# того же официального дистрибутива Node, чтобы не зависеть от npm на машине пользователя.
mkdir -p "$RES/node/bin" "$RES/node/lib/node_modules"
cp "$NODE_SRC/bin/node" "$RES/node/bin/node"
cp -R "$NODE_SRC/lib/node_modules/npm" "$RES/node/lib/node_modules/npm"
ln -s ../lib/node_modules/npm/bin/npm-cli.js "$RES/node/bin/npm"
ln -s ../lib/node_modules/npm/bin/npx-cli.js "$RES/node/bin/npx"

# Пакеты не входят в .app. Манифест — единственный вход ensureDesktopAgentRuntime: pinned
# версии адаптеров остаются синхронизированы с Dockerfile, CLI обновляются по тегу latest.
cat > "$RES/agent-package.json" <<EOF
{
  "name": "brigade-agent-runtime",
  "private": true,
  "dependencies": {
    "@agentclientprotocol/claude-agent-acp": "${ADAPTER_VERSION}",
    "@agentclientprotocol/codex-acp": "${CODEX_VERSION}",
    "@anthropic-ai/claude-code": "latest",
    "@openai/codex": "latest"
  }
}
EOF

# npm_cached <подкаталог-кеша> <куда-в-бандл> [specs…] — ставит пакеты в ПЕРСИСТЕНТНОЕ дерево
# в кеше и клонирует готовое в бандл. Свежий node_modules каждой сборки перекачивал сотни
# мегабайт; в кеше npm сверяет уже установленное и лезет в сеть только за реально
# изменившимся (@latest остаётся latest — свежесть не теряем).
npm_cached() {
  local dir="$CACHE/$1" dest="$2"
  shift 2
  ( cd "$dir" && npm install --omit=dev --no-audit --no-fund --loglevel=error \
      --prefer-offline "$@" )
  mkdir -p "$dest"
  # -c — clonefile APFS: копия дерева мгновенна и не занимает места. На не-APFS томе
  # (или старом cp) откатываемся на обычное копирование.
  cp -Rc "$dir/." "$dest/" 2>/dev/null || cp -R "$dir/." "$dest/"
}

# MCP-сервер brigade (render_ui/show_choice) — в docker он в /opt/brigade-mcp; в бандле кладём в
# Resources/brigade-mcp с зависимостями (@modelcontextprotocol/sdk), чтобы local-режим тоже
# показывал A2UI-карточки (напр. черновик заметки в /note). Путь бинарю задаёт prependBundledTools.
# Манифест и скрипт кладём в кеш из репо: правка в репозитории переустановит зависимости.
echo "make-app: ставлю MCP-сервер brigade (render_ui)…"
mkdir -p "$CACHE/brigade-mcp"
cp "$REPO/packaging/docker/agent/mcp/brigade-tools.mjs" \
   "$REPO/packaging/docker/agent/mcp/package.json" "$CACHE/brigade-mcp/"
npm_cached brigade-mcp "$RES/brigade-mcp"

echo "make-app: собрано $OUT (node + npm + mcp; агенты установятся при первом запуске)"
