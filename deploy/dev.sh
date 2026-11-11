#!/bin/sh
# Local preview: optional frontend-only mock mode, or separate Go API + Vite.
set -eu
cd "$(dirname "$0")/.."
mode=${1:-api}
if [ "$mode" != api ] && [ "$mode" != mock ]; then
  echo "Usage: sh deploy/dev.sh [api|mock]" >&2
  exit 2
fi
if [ ! -d web/node_modules ]; then
  (cd web && npm ci)
fi
if [ "$mode" = mock ]; then
  cd web
  exec npm run dev
fi
sh deploy/build-backend.sh
listen=${HM_DEV_LISTEN:-127.0.0.1:18080}
data=${HM_DEV_DATA_DIR:-/tmp/harbormaster-separated-dev}
if ./deploy/bin/hm-server healthcheck --listen "$listen" >/dev/null 2>&1; then
  echo "An API is already using $listen; choose another HM_DEV_LISTEN." >&2
  exit 1
fi
./deploy/bin/hm-server serve --dev --frontend api --listen "$listen" --data-dir "$data" &
backend_pid=$!
cleanup() {
  kill "$backend_pid" 2>/dev/null || true
  wait "$backend_pid" 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 130' INT TERM
attempt=0
until ./deploy/bin/hm-server healthcheck --listen "$listen" >/dev/null 2>&1; do
  if ! kill -0 "$backend_pid" 2>/dev/null; then
    echo "Backend did not start; check the log above." >&2
    exit 1
  fi
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 20 ]; then exit 1; fi
  sleep 0.2
done
cd web
HM_SERVER_URL="http://$listen" npm run dev:api
