#!/bin/sh
set -eu

DATA_DIR="${BRIGADE_DATA_DIR:-/srv/brigade}"
PORT="${BRIGADE_PORT:-8080}"
VERSION="${BRIGADE_VERSION:-latest}"
CONTAINER="${BRIGADE_CONTAINER_NAME:-brigade}"
ENV_FILE="$DATA_DIR/brigade.env"

if ! command -v docker >/dev/null 2>&1; then
  echo "Docker is required: https://docs.docker.com/engine/install/" >&2
  exit 1
fi
if docker inspect "$CONTAINER" >/dev/null 2>&1; then
  echo "Container $CONTAINER already exists. See the update guide: https://grigory51.github.io/brigade/docs/installation/update.html" >&2
  exit 1
fi
if [ -e "$ENV_FILE" ] || [ -e "$DATA_DIR/brigade.db" ]; then
  echo "Existing Brigade data found in $DATA_DIR. Refusing to replace its encryption key; use the update guide instead." >&2
  exit 1
fi

umask 077
mkdir -p "$DATA_DIR/workspace" "$DATA_DIR/agent-home" "$DATA_DIR/memory" "$DATA_DIR/plugins"
JWT_SECRET="$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')"
ADMIN_PASSWORD="${BRIGADE_ADMIN_PASSWORD:-$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')}"
{
  echo "BRIGADE_MODE=docker"
  echo "BRIGADE_SQLITE_PATH=$DATA_DIR/brigade.db"
  echo "BRIGADE_WORK_DIR=$DATA_DIR/workspace"
  echo "BRIGADE_AGENT_HOME_DIR=$DATA_DIR/agent-home"
  echo "BRIGADE_MEMORY__DIR=$DATA_DIR/memory"
  echo "BRIGADE_PLUGINS_DIR=$DATA_DIR/plugins"
  echo "BRIGADE_AGENT_IMAGE=ghcr.io/grigory51/brigade-agent:$VERSION"
  echo "BRIGADE_JWT__SECRET=$JWT_SECRET"
  echo "BRIGADE_SEED__USERNAME=admin"
  echo "BRIGADE_SEED__PASSWORD=$ADMIN_PASSWORD"
} > "$ENV_FILE"

docker run -d \
  --name "$CONTAINER" \
  --restart unless-stopped \
  --pull always \
  -p "$PORT:8080" \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v "$DATA_DIR:$DATA_DIR" \
  --env-file "$ENV_FILE" \
  "ghcr.io/grigory51/brigade:$VERSION" >/dev/null

echo "Brigade is starting at http://localhost:$PORT"
echo "Login: admin"
echo "Password: $ADMIN_PASSWORD"
echo "Configuration: $ENV_FILE"
