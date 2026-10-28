#!/bin/sh
set -e

DATA_DIR="${DATA_DIR:-/data}"
mkdir -p "$DATA_DIR"

# Volumes mounted by Docker/Coolify belong to root; hand them to the app user (uid 1000, the same
# uid as earlier releases, so existing volumes keep working), then drop root for good.
if [ "$(id -u)" = "0" ]; then
  chown -R app:app "$DATA_DIR"
  chmod 700 "$DATA_DIR"
  exec su-exec app "$@"
fi

exec "$@"
